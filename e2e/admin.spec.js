// Hiding images as an admin: the trash can in the viewer, and the page of
// hidden images, on the token-protected stack and on the public one. And the
// other side: someone who is not an admin is sent no admin script, has no
// controls and cannot use the routes.
const { test, expect, ADMIN_TOKEN, PUBLIC_URL, CHAT_IDS, basic, domKeys } = require('./helpers')

const basicAdmin = () => ({ Authorization: 'Basic ' + Buffer.from('e2e:' + ADMIN_TOKEN()).toString('base64') })
const ADMIN_FILES = ['/admin.js', '/admin.css']
const lb = page => page.locator('#lb')
const trash = page => page.locator('#trash')
const openKey = page => page.evaluate(() => /^#\/p\/([\w-]{22})$/.exec(location.hash)?.[1] || '')

// Paths of everything the page requests, from now on.
function requested(page) {
  const paths = []
  page.on('request', r => paths.push(new URL(r.url()).pathname))
  return paths
}

// Keys of the listing, read as a viewer (headers: the view token on the main stack, none on the public one).
async function listedKeys(request, headers) {
  const keys = []
  let cursor = ''
  for (let i = 0; i < 100; i++) {
    const r = await request.get('/api/tiles?limit=120' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { headers })
    expect(r.status()).toBe(200)
    const j = await r.json()
    keys.push(...j.tiles.map(t => t.key))
    cursor = j.next
    if (!cursor) return keys
  }
  throw new Error('the API never reached the last page')
}

const hiddenKeys = async request =>
  (await (await request.get('/api/admin/hidden', { headers: basicAdmin() })).json()).hidden.map(h => h.key)

// Whatever a test leaves hidden is restored, so the other specs see the whole gallery.
async function restoreAll(request) {
  for (const key of await hiddenKeys(request)) {
    expect((await request.post('/api/admin/restore/' + key, { headers: basicAdmin() })).status()).toBe(200)
  }
}

async function signInAsAdmin(page, label) {
  await page.goto('/login')
  await page.getByLabel(label).fill(ADMIN_TOKEN())
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.waitForURL(u => u.pathname === '/')
  await expect(page.locator('#grid .tile').first()).toBeVisible()
}

// Open the tile with this key in the viewer, and wait for its image.
async function open(page, key) {
  await page.locator(`#grid .tile[data-key="${key}"] a`).click()
  await expect(lb(page)).toHaveAttribute('open', '')
  expect(await openKey(page)).toBe(key)
  await expect.poll(() => page.locator('#full').evaluate(i => i.complete && i.naturalWidth)).toBeGreaterThan(0)
}

// Someone who is not an admin: no admin files requested, none served, no
// controls in the page or the viewer, and the routes answer 404.
async function expectNoAdminParts(page, request, headers, watched) {
  const paths = requested(page)
  await page.goto('/')
  await expect(page.locator('#grid .tile').first()).toBeVisible()
  expect(await page.locator('html').getAttribute('data-admin')).toBeNull()
  await page.locator('#grid .tile a').first().click()
  await expect(lb(page)).toHaveAttribute('open', '')
  await expect.poll(() => page.locator('#full').evaluate(i => i.naturalWidth)).toBeGreaterThan(0)
  await expect(page.locator('#trash')).toHaveCount(0)
  for (const name of ['Hide this image from the gallery', 'Restore']) {
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0)
  }
  await expect(page.getByRole('link', { name: 'Hidden images' })).toHaveCount(0)
  const html = await page.content()
  for (const word of ['admin.js', 'admin.css', '/admin/', 'Hidden images']) expect(html, word).not.toContain(word)
  expect(paths.filter(p => /admin/.test(p)), 'requests for admin files').toEqual([])
  expect(paths).toContain('/app.js')

  // Asked for outright, the files and the routes are not there.
  const key = await openKey(page)
  for (const path of [...ADMIN_FILES, '/admin/hidden', '/api/admin/hidden']) {
    expect((await request.get(path, { headers })).status(), path).toBe(404)
  }
  for (const path of ['/api/admin/hide/' + key, '/api/admin/restore/' + key, '/api/admin/hide/AAAAAAAAAAAAAAAAAAAAAA']) {
    const r = await request.post(path, { headers })
    expect(r.status(), path).toBe(404)
    expect(await r.text(), path).toBe('{"error":"not found"}\n')
  }
  // From the page itself, with its cookie and as the browser sends it.
  const status = await page.evaluate(async k => (await fetch('/api/admin/hide/' + k, { method: 'POST' })).status, key)
  expect(status).toBe(404)
  watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/\b404\b/.test(p)))
  expect(await listedKeys(request, headers)).toContain(key)
}

test.describe('a gallery that needs the token to view', () => {
  test.afterEach(async ({ request }) => { await restoreAll(request) })

  test('a viewer is sent no admin script and has no controls', async ({ page, request, watched }) => {
    await expectNoAdminParts(page, request, basic(), watched)
  })

  test('the admin hides an image with one press of the trash can, and restores it from the page of hidden images', async ({ page, request }) => {
    const paths = requested(page)
    await signInAsAdmin(page, 'Access token')
    for (const f of ADMIN_FILES) expect(paths, f).toContain(f)
    const before = await domKeys(page)
    const [target, following, after] = [before[2], before[3], before[4]]
    await open(page, target)

    // In the top left corner, the same size as the close button, and gone with the other overlays.
    const t = await trash(page).boundingBox(), c = await page.locator('#x').boundingBox(), stage = await page.locator('.stage').boundingBox()
    expect([t.width, t.height]).toEqual([c.width, c.height])
    expect([t.x, t.y]).toEqual([stage.x, stage.y])
    expect(c.x).toBeGreaterThan(t.x)
    await expect(trash(page)).toHaveAccessibleName('Hide this image from the gallery')
    await page.locator('#full').click()
    await expect(trash(page)).toBeHidden()
    await page.locator('#full').click()
    await expect(trash(page)).toBeVisible()
    // A tap on the image, a swipe or the arrow keys hide nothing.
    expect(await hiddenKeys(request)).toEqual([])

    // One press: the tile is gone, the viewer has moved on, nothing is asked and nothing stays on screen.
    await trash(page).click()
    await expect.poll(() => openKey(page)).toBe(following)
    await expect(page.locator(`#grid .tile[data-key="${target}"]`)).toHaveCount(0)
    expect(await domKeys(page)).not.toContain(target)
    expect(await page.locator('#strip > li').count()).toBe(before.length - 1)
    await expect(page.getByRole('button', { name: /Cancel|Hide image|Undo/ })).toHaveCount(0)
    await expect(page.locator('#adm, #undo')).toHaveCount(0)
    expect(await hiddenKeys(request)).toEqual([target])
    expect(await listedKeys(request, basic())).not.toContain(target)
    expect((await request.get(`/img/${target}.jpg`, { headers: basic() })).status()).toBe(404)

    // A double press hides one image, not two.
    await page.keyboard.press('Escape')
    await page.waitForTimeout(600) // the button ignores presses for half a second after a hide
    await open(page, following)
    await trash(page).dblclick()
    await expect.poll(() => openKey(page)).toBe(after)
    await page.waitForTimeout(700)
    expect((await hiddenKeys(request)).sort()).toEqual([target, following].sort()) // two, not three
    await page.keyboard.press('Escape')

    // The hidden images are on a page of their own, each with Restore.
    await page.getByRole('link', { name: 'Hidden images' }).click()
    await expect(page.locator('#hidden li')).toHaveCount(2)
    await page.locator(`[data-restore="${target}"]`).click()
    await expect(page.locator('#hidden li')).toHaveCount(1)
    expect(await hiddenKeys(request)).toEqual([following])
    expect(await listedKeys(request, basic())).toContain(target)
    expect((await request.get(`/img/${target}.jpg`, { headers: basic() })).status()).toBe(200)
  })

  test('a failed request changes nothing', async ({ page, request, watched }) => {
    await signInAsAdmin(page, 'Access token')
    const key = (await domKeys(page))[1]
    await open(page, key)
    await page.route('**/api/admin/hide/*', route => route.fulfill({ status: 500, body: '{"error":"no"}' }))
    await trash(page).click()
    await expect(page.locator('#live')).toHaveText('Could not hide the image')
    expect(await openKey(page)).toBe(key)
    await expect(page.locator(`#grid .tile[data-key="${key}"]`)).toHaveCount(1)
    expect(await hiddenKeys(request)).toEqual([])
    // And the button still works once the request does.
    await page.unroute('**/api/admin/hide/*')
    await trash(page).click()
    await expect.poll(() => hiddenKeys(request)).toEqual([key])
    watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/\b500\b/.test(p)))
  })
})

test.describe('a public gallery', () => {
  test.skip(() => !PUBLIC_URL(), 'no public stack in this run (set E2E_PUBLIC_URL)')
  test.use({ baseURL: PUBLIC_URL(), storageState: { cookies: [], origins: [] } })
  test.afterEach(async ({ request }) => { await restoreAll(request) })

  test('a visitor is sent no admin script and has no controls', async ({ page, request, watched }) => {
    await expectNoAdminParts(page, request, {}, watched)
  })

  test('what the admin hides is gone for every visitor at once, and back after Restore', async ({ page, request, browser }) => {
    // A visitor's own page, open before anything is hidden.
    const visitor = await browser.newContext({ baseURL: PUBLIC_URL() })
    const theirs = await visitor.newPage()
    await theirs.goto('/')
    await expect(theirs.locator('#grid .tile').first()).toBeVisible()
    const first = (await domKeys(theirs))[0]
    const v = (await (await request.get('/api/tiles?limit=1')).json()).v
    const urls = [`/img/${first}.jpg`, `/img/${first}-240.webp?v=${v}`, `/img/${first}-480.webp?v=${v}`]
    for (const u of urls) expect((await request.get(u)).headers()['cache-control']).toBe('public, max-age=31536000, immutable')

    await signInAsAdmin(page, 'Admin token')
    await open(page, first)
    await trash(page).click()
    await expect.poll(() => hiddenKeys(request)).toEqual([first])

    // No login, no cookie: the listing, the page without JavaScript and the image URLs.
    expect(await listedKeys(request, {})).not.toContain(first)
    expect(await (await request.get('/')).text()).not.toContain(first)
    for (const u of urls) {
      const r = await request.get(u)
      expect(r.status(), u).toBe(404)
      expect(r.headers()['cache-control'], u).toBe('no-store')
      expect(await r.text()).toBe(await (await request.get(u.replace(first, 'AAAAAAAAAAAAAAAAAAAAAA'))).text())
    }
    await theirs.reload()
    await expect(theirs.locator('#grid .tile').first()).toBeVisible()
    expect(await domKeys(theirs)).not.toContain(first)
    await expect(theirs.locator('#trash')).toHaveCount(0)

    // The admin's list and page carry no chat id (the listing an admin gets does; these do not).
    const list = await (await request.get('/api/admin/hidden', { headers: basicAdmin() })).text()
    expect(JSON.parse(list).hidden.map(h => h.key)).toEqual([first])
    const html = await (await request.get('/admin/hidden', { headers: basicAdmin() })).text()
    expect(html).toContain(`data-restore="${first}"`)
    for (const chat of CHAT_IDS) {
      expect(list).not.toContain(chat)
      expect(html).not.toContain(chat)
    }

    await page.goto('/admin/hidden')
    await page.locator(`[data-restore="${first}"]`).click()
    await expect(page.locator('#hidden li')).toHaveCount(0)
    expect(await listedKeys(request, {})).toContain(first)
    for (const u of urls) expect((await request.get(u)).status(), u).toBe(200)
    await theirs.reload()
    await expect(theirs.locator('#grid .tile').first()).toBeVisible()
    expect((await domKeys(theirs))[0]).toBe(first)
    await visitor.close()
  })
})
