// The "N new" pill. This file runs last: it adds records to the table.
//
// The page polls /api/tiles/new once a minute while it is visible, and again
// whenever it becomes visible. The test does not wait a minute: it dispatches
// the same `visibilitychange` event the browser sends when a tab comes back to
// the front, which makes the page poll at once. Nothing in the page or the
// server is changed for the test; the dev stack only runs the gallery with
// GALLERY_REFRESH=5s (the shortest it accepts) so new records are read quickly.
const { test, expect, apiTiles, domKeys, gridGeometry, control, viewportImagesLoaded } = require('./helpers')

const pollNow = page => page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))

test('"N new" appears for new images and inserts them only on click', async ({ page }) => {
  test.setTimeout(120_000)
  const before = await apiTiles(page.request)
  const polls = []
  page.on('request', r => { if (new URL(r.url()).pathname === '/api/tiles/new') polls.push(r.url()) })
  await page.goto('/')
  await page.waitForSelector('#grid .tile')
  const pill = page.locator('#pill')

  // Nothing new yet: a poll asks from the newest tile the page has, and the pill stays hidden.
  await expect(pill).toBeHidden()
  await pollNow(page)
  await expect.poll(() => polls.length).toBe(1)
  expect(polls[0]).toContain('since=' + encodeURIComponent(before.newest))
  await page.waitForTimeout(300)
  await expect(pill).toBeHidden()

  // Scroll down a little, as a visitor would have, and load a second page.
  await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
  await expect.poll(() => page.locator('#grid .tile').count()).toBeGreaterThan(60)
  await page.evaluate(() => scrollTo(0, 1200))
  const keysBefore = await domKeys(page)

  // The bot draws three new images.
  expect((await control('/add?n=3', 'POST')).added).toBe(3)
  let after
  await expect.poll(async () => (after = await apiTiles(page.request)).tiles.length, { timeout: 30_000, intervals: [500] })
    .toBe(before.tiles.length + 3)
  const fresh = after.tiles.slice(0, 3)
  expect(after.tiles.slice(3).map(t => t.key)).toEqual(before.tiles.map(t => t.key))
  for (const t of fresh) expect(keysBefore).not.toContain(t.key)

  await pollNow(page)
  await expect(pill).toBeVisible()
  await expect(pill).toHaveText('3 new')
  await expect(page.locator('#live')).toHaveText('3 new images')
  // Nothing was inserted and the page did not move.
  expect(await domKeys(page)).toEqual(keysBefore)
  expect(await page.evaluate(() => scrollY)).toBe(1200)
  // The pill is in view, centred at the top, above the tiles.
  const box = await pill.boundingBox()
  expect(box.y).toBeGreaterThanOrEqual(0)
  expect(box.y).toBeLessThan(60)
  expect(Math.abs(box.x + box.width / 2 - 720)).toBeLessThan(2)

  // Two more arrive before the click: the count follows.
  expect((await control('/add?n=2', 'POST')).added).toBe(2)
  await expect.poll(async () => (after = await apiTiles(page.request)).tiles.length, { timeout: 30_000, intervals: [500] })
    .toBe(before.tiles.length + 5)
  await pollNow(page)
  await expect(pill).toHaveText('5 new')
  expect(await domKeys(page)).toEqual(keysBefore)

  // Click: the new tiles are prepended, in order, and the page goes to the top.
  await pill.click()
  await expect(pill).toBeHidden()
  await expect.poll(() => page.evaluate(() => scrollY)).toBe(0)
  const keys = await domKeys(page)
  expect(keys.length).toBe(keysBefore.length + 5)
  expect(new Set(keys).size, 'duplicate keys').toBe(keys.length)
  expect(keys).toEqual(after.tiles.slice(0, keys.length).map(t => t.key))
  await expect(page.locator('#live')).toHaveText('Loaded 5 new images')
  // Still one flush grid, with every batch full again, and the new images load.
  await viewportImagesLoaded(page)
  // (Only the first batch is measured here: the batches below the viewport
  // have not been rendered since they were rebuilt, and until then they are
  // held at their placeholder height by content-visibility. The whole grid is
  // measured further down, once it has been scrolled through.)
  const g = await gridGeometry(page, '#grid .page:first-child .tile')
  expect(g.count).toBe(60)
  expect(g.issues).toEqual([])
  expect(await page.$$eval('#grid .sub', subs => subs.slice(0, -1).every(s => s.childElementCount === 60))).toBe(true)
  expect(await page.locator('#grid .tile img').first().evaluate(i => i.naturalWidth)).toBeGreaterThanOrEqual(240)
  // Focus is on the first new tile.
  expect(await page.evaluate(() => document.activeElement.closest('.tile')?.dataset.key)).toBe(keys[0])

  // The next poll asks from the new newest tile, and finds nothing.
  const n = polls.length
  await pollNow(page)
  await expect.poll(() => polls.length).toBeGreaterThan(n)
  expect(polls.at(-1)).toContain('since=' + encodeURIComponent(after.newest))
  await expect(pill).toBeHidden()

  // Scrolling on to the end still gives every tile once, in the API's order.
  for (let i = 0; i < 200 && !(await page.locator('#end').isHidden()); i++) {
    await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
    await page.waitForTimeout(100)
  }
  const all = await domKeys(page)
  expect(new Set(all).size).toBe(all.length)
  expect(all).toEqual(after.tiles.map(t => t.key))
  const whole = await gridGeometry(page)
  expect(whole.count).toBe(after.tiles.length)
  expect(whole.issues).toEqual([])

  // A new image opens in the lightbox like any other.
  await page.evaluate(() => scrollTo(0, 0))
  await page.locator(`.tile[data-key="${keys[0]}"] a`).click()
  await expect(page.locator('dialog#lb')).toBeVisible()
  await expect.poll(() => page.locator('#full').evaluate(i => i.complete && i.naturalWidth)).toBe(1024)
})

test('the page does not poll while it is hidden', async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('#grid .tile')
  const polls = []
  page.on('request', r => { if (new URL(r.url()).pathname === '/api/tiles/new') polls.push(r.url()) })
  // Make the document report "hidden", as a background tab does.
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await page.waitForTimeout(500)
  expect(polls).toEqual([])
  await page.evaluate(() => {
    delete document.visibilityState
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await expect.poll(() => polls.length).toBe(1)
})

test('Telegram saw nothing but getFile and file downloads', async () => {
  const state = await control('/state')
  expect(state.forbidden).toEqual([])
  expect(state.getFile).toBeGreaterThan(0)
  expect(state.downloads).toBeGreaterThan(0)
})
