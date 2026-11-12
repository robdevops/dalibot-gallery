// Signing in at /login, the cookie, and what an anonymous visitor gets.
const { test, expect, TOKEN, basic } = require('./helpers')

test.use({ storageState: { cookies: [], origins: [] } }) // start signed out

test('an anonymous visitor is sent to /login and gets no data', async ({ page, request }) => {
  const r = await page.goto('/')
  expect(new URL(page.url()).pathname).toBe('/login')
  expect(r.status()).toBe(200)
  await expect(page.getByLabel('Access token')).toBeFocused()
  await expect(page.getByLabel('Access token')).toHaveAttribute('type', 'password')
  await expect(page.locator('#grid')).toHaveCount(0)

  // Without the cookie the API and the images answer 401 and carry nothing.
  for (const path of ['/api/tiles', '/api/tiles/new?since=', '/img/AAAAAAAAAAAAAAAAAAAAAA-240.webp?v=1', '/img/AAAAAAAAAAAAAAAAAAAAAA.jpg']) {
    const res = await request.get(path, { maxRedirects: 0 })
    expect(res.status(), path).toBe(401)
    expect((await res.text()).length, path).toBeLessThan(200)
  }
  expect((await request.get('/healthz')).status()).toBe(200)
  // The static files of the login page are public; the app script is harmless without a session.
  expect((await request.get('/app.css')).status()).toBe(200)
})

test('a wrong token is refused, the right one signs in and sets a safe cookie', async ({ page, context, watched }) => {
  await page.goto('/login')
  await page.getByLabel('Access token').fill('not-the-right-token-000000')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toHaveText('That token is not right. Try again.')
  expect(new URL(page.url()).pathname).toBe('/login')
  expect(await context.cookies()).toEqual([])
  // The browser notes the 401 of the refused form post in the console; nothing else.
  watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/\b401\b/.test(p)))
  await page.goto('/')
  expect(new URL(page.url()).pathname).toBe('/login')

  await page.getByLabel('Access token').fill(TOKEN())
  await page.getByLabel('Access token').press('Enter')
  await page.waitForURL(u => u.pathname === '/')
  await expect(page.locator('#grid .tile').first()).toBeVisible()
  await expect.poll(() => page.locator('#grid .tile img').first().evaluate(i => i.naturalWidth)).toBeGreaterThan(0)

  const cookies = await context.cookies()
  expect(cookies.length).toBe(1)
  const c = cookies[0]
  expect(c.httpOnly).toBe(true)
  expect(c.secure).toBe(true)
  expect(c.sameSite).toBe('Strict')
  expect(c.path).toBe('/')
  expect(c.value).not.toContain(TOKEN())
  expect(c.value.length).toBeGreaterThanOrEqual(32) // an HMAC, not the token
  expect(await page.evaluate(() => document.cookie), 'not readable from script').toBe('')

  // The session lasts across reloads and new tabs; /login then goes straight to the gallery or shows the form.
  await page.reload()
  await expect(page.locator('#grid .tile').first()).toBeVisible()
  const tab = await context.newPage()
  await tab.goto('/')
  expect(new URL(tab.url()).pathname).toBe('/')
  await expect(tab.locator('#grid .tile').first()).toBeVisible()
  await tab.close()

  // A forged cookie value does not work.
  await context.clearCookies()
  await context.addCookies([{ ...c, value: c.value.replace(/.$/, ch => (ch === 'A' ? 'B' : 'A')) }])
  await page.goto('/')
  expect(new URL(page.url()).pathname).toBe('/login')
})

test('the token also works as the HTTP Basic password', async ({ request }) => {
  const ok = await request.get('/api/tiles?limit=1', { headers: basic() })
  expect(ok.status()).toBe(200)
  const bad = await request.get('/api/tiles?limit=1', { headers: { Authorization: 'Basic ' + Buffer.from('e2e:wrong-token-wrong-token').toString('base64') } })
  expect(bad.status()).toBe(401)
})

test('every page carries the security headers', async ({ page, request }) => {
  const csp = "default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
  const paths = ['/login', '/healthz', '/app.css', '/app.js', '/api/tiles', '/nothing-here']
  for (const path of paths) {
    const h = (await request.get(path, { maxRedirects: 0 })).headers()
    expect(h['content-security-policy'], path).toBe(csp)
    expect(h['x-content-type-options'], path).toBe('nosniff')
    expect(h['referrer-policy'], path).toBe('no-referrer')
    expect(h['permissions-policy'], path).toBeTruthy()
  }
  for (const path of ['/', '/api/tiles?limit=3']) {
    const h = (await request.get(path, { headers: basic() })).headers()
    expect(h['content-security-policy'], path).toBe(csp)
    expect(h['x-content-type-options'], path).toBe('nosniff')
  }
})
