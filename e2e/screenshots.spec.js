// Screenshots of the grid and the lightbox, at 2x on a desktop and on an
// iPhone. They go to artifacts/screenshots/; with E2E_SCREENSHOTS=1 they
// replace the committed ones in docs/screenshots/.
//
// The dev stack has many more tiles than sample images, so the same picture
// comes up again and again. For the screenshots the listing is cut down to one
// tile per distinct picture, and the desktop window is sized so that those
// tiles fill the grid exactly (4 columns by 2 rows for 8 pictures).
const crypto = require('crypto')
const fs = require('fs')
const path = require('path')
const { test, expect, devices, viewportImagesLoaded, basic } = require('./helpers')

const dir = process.env.E2E_SCREENSHOTS
  ? path.join(__dirname, '..', 'docs', 'screenshots')
  : path.join(__dirname, 'artifacts', 'screenshots')

const phone = devices['iPhone 15']
// [name, context options, grid viewport, viewer viewport, device scale factor].
const PROFILES = [
  ['desktop', { deviceScaleFactor: 2 }, { width: 1100, height: 550 }, { width: 1100, height: 800 }, 2],
  // The device profile brings its own viewport, scale factor (3) and user agent.
  ['iphone', phone, phone.viewport, phone.viewport, 3],
]

// The listing reduced to the first tile of each distinct picture, told apart by
// the bytes of the 240 px thumbnail.
async function distinctTiles(request) {
  const listing = await (await request.get('/api/tiles?limit=120', { headers: basic() })).json()
  const seen = new Set()
  const tiles = []
  for (const t of listing.tiles) {
    const body = await (await request.get(`/img/${t.key}-240.webp?v=${listing.v}`, { headers: basic() })).body()
    const hash = crypto.createHash('sha256').update(body).digest('hex')
    if (seen.has(hash)) continue
    seen.add(hash)
    tiles.push({ ...t, styles: [t.styles.find(x => x.key === t.key)] })
  }
  return { ...listing, tiles, next: null }
}

for (const [name, profile, gridView, viewerView, scale] of PROFILES) {
  test(`screenshots: ${name}`, async ({ browser, baseURL }, testInfo) => {
    fs.mkdirSync(dir, { recursive: true })
    const context = await browser.newContext({ ...profile, viewport: gridView, baseURL, storageState: testInfo.project.use.storageState })
    const page = await context.newPage()
    const listing = await distinctTiles(context.request)
    expect(listing.tiles.length, 'one tile per sample image').toBe(8)
    await page.route(/\/api\/tiles\?/, route => route.fulfill({ json: listing }))
    const errors = []
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()) })
    try {
      await page.goto('/')
      await page.waitForSelector('#grid .tile')
      await viewportImagesLoaded(page)
      // Saved as JPEG (a PNG of photographs at this size is 4 to 6 MB); the
      // pixel size is checked on a PNG of the same screen.
      const size = buf => [buf.readUInt32BE(16), buf.readUInt32BE(20)] // PNG IHDR
      const save = async (file, view) => {
        expect(size(await page.screenshot())).toEqual([view.width * scale, view.height * scale])
        await page.screenshot({ path: path.join(dir, file), type: 'jpeg', quality: 90 })
      }
      await expect(page.locator('#grid .tile')).toHaveCount(8)
      await save(`${name}-grid.jpg`, gridView)

      // The lightbox, with its strip of the other images.
      await page.setViewportSize(viewerView)
      const key = (listing.tiles.find(t => t.caption) || listing.tiles[0]).key
      await page.locator(`.tile[data-key="${key}"] a`).evaluate(a => a.click())
      await expect(page.locator('dialog#lb')).toBeVisible()
      await expect.poll(() => page.locator('#full').evaluate(i => i.complete && i.naturalWidth)).toBe(1024)
      // The strip's thumbnails load lazily: wait for the ones in view.
      await expect.poll(() => page.locator('#strip').evaluate(st => {
        const box = st.getBoundingClientRect()
        const seen = [...st.querySelectorAll('img')].filter(i => {
          const r = i.getBoundingClientRect()
          return r.right > box.left && r.left < box.right
        })
        return seen.length > 0 && seen.every(i => i.complete && i.naturalWidth > 0)
      })).toBe(true)
      await save(`${name}-lightbox.jpg`, viewerView)
      expect(errors).toEqual([])
    } finally {
      await context.close()
    }
  })
}
