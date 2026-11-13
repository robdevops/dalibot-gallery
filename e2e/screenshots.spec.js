// Screenshots of the grid and the lightbox, at 2x on a 1440 px desktop and on
// an iPhone. They go to artifacts/screenshots/; with E2E_SCREENSHOTS=1 they
// replace the committed ones in docs/screenshots/.
const fs = require('fs')
const path = require('path')
const { test, expect, devices, viewportImagesLoaded } = require('./helpers')

const dir = process.env.E2E_SCREENSHOTS
  ? path.join(__dirname, '..', 'docs', 'screenshots')
  : path.join(__dirname, 'artifacts', 'screenshots')

const PROFILES = [
  ['desktop-1440', { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 }, [2880, 1800]],
  // The device profile brings its own viewport, scale factor (3) and user agent.
  ['iphone', devices['iPhone 15'], [devices['iPhone 15'].viewport.width * 3, devices['iPhone 15'].viewport.height * 3]],
]

for (const [name, profile, [w, h]] of PROFILES) {
  test(`screenshots: ${name}`, async ({ browser, baseURL }, testInfo) => {
    fs.mkdirSync(dir, { recursive: true })
    const context = await browser.newContext({ ...profile, baseURL, storageState: testInfo.project.use.storageState })
    const page = await context.newPage()
    const errors = []
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()) })
    try {
      await page.goto('/')
      await page.waitForSelector('#grid .tile')
      await viewportImagesLoaded(page)
      // Saved as JPEG (a PNG of photographs at this size is 4 to 6 MB); the
      // pixel size is checked on a PNG of the same screen.
      const size = buf => [buf.readUInt32BE(16), buf.readUInt32BE(20)] // PNG IHDR
      const save = async file => {
        expect(size(await page.screenshot())).toEqual([w, h])
        await page.screenshot({ path: path.join(dir, file), type: 'jpeg', quality: 90 })
      }
      await save(`${name}-grid.jpg`)

      // The lightbox, with its strip of neighbouring images.
      const key = await page.evaluate(async () => {
        const j = await (await fetch('/api/tiles?limit=60')).json()
        return j.tiles.find(t => t.styles.length >= 3 && t.caption)?.key || j.tiles.find(t => t.styles.length >= 2).key
      })
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
      await save(`${name}-lightbox.jpg`)
      expect(errors).toEqual([])
    } finally {
      await context.close()
    }
  })
}
