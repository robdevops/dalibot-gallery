// The grid: flush square tiles at every width, newest first, infinite scroll.
const { test, expect, apiTiles, domKeys, gridGeometry, nonWhitePixels, viewportImagesLoaded } = require('./helpers')

// [width, height, deviceScaleFactor, expected columns]. 1001 and 1366 give
// fractional tile widths (250.25 and 273.2 px), where seams usually show.
const VIEWPORTS = [
  [375, 667, 1, 2], [375, 667, 2, 2], [390, 844, 3, 2], [479, 800, 1, 2], [480, 800, 1, 3],
  [800, 600, 1, 4], [1001, 700, 1, 4], [1366, 768, 1, 5], [1440, 900, 1, 5], [1440, 900, 2, 5],
  [1920, 1080, 1, 6],
]

for (const [width, height, dpr, cols] of VIEWPORTS) {
  test(`tiles are square and flush at ${width}x${height} @${dpr}x`, async ({ browser, baseURL }, testInfo) => {
    const context = await browser.newContext({
      baseURL, storageState: testInfo.project.use.storageState,
      viewport: { width, height }, deviceScaleFactor: dpr,
    })
    const page = await context.newPage()
    const errors = []
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()) })
    page.on('pageerror', e => errors.push(e.message))
    try {
      await page.goto('/')
      await page.waitForSelector('#grid .tile')
      await viewportImagesLoaded(page)

      // Layout: bounding rects of neighbours touch, every tile is square.
      const g = await gridGeometry(page)
      expect(g.cols, 'columns').toBe(cols)
      expect(g.count).toBeGreaterThanOrEqual(cols * 4)
      expect(g.width, 'no horizontal scrollbar space or overflow').toBe(width)
      expect(g.issues, 'layout gaps').toEqual([])
      expect(g.worst).toBeLessThanOrEqual(0.05)
      expect(await page.evaluate(() => getComputedStyle(document.querySelector('#grid')).gap)).toBe('0px')
      expect(await page.evaluate(() => getComputedStyle(document.querySelector('#grid .sub')).gap)).toBe('0px')
      expect(await page.evaluate(() => document.documentElement.scrollWidth), 'no horizontal overflow').toBe(width)
      // The images fill their tiles exactly.
      const fill = await page.$$eval('#grid .tile', els => Math.max(...els.slice(0, 30).map(e => {
        const t = e.getBoundingClientRect(), i = e.querySelector('img').getBoundingClientRect()
        return Math.max(Math.abs(t.left - i.left), Math.abs(t.top - i.top), Math.abs(t.right - i.right), Math.abs(t.bottom - i.bottom))
      })))
      expect(fill, 'image inset inside its tile').toBeLessThanOrEqual(0.05)

      // Paint: with white stand-in images, a full viewport of tiles must be
      // entirely white, at the top and across the seam between two batches.
      const white = await whitePNG(context)
      await page.route('**/img/**', route => route.fulfill({ status: 200, contentType: 'image/png', body: white }))
      await page.reload()
      await page.waitForSelector('#grid .tile')
      await viewportImagesLoaded(page)
      let shot = await nonWhitePixels(context, await page.screenshot())
      expect(shot.width).toBe(width * dpr)
      expect(shot.bad, 'non-white pixels in the first screen').toBe(0)

      // Load the second batch and put the boundary between the batches mid-screen.
      await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
      await expect.poll(() => page.locator('#grid .tile').count()).toBeGreaterThan(60)
      await page.evaluate(() => {
        const r = document.querySelectorAll('#grid .tile')[60].getBoundingClientRect()
        scrollTo(0, scrollY + r.top - innerHeight / 2)
      })
      await viewportImagesLoaded(page)
      const g2 = await gridGeometry(page)
      expect(g2.issues, 'layout gaps after the second batch').toEqual([])
      const seam = await page.evaluate(() => {
        const t = document.querySelectorAll('#grid .tile')
        return { above: t[59].getBoundingClientRect().bottom, below: t[60].getBoundingClientRect().top, h: innerHeight }
      })
      expect(Math.abs(seam.above - seam.below)).toBeLessThanOrEqual(0.05)
      expect(seam.below).toBeGreaterThan(0)
      expect(seam.below).toBeLessThan(seam.h)
      shot = await nonWhitePixels(context, await page.screenshot())
      expect(shot.bad, 'non-white pixels across the batch boundary').toBe(0)
      expect(errors).toEqual([])
    } finally {
      await context.close()
    }
  })
}

let png
async function whitePNG(context) {
  if (png) return png
  const p = await context.newPage()
  await p.setViewportSize({ width: 64, height: 64 })
  await p.setContent('<body style="margin:0;background:#fff">')
  png = await p.screenshot({ clip: { x: 0, y: 0, width: 16, height: 16 } })
  await p.close()
  return png
}

test('first page is newest first and matches the API', async ({ page }) => {
  const api = await apiTiles(page.request)
  await page.goto('/')
  await page.waitForSelector('#grid .tile')
  const keys = await domKeys(page)
  expect(keys.length).toBeGreaterThanOrEqual(50) // a page holds whole records, so about 60
  expect(keys).toEqual(api.tiles.slice(0, keys.length).map(t => t.key))
  // The API order really is newest first.
  for (let i = 1; i < api.tiles.length; i++) {
    expect(api.tiles[i].updated, 'updated must not increase at ' + i).toBeLessThanOrEqual(api.tiles[i - 1].updated)
  }
  // Markup of the tiles: srcset, sizes, eager first row, alt text.
  const first = await page.$$eval('#grid .tile img', els => els.slice(0, 12).map(i => ({
    src: i.getAttribute('src'), srcset: i.getAttribute('srcset'), sizes: i.getAttribute('sizes'),
    loading: i.loading, prio: i.getAttribute('fetchpriority'), alt: i.alt, w: i.getAttribute('width'), h: i.getAttribute('height'),
  })))
  first.forEach((img, i) => {
    const t = api.tiles[i]
    expect(img.src).toBe(`/img/${t.key}-240.webp?v=${api.v}`)
    expect(img.srcset).toBe(`/img/${t.key}-240.webp?v=${api.v} 240w, /img/${t.key}-480.webp?v=${api.v} 480w`)
    expect(img.alt).toBe(t.caption || t.prompt)
    expect([img.w, img.h]).toEqual(['240', '240'])
    if (i < 5) { // 5 columns at 1440 px
      expect(img.loading).toBe('eager')
      expect(img.sizes).not.toContain('auto')
      expect(img.prio).toBe(i < 4 ? 'high' : null)
    } else {
      expect(img.loading).toBe('lazy')
      expect(img.sizes.startsWith('auto, ')).toBe(true)
    }
  })
})

test('scrolling loads every page to the very end with no duplicates', async ({ page }) => {
  const api = await apiTiles(page.request)
  expect(api.tiles.length).toBeGreaterThan(180) // at least four pages of 60
  const pages = [], images = { ok: 0, placeholder: 0, bad: [] }
  page.on('request', r => { if (new URL(r.url()).pathname === '/api/tiles') pages.push(r.url()) })
  page.on('response', r => {
    if (!new URL(r.url()).pathname.startsWith('/img/')) return
    if (r.status() !== 200) images.bad.push(r.status() + ' ' + r.url())
    else if (r.headers()['x-gallery-placeholder']) images.placeholder++
    else images.ok++
  })
  await page.goto('/')
  await page.waitForSelector('#grid .tile')
  expect((await domKeys(page)).length).toBeLessThan(api.tiles.length) // not everything at once

  let last = 0
  for (let i = 0; i < 200; i++) {
    const n = await page.locator('#grid .tile').count()
    if (n === api.tiles.length && await page.locator('#end').isHidden()) break
    if (n > last && last) expect(await domKeys(page)).toEqual(api.tiles.slice(0, n).map(t => t.key))
    last = n
    await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
    await page.waitForTimeout(100)
  }
  const keys = await domKeys(page)
  expect(new Set(keys).size, 'duplicate keys').toBe(keys.length)
  expect(keys).toEqual(api.tiles.map(t => t.key))
  expect(pages.length, 'pages requested').toBeGreaterThanOrEqual(Math.ceil(api.tiles.length / 60))
  expect(new Set(pages).size, 'no page requested twice').toBe(pages.length)
  await expect(page.locator('#end')).toBeHidden()

  // At the very bottom: still one flush grid, the last tiles loaded, and no further requests.
  await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight))
  await viewportImagesLoaded(page)
  const g = await gridGeometry(page)
  expect(g.count).toBe(api.tiles.length)
  expect(g.issues).toEqual([])
  const before = pages.length
  await page.mouse.wheel(0, 5000)
  await page.waitForTimeout(500)
  expect(pages.length).toBe(before)
  // Every thumbnail that was requested on the way down was served. The seed
  // has exactly one file that Telegram no longer has; it gets the placeholder.
  expect(images.bad).toEqual([])
  expect(images.ok).toBeGreaterThan(100)
  expect(images.placeholder).toBeLessThanOrEqual(1)
})
