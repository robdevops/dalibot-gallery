// Without JavaScript: the server renders each page, with a "Load more" link.
const { test, expect, apiTiles, gridGeometry, viewportImagesLoaded } = require('./helpers')

test.use({ javaScriptEnabled: false })

const TILES = 'noscript .tile'

test('the first page is rendered by the server, flush and in order', async ({ page, watched }) => {
  const api = await apiTiles(page.request)
  await page.goto('/')
  const keys = await page.$$eval(TILES + ' a', as => as.map(a => a.getAttribute('href').match(/^\/img\/([\w-]{22})\.jpg$/)[1]))
  expect(keys.length).toBeGreaterThanOrEqual(50)
  expect(keys).toEqual(api.tiles.slice(0, keys.length).map(t => t.key))
  await expect(page.locator('#grid .tile')).toHaveCount(0) // the script never ran

  await viewportImagesLoaded(page, TILES + ' img')
  const g = await gridGeometry(page, TILES)
  expect(g.cols).toBe(5)
  expect(g.issues).toEqual([])
  const img = await page.locator(TILES + ' img').first().evaluate(i => ({
    src: i.getAttribute('src'), srcset: i.getAttribute('srcset'), alt: i.alt, natural: i.naturalWidth, loading: i.loading,
  }))
  const t = api.tiles[0]
  expect(img.src).toBe(`/img/${t.key}-240.webp?v=${api.v}`)
  expect(img.srcset).toBe(`/img/${t.key}-240.webp?v=${api.v} 240w, /img/${t.key}-480.webp?v=${api.v} 480w`)
  expect(img.alt).toBe(t.caption || t.prompt)
  expect(img.natural).toBeGreaterThanOrEqual(240)
  expect(img.loading).toBe('lazy')

  // A tile links to the original image.
  const before = watched.problems.length
  await page.locator(TILES + ' a').first().click()
  expect(new URL(page.url()).pathname).toBe(`/img/${t.key}.jpg`)
  expect(await page.locator('img').evaluate(i => [i.naturalWidth, i.naturalHeight])).toEqual([1024, 1024])
  // Chromium's own viewer for a bare image styles itself inline, which the
  // gallery's CSP (style-src 'self') blocks: the image still shows, unstyled.
  // Those console messages are the browser's, not the gallery's.
  const after = watched.problems.splice(before)
  for (const p of after) expect(p).toMatch(/Applying inline style violates|style-src.* blocked inline/)
})

test('"Load more" walks every page to the end with no duplicates or gaps', async ({ page }) => {
  const api = await apiTiles(page.request)
  const seen = []
  await page.goto('/')
  for (let n = 0; n < 50; n++) {
    const keys = await page.$$eval(TILES + ' a', as => as.map(a => a.getAttribute('href').slice(5, 27)))
    expect(keys.length).toBeGreaterThan(0)
    seen.push(...keys)
    const more = page.getByRole('link', { name: 'Load more' })
    if (await more.count() === 0) break
    const href = await more.getAttribute('href')
    expect(href).toMatch(/^\/\?cursor=[\w-]+$/)
    await more.click()
    await page.waitForURL(u => u.pathname + u.search === href)
  }
  expect(new Set(seen).size, 'duplicates').toBe(seen.length)
  expect(seen).toEqual(api.tiles.map(t => t.key))
  await expect(page.getByRole('link', { name: 'Load more' })).toHaveCount(0)
  // The last page's images load too.
  await viewportImagesLoaded(page, TILES + ' img')
})

test('a bad cursor is a plain 400, not a broken page', async ({ page, watched }) => {
  const r = await page.goto('/?cursor=%3Cscript%3Ealert(1)%3C/script%3E')
  expect(r.status()).toBe(400)
  expect(await r.text()).not.toContain('<script>')
  watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/\b400\b/.test(p)))
})
