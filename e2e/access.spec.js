// Who may view and who may administer: the admin session next to the view
// session on the token-protected stack, and the public stack (GALLERY_PUBLIC=1),
// where nobody signs in to view and nothing a visitor receives holds a chat id.
const { test, expect, TOKEN, ADMIN_TOKEN, PUBLIC_URL, CHAT_IDS, basic, viewportImagesLoaded } = require('./helpers')

const basicAdmin = () => ({ Authorization: 'Basic ' + Buffer.from('e2e:' + ADMIN_TOKEN()).toString('base64') })
const isAdminPage = page => page.evaluate(() => document.documentElement.dataset.admin === '1')
const footer = page => page.locator('footer.foot')

test.describe('a gallery that needs the token to view', () => {
  test('a view session is not an admin; the admin token at the same form is', async ({ page, context }) => {
    // The saved session is a view session.
    await page.goto('/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    expect(await isAdminPage(page)).toBe(false)
    expect(await page.locator('html').getAttribute('data-admin')).toBeNull()
    const viewCookie = (await context.cookies())[0].value

    // The foot of the page links to the sign-in, discreetly, and offers to sign out.
    await expect(footer(page).getByRole('link', { name: 'Admin' })).toHaveAttribute('href', '/login')
    await expect(footer(page).getByRole('button', { name: 'Sign out' })).toHaveCount(1)
    await page.goto('/login')
    expect(new URL(page.url()).pathname, 'a viewer can reach the form to sign in as admin').toBe('/login')
    await page.getByLabel('Access token').fill(ADMIN_TOKEN())
    await page.getByRole('button', { name: 'Sign in' }).click()
    await page.waitForURL(u => u.pathname === '/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    expect(await isAdminPage(page)).toBe(true)
    await expect(footer(page).getByRole('link', { name: 'Admin' })).toHaveCount(0)

    // A different cookie value, as safe as the other, and no more readable.
    const cookies = await context.cookies()
    expect(cookies.length).toBe(1)
    const c = cookies[0]
    expect(c.value).not.toBe(viewCookie)
    expect(c.value).not.toContain(ADMIN_TOKEN())
    expect([c.httpOnly, c.secure, c.sameSite, c.path]).toEqual([true, true, 'Strict', '/'])
    expect(await page.evaluate(() => document.cookie)).toBe('')
    // An admin has nothing more to sign in for.
    await page.goto('/login')
    expect(new URL(page.url()).pathname).toBe('/')

    // Signing out drops the session: viewing needs the token again.
    await footer(page).getByRole('button', { name: 'Sign out' }).click()
    await page.waitForURL(u => u.pathname === '/login')
    expect(await context.cookies()).toEqual([])

    // The view cookie, put back, is a view session and nothing more.
    await context.addCookies([{ ...c, value: viewCookie }])
    await page.goto('/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    expect(await isAdminPage(page)).toBe(false)
  })

  test('each token works as the Basic password, with its own level', async ({ request }) => {
    const view = await request.get('/', { headers: basic() })
    expect(view.status()).toBe(200)
    expect(await view.text()).not.toContain('data-admin')
    const admin = await request.get('/', { headers: basicAdmin() })
    expect(admin.status()).toBe(200)
    expect(await admin.text()).toContain('<html lang="en" data-admin="1">')
    // Everyone let into a token-protected gallery gets the record id, as before.
    for (const headers of [basic(), basicAdmin()]) {
      const j = await (await request.get('/api/tiles?limit=1', { headers })).json()
      expect(j.tiles[0].id).toMatch(/^-?\d+:/)
    }
    // Cursors are sealed all the same: nothing in them reads as a chat id.
    const j = await (await request.get('/api/tiles?limit=7', { headers: basic() })).json()
    for (const cursor of [j.next, j.newest]) {
      const raw = Buffer.from(cursor, 'base64url').toString('latin1')
      for (const chat of CHAT_IDS) expect(raw).not.toContain(chat)
    }
  })
})

test.describe('a public gallery', () => {
  test.skip(() => !PUBLIC_URL(), 'no public stack in this run (set E2E_PUBLIC_URL)')
  test.use({ baseURL: PUBLIC_URL(), storageState: { cookies: [], origins: [] } })

  // Every response of a page, headers and body, searched for the chat ids.
  // Sealed cursors are opened as base64 too. Image bodies are left out: they
  // are the sample JPEGs and their thumbnails.
  function chatIdWatch(page) {
    const found = [], scans = []
    let responses = 0
    page.on('response', r => {
      responses++
      scans.push((async () => {
        const where = r.request().method() + ' ' + new URL(r.url()).pathname + new URL(r.url()).search + ' (' + r.status() + ')'
        let text = r.url() + '\n'
        try { text += JSON.stringify(await r.allHeaders()) } catch {}
        if (!/^image\//.test(r.headers()['content-type'] || '')) {
          try { text += '\n' + (await r.body()).toString('latin1') } catch {}
        }
        for (const word of text.match(/[A-Za-z0-9_-]{16,}/g) || []) {
          for (let off = 0; off < 4; off++) text += '\n' + Buffer.from(word.slice(off), 'base64url').toString('latin1')
        }
        for (const chat of CHAT_IDS) if (text.includes(chat)) found.push(where + ' contains chat id ' + chat)
      })())
    })
    return async () => {
      await Promise.race([Promise.all(scans), new Promise(r => setTimeout(r, 5000))])
      return { found, responses }
    }
  }

  test('anyone views without signing in, and never receives a chat id', async ({ page, context, request }) => {
    const done = chatIdWatch(page)
    const r = await page.goto('/')
    expect(r.status()).toBe(200)
    expect(new URL(page.url()).pathname, 'no redirect to a login').toBe('/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    await viewportImagesLoaded(page, '#grid .tile img')
    expect(await isAdminPage(page)).toBe(false)
    expect(await context.cookies(), 'viewing sets no cookie').toEqual([])

    // Scroll to the very end, so every page of the listing passes the watch.
    await expect.poll(async () => {
      await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
      return page.locator('#end').isHidden()
    }, { timeout: 30_000 }).toBe(true)
    const shown = await page.locator('#grid .tile').count()
    expect(shown).toBeGreaterThan(150)

    // The viewer, a deep link and the poll for new images.
    await page.locator('#grid .tile a').first().click()
    await expect(page.locator('#lb')).toHaveAttribute('open', '')
    expect(page.url()).toMatch(/#\/p\/[\w-]{22}$/)
    await expect.poll(() => page.locator('#full').evaluate(i => i.naturalWidth)).toBeGreaterThan(0)
    await page.keyboard.press('ArrowRight')
    await page.keyboard.press('Escape')
    const poll = page.waitForResponse(res => new URL(res.url()).pathname === '/api/tiles/new')
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    expect((await poll).status()).toBe(200)

    // The listing itself, read directly: no "id" anywhere, cursors that say nothing.
    let cursor = '', total = 0
    for (let i = 0; i < 100; i++) {
      const res = await request.get('/api/tiles?limit=120' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''))
      expect(res.status()).toBe(200)
      const text = await res.text()
      expect(text).not.toContain('"id"')
      for (const chat of CHAT_IDS) expect(text).not.toContain(chat)
      const j = JSON.parse(text)
      for (const t of j.tiles) expect(Object.keys(t).sort()).toEqual(['caption', 'command', 'current', 'key', 'label', 'prompt', 'style', 'styles', 'updated'])
      total += j.tiles.length
      for (const c of [j.next, j.newest].filter(Boolean)) {
        const raw = Buffer.from(c, 'base64url').toString('latin1')
        for (const chat of CHAT_IDS) expect(raw).not.toContain(chat)
        expect(raw.startsWith('1|'), 'not the unsealed cursor').toBe(false)
      }
      cursor = j.next
      if (!cursor) break
    }
    expect(total).toBe(shown)
    // A cursor the visitor makes is refused (it would let an id be found by bisection).
    const forged = Buffer.from('1|1700000000|0|-1001111111111:1').toString('base64url')
    for (const path of ['/api/tiles?cursor=', '/api/tiles/new?since=', '/?cursor=']) {
      expect((await request.get(path + forged)).status(), path).toBe(400)
    }

    // Images may be kept by shared caches; pages and the listing by none.
    const key = await page.locator('#grid .tile').first().getAttribute('data-key')
    const v = (await (await request.get('/api/tiles?limit=1')).json()).v
    for (const path of [`/img/${key}.jpg`, `/img/${key}-240.webp?v=${v}`]) {
      expect((await request.get(path)).headers()['cache-control'], path).toBe('public, max-age=31536000, immutable')
    }
    for (const path of ['/', '/api/tiles?limit=1']) {
      expect((await request.get(path)).headers()['cache-control'], path).toBe('no-store')
    }

    const { found, responses } = await done()
    expect(responses).toBeGreaterThan(20)
    expect(found, 'chat ids in what a public visitor received').toEqual([])
  })

  test('without JavaScript: every server-rendered page, and none holds a chat id', async ({ browser }) => {
    const context = await browser.newContext({ baseURL: PUBLIC_URL(), javaScriptEnabled: false })
    const page = await context.newPage()
    const done = chatIdWatch(page)
    await page.goto('/')
    let pages = 1, tiles = await page.locator('noscript .tile').count()
    for (; pages < 50; pages++) {
      const more = page.getByRole('link', { name: 'Load more' })
      if (await more.count() === 0) break
      const href = await more.getAttribute('href')
      expect(href).toMatch(/^\/\?cursor=[\w-]+$/)
      await more.click()
      await page.waitForURL(u => u.pathname + u.search === href)
      tiles += await page.locator('noscript .tile').count()
    }
    expect(pages).toBeGreaterThan(2)
    expect(tiles).toBeGreaterThan(150)
    await expect(page.getByRole('link', { name: 'Admin' })).toHaveAttribute('href', '/login')
    const { found } = await done()
    expect(found, 'chat ids in the no-script pages').toEqual([])
    await context.close()
  })

  test('the admin signs in from the link at the foot of the page, and out again', async ({ page, context, request, watched }) => {
    await page.goto('/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    await expect(footer(page).getByRole('button', { name: 'Sign out' })).toHaveCount(0)
    const link = footer(page).getByRole('link', { name: 'Admin' })
    await expect(link).toHaveCount(1)
    await page.goto(await link.getAttribute('href'))
    await expect(page.getByLabel('Admin token')).toBeFocused()
    await expect(page.getByRole('link', { name: 'Back to the gallery' })).toHaveAttribute('href', '/')

    // The other stack's view token is nothing here.
    await page.getByLabel('Admin token').fill(TOKEN())
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('alert')).toHaveText('That token is not right. Try again.')
    expect(await context.cookies()).toEqual([])
    watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/\b401\b/.test(p)))

    await page.getByLabel('Admin token').fill(ADMIN_TOKEN())
    await page.getByLabel('Admin token').press('Enter')
    await page.waitForURL(u => u.pathname === '/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    expect(await isAdminPage(page)).toBe(true)
    await expect(footer(page).getByRole('link', { name: 'Admin' })).toHaveCount(0)
    const c = (await context.cookies())[0]
    expect([c.httpOnly, c.secure, c.sameSite]).toEqual([true, true, 'Strict'])
    expect(c.value).not.toContain(ADMIN_TOKEN())

    // The admin, and only the admin, is sent the record id.
    expect((await (await request.get('/api/tiles?limit=1', { headers: basicAdmin() })).json()).tiles[0].id).toMatch(/^-?\d+:/)
    expect((await (await request.get('/api/tiles?limit=1')).json()).tiles[0].id).toBeUndefined()
    expect(await (await request.get('/')).text()).not.toContain('data-admin')

    // Signing out: back to an ordinary visitor, still viewing.
    await footer(page).getByRole('button', { name: 'Sign out' }).click()
    await page.waitForURL(u => u.pathname === '/')
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    expect(await isAdminPage(page)).toBe(false)
    expect(await context.cookies()).toEqual([])
    await expect(footer(page).getByRole('link', { name: 'Admin' })).toHaveCount(1)
  })
})
