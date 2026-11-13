// The lightbox: open, arrow keys, strip of neighbours, Esc, backdrop, focus, deep links.
const { test, expect, apiTiles, domKeys } = require('./helpers')

const dialog = page => page.locator('dialog#lb')
const fullKey = page => page.locator('#full').evaluate(i => i.dataset.key)
const focusKey = page => page.evaluate(() => {
  const a = document.activeElement
  return a && a.tagName === 'A' ? a.closest('.tile')?.dataset.key ?? null : null
})
async function loaded(page, key) {
  await expect(page.locator('#full')).toHaveAttribute('src', `/img/${key}.jpg`)
  await expect.poll(() => page.locator('#full').evaluate(i => i.complete && i.naturalWidth)).toBe(1024)
}

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('#grid .tile')
})

test('opens on the clicked tile with the original image, caption and command', async ({ page }) => {
  const api = await apiTiles(page.request)
  const t = api.tiles[7]
  await expect(dialog(page)).toBeHidden()
  await page.locator(`.tile[data-key="${t.key}"] a`).click()
  await expect(dialog(page)).toBeVisible()
  expect(await dialog(page).evaluate(d => d.matches(':modal'))).toBe(true)
  await expect(page).toHaveURL(new RegExp(`#/p/${t.key}$`))
  await loaded(page, t.key)
  expect(await page.locator('#full').evaluate(i => i.naturalHeight)).toBe(1024)
  await expect(page.locator('#full')).toHaveAttribute('alt', t.caption || t.prompt)
  await expect(page.locator('#cap')).toHaveText(t.caption || t.prompt)
  await expect(page.locator('#cmd')).toHaveText(`/${t.command} · ${t.label}`)
  // The close button has the focus, and the page behind does not scroll.
  await expect(page.locator('#x')).toBeFocused()
  expect(await page.evaluate(() => getComputedStyle(document.body).overflow)).toBe('hidden')
  // The image fits the viewport and is square.
  const box = await page.locator('#full').boundingBox()
  expect(Math.abs(box.width - box.height)).toBeLessThan(0.5)
  expect(box.y).toBeGreaterThanOrEqual(0)
  expect(box.y + box.height).toBeLessThanOrEqual(900)
})

test('arrow keys and buttons move between tiles; Esc closes and restores focus', async ({ page }) => {
  const keys = await domKeys(page)
  const opener = page.locator(`.tile[data-key="${keys[3]}"] a`)
  await opener.click()
  await loaded(page, keys[3])

  await page.keyboard.press('ArrowRight')
  await loaded(page, keys[4])
  await expect(page).toHaveURL(new RegExp(`#/p/${keys[4]}$`))
  await page.keyboard.press('ArrowRight')
  await loaded(page, keys[5])
  await page.keyboard.press('ArrowLeft')
  await page.keyboard.press('ArrowLeft')
  await loaded(page, keys[3])
  await page.keyboard.press('ArrowLeft')
  await page.keyboard.press('ArrowLeft')
  await page.keyboard.press('ArrowLeft')
  await loaded(page, keys[0])
  await page.keyboard.press('ArrowLeft') // nothing before the first tile
  expect(await fullKey(page)).toBe(keys[0])
  await page.getByRole('button', { name: 'Next image' }).click()
  await loaded(page, keys[1])
  await page.getByRole('button', { name: 'Previous image' }).click()
  await loaded(page, keys[0])
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('ArrowRight')
  await loaded(page, keys[3])

  await page.keyboard.press('Escape')
  await expect(dialog(page)).toBeHidden()
  await expect(opener).toBeFocused()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  expect(await page.evaluate(() => getComputedStyle(document.body).overflow)).not.toBe('hidden')
})

test('while a large image loads, the previous one is hidden, not left on screen', async ({ page }) => {
  const api = await apiTiles(page.request)
  const [a, b] = [api.tiles[2], api.tiles[3]]
  const visibility = () => page.locator('#full').evaluate(i => getComputedStyle(i).visibility)
  // Slow the originals down so the loading state can be observed.
  await page.route('**/img/*.jpg', async route => {
    await new Promise(r => setTimeout(r, 700))
    await route.continue()
  })

  // Opening: nothing is shown until the image has loaded.
  await page.locator(`.tile[data-key="${a.key}"] a`).click()
  await expect(dialog(page)).toBeVisible()
  expect(await visibility()).toBe('hidden')
  await loaded(page, a.key)
  await expect.poll(visibility).toBe('visible')

  // Paging: the caption and address change at once and the old image is hidden straight away.
  await page.keyboard.press('ArrowRight')
  await expect(page.locator('#cap')).toHaveText(b.caption || b.prompt)
  expect(await fullKey(page)).toBe(b.key)
  expect(await visibility()).toBe('hidden')
  const box = await page.locator('#full').boundingBox()
  expect(box.width).toBeGreaterThan(100) // the space is kept, so the layout does not jump
  await loaded(page, b.key)
  await expect.poll(visibility).toBe('visible')
})

test('focus returns to the tile that is showing when the lightbox closes', async ({ page }) => {
  const keys = await domKeys(page)
  await page.locator(`.tile[data-key="${keys[2]}"] a`).focus()
  await page.keyboard.press('Enter') // keyboard users open with Enter
  await loaded(page, keys[2])
  await page.keyboard.press('ArrowRight')
  await loaded(page, keys[3])
  await page.getByRole('button', { name: 'Close' }).click()
  await expect(dialog(page)).toBeHidden()
  await expect.poll(() => focusKey(page)).toBe(keys[3])
})

test('near the end of the loaded tiles, more are fetched and the strip fills in', async ({ page }) => {
  const api = await apiTiles(page.request)
  const keys = await domKeys(page)
  const n = keys.length
  expect(n).toBeLessThan(api.tiles.length)
  // Open the last loaded tile: the next page is fetched without any key press.
  await page.locator(`.tile[data-key="${keys[n - 1]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[n - 1])
  await expect.poll(() => page.locator('#grid .tile').count()).toBeGreaterThan(n)
  // The strip now has the eight images that follow, which were not loaded a moment ago.
  await expect(page.locator('#strip button')).toHaveCount(17)
  await expect(page.locator('#strip button').nth(8)).toHaveAttribute('aria-current', 'true')
  // The large image was not reloaded or hidden by that.
  expect(await page.locator('#full').evaluate(i => getComputedStyle(i).visibility)).toBe('visible')
  await page.keyboard.press('ArrowRight')
  await loaded(page, api.tiles[n].key)
})

test('the strip shows the neighbouring images in gallery order, whatever their prompt', async ({ page }) => {
  const keys = await domKeys(page)
  const strip = () => page.locator('#strip button')
  const stripKeys = () => page.locator('#strip img').evaluateAll(is => is.map(i => i.getAttribute('src').match(/\/img\/(.+)-240\.webp/)[1]))

  // In the middle of the page: eight before, the image itself, eight after.
  const i = 20
  await page.locator(`.tile[data-key="${keys[i]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[i])
  await expect(strip()).toHaveCount(17)
  expect(await stripKeys()).toEqual(keys.slice(i - 8, i + 9))
  await expect(strip().nth(8)).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
  await expect.poll(() => page.locator('#strip img').evaluateAll(is => is.every(i => i.complete && i.naturalWidth > 0))).toBe(true)

  // Selecting a neighbour moves there, and the strip moves with it.
  await strip().nth(10).click()
  await loaded(page, keys[i + 2])
  await expect(page).toHaveURL(new RegExp(`#/p/${keys[i + 2]}$`))
  expect(await stripKeys()).toEqual(keys.slice(i - 6, i + 11))
  await expect(strip().nth(8)).toHaveAttribute('aria-current', 'true')

  // The keyboard focus stays in the strip, on the image now showing, although the strip was rebuilt.
  expect(await page.evaluate(() => document.activeElement?.getAttribute('aria-current'))).toBe('true')

  // The arrow keys move the strip too. With Alt held they are left to the browser (Back and Forward).
  await page.keyboard.press('ArrowLeft')
  await loaded(page, keys[i + 1])
  expect(await stripKeys()).toEqual(keys.slice(i - 7, i + 10))
  expect(await page.evaluate(() => {
    const e = new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, bubbles: true, cancelable: true })
    document.activeElement.dispatchEvent(e)
    return e.defaultPrevented
  })).toBe(false)
  expect(await fullKey(page)).toBe(keys[i + 1])

  // Close: focus lands on the tile that was showing.
  await page.keyboard.press('Escape')
  await expect(dialog(page)).toBeHidden()
  await expect.poll(() => focusKey(page)).toBe(keys[i + 1])

  // At the very start there is nothing before the first image.
  await page.locator(`.tile[data-key="${keys[0]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[0])
  expect(await stripKeys()).toEqual(keys.slice(0, 9))
  await expect(strip().nth(0)).toHaveAttribute('aria-current', 'true')
})

test('on a narrow screen the strip is scrolled so the open image is in view', async ({ browser, baseURL }) => {
  // A landscape phone: the strip is wider than the viewer, so it has to scroll.
  const context = await browser.newContext({ baseURL, viewport: { width: 740, height: 360 } })
  const page = await context.newPage()
  try {
    await page.goto('/')
    await page.waitForSelector('#grid .tile')
    const keys = await domKeys(page)
    await page.locator(`.tile[data-key="${keys[12]}"] a`).evaluate(a => a.click())
    await loaded(page, keys[12])
    const box = await page.locator('#strip').boundingBox()
    const current = await page.locator('#strip [aria-current]').boundingBox()
    expect(await page.locator('#strip').evaluate(s => s.scrollWidth > s.clientWidth)).toBe(true)
    expect(current.x).toBeGreaterThanOrEqual(box.x - 1)
    expect(current.x + current.width).toBeLessThanOrEqual(box.x + box.width + 1)
    // It is in the middle, give or take a thumbnail, so neighbours show on both sides.
    const middle = box.x + box.width / 2
    expect(Math.abs(current.x + current.width / 2 - middle)).toBeLessThan(60)
  } finally {
    await context.close()
  }
})

test('a large image that fails to load shows its description and is retried next time', async ({ page, watched }) => {
  const keys = await domKeys(page)
  let fail = true
  await page.route(`**/img/${keys[5]}.jpg`, route => (fail ? route.abort() : route.continue()))
  await page.locator(`.tile[data-key="${keys[5]}"] a`).evaluate(a => a.click())
  await expect(dialog(page)).toBeVisible()
  // Not left hidden: the alt text can show.
  await expect.poll(() => page.locator('#full').evaluate(i => getComputedStyle(i).visibility)).toBe('visible')
  await page.keyboard.press('Escape')
  await expect(dialog(page)).toBeHidden()
  // Opening the same tile again tries again.
  fail = false
  await page.locator(`.tile[data-key="${keys[5]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[5])
  await expect.poll(() => page.locator('#full').evaluate(i => getComputedStyle(i).visibility)).toBe('visible')
  // The only problems are the ones this test caused by failing that request.
  const mine = watched.problems.filter(p => /ERR_FAILED/.test(p))
  expect(mine.length).toBe(2)
  watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/ERR_FAILED/.test(p)))
})

test('a click on the backdrop closes; a click on the image does not', async ({ page }) => {
  const keys = await domKeys(page)
  const opener = page.locator(`.tile[data-key="${keys[1]}"] a`)
  await opener.click()
  await loaded(page, keys[1])
  await page.locator('#full').click()
  await page.locator('#cap').click()
  await expect(dialog(page)).toBeVisible()
  const box = await dialog(page).boundingBox()
  expect(box.x, 'there is a backdrop beside the dialog at 1440 px').toBeGreaterThan(40)
  await page.mouse.click(box.x / 2, 450)
  await expect(dialog(page)).toBeHidden()
  await expect(opener).toBeFocused()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
})

test('the browser Back button closes the lightbox, Forward reopens it', async ({ page }) => {
  const keys = await domKeys(page)
  await page.locator(`.tile[data-key="${keys[0]}"] a`).click()
  await loaded(page, keys[0])
  await page.goBack()
  await expect(dialog(page)).toBeHidden()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  await page.goForward()
  await expect(dialog(page)).toBeVisible()
  await loaded(page, keys[0])
  // Closing with Esc leaves a history in which Back does not reopen it.
  await page.keyboard.press('Escape')
  await expect(dialog(page)).toBeHidden()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
})

test('a deep link opens the image, also one far down the list', async ({ page }) => {
  const api = await apiTiles(page.request)
  for (const t of [api.tiles[2], api.tiles[150]]) {
    await page.goto('about:blank')
    await page.goto('/#/p/' + t.key)
    await expect(dialog(page)).toBeVisible()
    await loaded(page, t.key)
    // Its details arrive with its tile, however many pages down that is.
    await expect(page.locator('#cmd')).toHaveText(`/${t.command} · ${t.label}`)
    await expect(page.locator('#cap')).toHaveText(t.caption || t.prompt)
    await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
    await expect(page.locator(`.tile[data-key="${t.key}"]`)).toHaveCount(1)
    await page.keyboard.press('Escape')
    await expect(dialog(page)).toBeHidden()
    await expect.poll(() => focusKey(page)).toBe(t.key)
    await expect.poll(() => new URL(page.url()).hash).toBe('')
  }
})

test('a deep link to an image that does not exist closes quietly', async ({ page, watched }) => {
  await page.goto('about:blank')
  // Record everything the live region says: "Loaded N more images" may follow.
  await page.addInitScript(() => {
    window.said = []
    document.addEventListener('DOMContentLoaded', () => {
      const live = document.getElementById('live')
      if (live) new MutationObserver(() => window.said.push(live.textContent)).observe(live, { childList: true })
    })
  })
  await page.goto('/#/p/AAAAAAAAAAAAAAAAAAAAAA')
  await expect.poll(() => page.evaluate(() => window.said)).toContain('Image not found')
  await expect(dialog(page)).toBeHidden()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  await expect.poll(() => page.locator('#grid .tile').count()).toBeGreaterThan(0)
  // The only error is the browser's note about the 404 for that image.
  const expected = watched.problems.filter(p => /404/.test(p))
  expect(expected.length).toBe(1)
  watched.problems.splice(0, watched.problems.length, ...watched.problems.filter(p => !/404/.test(p)))
})

test('lightbox fits a phone screen', async ({ browser, baseURL }, testInfo) => {
  const context = await browser.newContext({
    baseURL, storageState: testInfo.project.use.storageState, viewport: { width: 375, height: 667 },
    hasTouch: true, isMobile: true,
  })
  const page = await context.newPage()
  try {
    await page.goto('/')
    await page.locator('#grid .tile a').nth(1).tap()
    await expect(dialog(page)).toBeVisible()
    const img = await page.locator('#full').boundingBox()
    expect(img.x).toBe(0)
    expect(Math.round(img.width)).toBe(375) // full width on a phone
    for (const name of ['Close', 'Previous image', 'Next image']) {
      const b = await page.getByRole('button', { name }).boundingBox()
      expect(b.width).toBeGreaterThanOrEqual(44) // touch target
      expect(b.height).toBeGreaterThanOrEqual(44)
      expect(b.x).toBeGreaterThanOrEqual(0)
      expect(b.x + b.width).toBeLessThanOrEqual(375)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375)
  } finally {
    await context.close()
  }
})
