// Hiding images as an admin: the Hide control in the viewer with its
// confirmation, Undo, and the page of hidden images, on the token-protected
// stack and on the public one. And the other side: someone who is not an
// admin is sent no admin script, has no controls and cannot use the routes.
const { test, expect, ADMIN_TOKEN, PUBLIC_URL, CHAT_IDS, basic, domKeys, gridGeometry } = require('./helpers')

const basicAdmin = () => ({ Authorization: 'Basic ' + Buffer.from('e2e:' + ADMIN_TOKEN()).toString('base64') })
const ADMIN_FILES = ['/admin.js', '/admin.css']
const lb = page => page.locator('#lb')
const adm = page => page.locator('#adm')
const hideButton = page => adm(page).getByRole('button', { name: 'Hide', exact: true })
const confirmButton = page => adm(page).getByRole('button', { name: 'Hide image' })
const cancelButton = page => adm(page).getByRole('button', { name: 'Cancel' })
const undoButton = page => page.getByRole('button', { name: 'Undo' })
const openKey = page => page.evaluate(() => /^#\/p\/([\w-]{22})$/.exec(location.hash)?.[1] || '')
const stripCount = page => page.locator('#strip > li').count()

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

// Hide the image the viewer shows, with the mouse: Hide, wait out the guard, Hide image.
async function hideOpenImage(page) {
  await hideButton(page).click()
  await expect(confirmButton(page)).toBeVisible()
  await page.waitForTimeout(600)
  await confirmButton(page).click()
  await expect(page.locator('.did').getByText('Image hidden.')).toBeVisible()
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
  await expect(adm(page)).toHaveCount(0)
  await expect(page.locator('#undo')).toHaveCount(0)
  for (const name of ['Hide', 'Hide image', 'Undo', 'Restore']) {
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

  test('the admin hides an image in the viewer, undoes it, and restores from the page of hidden images', async ({ page, request }) => {
    const paths = requested(page)
    await signInAsAdmin(page, 'Access token')
    // Only now, as an admin, is the admin's script loaded.
    for (const f of ADMIN_FILES) expect(paths, f).toContain(f)
    for (const f of ADMIN_FILES) {
      const r = await request.get(f, { headers: basicAdmin() })
      expect(r.status()).toBe(200)
      expect(r.headers()['cache-control']).toBe('private, no-cache')
    }

    // Load a second batch, so that hiding has to move tiles between batches.
    await expect.poll(async () => {
      await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
      return page.locator('#grid .tile').count()
    }, { timeout: 30_000 }).toBeGreaterThan(100)
    await page.evaluate(() => scrollTo(0, 0))
    const before = await domKeys(page)
    expect(await listedKeys(request, basic())).toEqual(expect.arrayContaining(before))

    // Open the third tile.
    const [target, following] = [before[2], before[3]]
    await page.locator(`#grid .tile[data-key="${target}"] a`).click()
    await expect(lb(page)).toHaveAttribute('open', '')
    await expect.poll(() => page.locator('#full').evaluate(i => i.naturalWidth)).toBeGreaterThan(0)
    expect(await stripCount(page)).toBe(before.length)
    await expect(hideButton(page)).toBeVisible()
    await expect(confirmButton(page)).toBeHidden()

    // Nothing but the two buttons, one after the other, hides: not a click on the image, not a
    // drag or a swipe, not a double click on Hide, not Enter held down, not the arrow keys.
    await page.locator('#full').click() // hides the buttons and the caption, the Hide button with them
    await expect(lb(page)).toHaveClass('bare')
    await expect(hideButton(page)).toBeHidden()
    await page.locator('#full').click() // and shows them again
    await expect(lb(page)).not.toHaveClass('bare')
    await expect(hideButton(page)).toBeVisible()
    // A press on the image dragged onto the Hide button and released there is not a click on it.
    const img = await page.locator('#full').boundingBox()
    const btn = await hideButton(page).boundingBox()
    await page.mouse.move(img.x + img.width / 2, img.y + img.height / 2)
    await page.mouse.down()
    await page.mouse.move(btn.x + btn.width / 2, btn.y + btn.height / 2, { steps: 8 })
    await page.mouse.up()
    await page.waitForTimeout(400) // past app.js's wait for a second click
    await expect(lb(page)).toHaveAttribute('open', '')
    await expect(confirmButton(page)).toBeHidden()
    await expect(hideButton(page)).toBeVisible()
    expect(await openKey(page)).toBe(target)
    // A double click on Hide opens the question, and that is all.
    await hideButton(page).dblclick()
    await page.waitForTimeout(300)
    expect(await openKey(page)).toBe(target)
    expect(await hiddenKeys(request)).toEqual([])
    await expect(page.getByText('Hide this image from the gallery?')).toBeVisible()
    await expect(hideButton(page)).toBeHidden()
    // A single click opens it with the focus on Cancel.
    await cancelButton(page).click()
    await expect(hideButton(page)).toBeFocused()
    await hideButton(page).click()
    await expect(cancelButton(page)).toBeFocused()
    await expect(confirmButton(page)).toBeVisible()
    // Moving to another image withdraws it: with the arrow keys, and with a sideways swipe on a touchpad.
    await page.keyboard.press('ArrowRight')
    await expect.poll(() => openKey(page)).toBe(following)
    await expect(confirmButton(page)).toBeHidden()
    await expect(hideButton(page)).toBeVisible()
    await page.keyboard.press('ArrowLeft')
    await expect.poll(() => openKey(page)).toBe(target)
    await hideButton(page).click()
    await expect(confirmButton(page)).toBeVisible()
    await page.mouse.move(img.x + img.width / 2, img.y + img.height / 2)
    for (const dx of [4, 12, 30, 45, 40, 30, 22, 16, 11, 8, 5, 3, 2, 1, 1]) {
      await page.mouse.wheel(dx, 0)
      await page.waitForTimeout(16)
    }
    await expect.poll(() => openKey(page)).toBe(following)
    await expect(confirmButton(page)).toBeHidden()
    await expect(hideButton(page)).toBeVisible()
    await page.waitForTimeout(250) // the swipe's momentum has ended
    await page.keyboard.press('ArrowLeft')
    await expect.poll(() => openKey(page)).toBe(target)
    await expect.poll(() => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).m41)).toBe(0)
    expect(await hiddenKeys(request)).toEqual([])
    // Enter held down on Hide goes to Cancel and back, never to "Hide image".
    await hideButton(page).focus()
    for (let i = 0; i < 6; i++) await page.keyboard.press('Enter')
    await page.waitForTimeout(700)
    for (let i = 0; i < 5; i++) await page.keyboard.press('Enter')
    expect(await hiddenKeys(request)).toEqual([])
    // "Hide image" straight after Hide (a second click that lands on it) does nothing either.
    if (await confirmButton(page).isVisible()) await cancelButton(page).click()
    await hideButton(page).click()
    await confirmButton(page).click()
    await page.waitForTimeout(300)
    expect(await hiddenKeys(request)).toEqual([])
    expect(await openKey(page)).toBe(target)
    await cancelButton(page).click()
    await expect(hideButton(page)).toBeFocused()

    // With the keyboard: Enter on Hide, Tab to "Hide image", Enter.
    await page.keyboard.press('Enter')
    await expect(cancelButton(page)).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(confirmButton(page)).toBeFocused()
    await page.waitForTimeout(600)
    await page.keyboard.press('Enter')

    // The tile is gone, the viewer has moved to the next image, and Undo is offered.
    await expect(page.locator('.did').getByText('Image hidden.')).toBeVisible()
    await expect.poll(() => openKey(page)).toBe(following)
    await expect(page.locator('#full')).toHaveAttribute('data-key', following)
    await expect(lb(page)).toHaveAttribute('open', '')
    const after = before.filter(k => k !== target)
    expect(await domKeys(page)).toEqual(after)
    expect(await stripCount(page)).toBe(after.length)
    await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
    await expect(undoButton(page)).toBeVisible()
    await expect(hideButton(page)).toBeFocused()
    await expect(page.locator('#live')).toHaveText('Image hidden.')
    // For everyone, at once: not in the listing, and its images are unknown.
    expect(await hiddenKeys(request)).toEqual([target])
    expect(await listedKeys(request, basic())).not.toContain(target)
    const v = (await (await request.get('/api/tiles?limit=1', { headers: basic() })).json()).v
    for (const path of [`/img/${target}.jpg`, `/img/${target}-240.webp?v=${v}`]) {
      expect((await request.get(path, { headers: basic() })).status(), path).toBe(404)
      const r = await request.get(path, { headers: basicAdmin() })
      expect(r.status(), path + ' for the admin').toBe(200)
      expect(r.headers()['cache-control']).toBe('no-store')
    }

    // Undo, with the keyboard (it is the next stop after Hide): back in its place, and shown again.
    await page.keyboard.press('Tab')
    await expect(undoButton(page)).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page.locator('.did').getByText('Image restored.')).toBeVisible()
    await expect.poll(() => openKey(page)).toBe(target)
    expect(await domKeys(page)).toEqual(before)
    expect(await stripCount(page)).toBe(before.length)
    await expect(page.locator('#strip > li').nth(2).locator('button')).toHaveAttribute('aria-current', 'true')
    await expect(undoButton(page)).toHaveCount(0)
    expect(await hiddenKeys(request)).toEqual([])
    expect(await listedKeys(request, basic())).toContain(target)

    // Hide it again, and the one after it, with the mouse. Close the viewer: the grid has no hole.
    await hideOpenImage(page)
    await expect.poll(() => openKey(page)).toBe(following)
    await hideOpenImage(page)
    await expect.poll(() => openKey(page)).toBe(before[4])
    await page.keyboard.press('Escape')
    await expect(lb(page)).not.toHaveAttribute('open', '')
    const left = before.filter(k => k !== target && k !== following)
    expect(await domKeys(page)).toEqual(left)
    const geo = await gridGeometry(page)
    expect(geo.issues, 'the grid after hiding two tiles').toEqual([])
    expect(await page.$$eval('#grid .sub', subs => subs.slice(0, -1).map(s => s.childElementCount)), 'every batch but the last is full')
      .toEqual(expect.arrayContaining([60]))
    expect(await page.$$eval('#grid .sub', subs => subs.slice(0, -1).every(s => s.childElementCount === 60))).toBe(true)
    // With the viewer closed, Undo is at the foot of the page, and restores the last one hidden.
    await expect(page.locator('#undo').getByRole('button', { name: 'Undo' })).toBeVisible()
    await page.locator('#undo').getByRole('button', { name: 'Undo' }).click()
    await expect(page.locator('.did').getByText('Image restored.')).toBeVisible()
    expect(await domKeys(page)).toEqual(before.filter(k => k !== target))
    await expect(lb(page)).not.toHaveAttribute('open', '')
    expect((await gridGeometry(page)).issues).toEqual([])
    expect(await hiddenKeys(request)).toEqual([target])

    // The page of hidden images, from the link at the foot of the gallery.
    const link = page.locator('footer.foot').getByRole('link', { name: 'Hidden images' })
    await expect(link).toHaveAttribute('href', '/admin/hidden')
    await page.goto('/admin/hidden')
    await expect(page.getByRole('heading', { name: 'Hidden images' })).toBeVisible()
    const item = page.locator('#hidden > li')
    await expect(item).toHaveCount(1)
    await expect.poll(() => item.locator('img').evaluate(i => i.complete && i.naturalWidth)).toBeGreaterThan(0)
    await expect(item.locator('a')).toHaveAttribute('href', `/img/${target}.jpg`)
    await expect(page.locator('#none')).toBeHidden()
    const restore = item.getByRole('button', { name: 'Restore' })
    await restore.focus()
    await page.keyboard.press('Enter')
    await expect(item).toHaveCount(0)
    await expect(page.locator('#none')).toBeVisible()
    await expect(page.locator('#live')).toHaveText('Image restored')
    await expect(page.getByRole('link', { name: 'Back to the gallery' })).toBeFocused()
    expect(await hiddenKeys(request)).toEqual([])

    // Back in the gallery, in its place.
    await page.getByRole('link', { name: 'Back to the gallery' }).click()
    await expect(page.locator('#grid .tile').first()).toBeVisible()
    expect((await domKeys(page)).slice(0, 5)).toEqual(before.slice(0, 5))
    expect(await listedKeys(request, basic())).toContain(target)
  })

  test('hiding the last loaded image moves to the one before; a failed request changes nothing', async ({ page, request, context, watched }) => {
    await signInAsAdmin(page, 'Access token')
    const keys = await domKeys(page)
    const last = keys[keys.length - 1], previous = keys[keys.length - 2]
    // Keep the page from loading more, so that the last tile stays the last.
    await page.route('**/api/tiles?*', route => route.abort())
    await page.locator(`#grid .tile[data-key="${last}"] a`).evaluate(a => a.click())
    await expect(lb(page)).toHaveAttribute('open', '')
    await expect.poll(() => openKey(page)).toBe(last)

    // The server refuses (the session is gone): the image stays, and the admin is told.
    const cookies = await context.cookies()
    await context.clearCookies()
    await hideButton(page).click()
    await page.waitForTimeout(600)
    await confirmButton(page).click()
    await expect(adm(page).getByText('Could not hide the image')).toBeVisible()
    await expect(page.locator('#live')).toHaveText('Could not hide the image')
    expect(await openKey(page)).toBe(last)
    expect(await domKeys(page)).toEqual(keys)
    expect(await hiddenKeys(request)).toEqual([])
    await context.addCookies(cookies)

    await hideOpenImage(page)
    await expect.poll(() => openKey(page)).toBe(previous)
    expect(await domKeys(page)).toEqual(keys.slice(0, -1))
    await undoButton(page).click()
    await expect.poll(() => openKey(page)).toBe(last)
    expect(await domKeys(page)).toEqual(keys)
    expect(await hiddenKeys(request)).toEqual([])
    watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/\b40[14]\b|api\/tiles|ERR_FAILED/.test(p)))
  })
})

test.describe('a public gallery', () => {
  test.skip(() => !PUBLIC_URL(), 'no public stack in this run (set E2E_PUBLIC_URL)')
  test.use({ baseURL: PUBLIC_URL(), storageState: { cookies: [], origins: [] } })
  test.afterEach(async ({ request }) => { await restoreAll(request) })

  test('a visitor is sent no admin script and has no controls', async ({ page, request, watched }) => {
    await expectNoAdminParts(page, request, {}, watched)
  })

  test('what the admin hides is gone for every visitor at once, and back after Undo', async ({ page, request, browser }) => {
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
    await page.locator('#grid .tile a').first().click()
    await expect(lb(page)).toHaveAttribute('open', '')
    expect(await openKey(page)).toBe(first)
    await hideOpenImage(page)

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
    await expect(theirs.locator('#adm')).toHaveCount(0)

    // The admin's list and page carry no chat id (the listing an admin gets does; these do not).
    const list = await (await request.get('/api/admin/hidden', { headers: basicAdmin() })).text()
    expect(JSON.parse(list).hidden.map(h => h.key)).toEqual([first])
    const html = await (await request.get('/admin/hidden', { headers: basicAdmin() })).text()
    expect(html).toContain(`data-restore="${first}"`)
    for (const chat of CHAT_IDS) {
      expect(list).not.toContain(chat)
      expect(html).not.toContain(chat)
    }

    await undoButton(page).click()
    await expect(page.locator('.did').getByText('Image restored.')).toBeVisible()
    expect(await listedKeys(request, {})).toContain(first)
    for (const u of urls) expect((await request.get(u)).status(), u).toBe(200)
    await theirs.reload()
    await expect(theirs.locator('#grid .tile').first()).toBeVisible()
    expect((await domKeys(theirs))[0]).toBe(first)
    await visitor.close()
  })
})
