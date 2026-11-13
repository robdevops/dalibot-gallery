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

// The sample pictures in the order they are shown: the four birds in the top
// row on a desktop, then the fifth bird, the fish and the two flies.
const ORDER = [4, 6, 7, 8, 5, 3, 2, 1].map(n => `telegram-0${n}.jpg`)
const sha = buf => crypto.createHash('sha256').update(buf).digest('hex')
const samples = path.join(__dirname, '..', 'testdata', 'telegram')
const fileOf = new Map(fs.readdirSync(samples).map(f => [sha(fs.readFileSync(path.join(samples, f))), f]))

// The listing reduced to one tile per sample picture, in ORDER. A tile's
// picture is recognised by its original, which the gallery serves unchanged.
async function distinctTiles(request) {
  const listing = await (await request.get('/api/tiles?limit=120', { headers: basic() })).json()
  const byFile = new Map()
  for (const t of listing.tiles) {
    if (byFile.size === fileOf.size) break
    const file = fileOf.get(sha(await (await request.get(`/img/${t.key}.jpg`, { headers: basic() })).body()))
    if (file && !byFile.has(file)) byFile.set(file, { ...t, styles: [t.styles.find(x => x.key === t.key)] })
  }
  return { ...listing, tiles: ORDER.map(f => byFile.get(f)).filter(Boolean), next: null }
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
