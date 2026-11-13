// The lightbox: open, arrow keys, style strip, Esc, backdrop, focus, deep links.
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

test('arrow right past the last loaded tile loads the next page', async ({ page }) => {
  const api = await apiTiles(page.request)
  const keys = await domKeys(page)
  const n = keys.length
  expect(n).toBeLessThan(api.tiles.length)
  await page.locator(`.tile[data-key="${keys[n - 1]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[n - 1])
  await page.keyboard.press('ArrowRight') // asks for more tiles
  await expect.poll(() => page.locator('#grid .tile').count()).toBeGreaterThan(n)
  await page.keyboard.press('ArrowRight')
  await loaded(page, api.tiles[n].key)
})

test('the style strip swaps the image within a record', async ({ page }) => {
  const api = await apiTiles(page.request)
  const keys = await domKeys(page)
  const t = api.tiles.find(x => x.styles.length >= 2 && keys.includes(x.key))
  const single = api.tiles.find(x => x.styles.length === 1 && keys.includes(x.key))
  expect(t, 'a record with several styles on the first page').toBeTruthy()
  await page.locator(`.tile[data-key="${t.key}"] a`).evaluate(a => a.click())
  await loaded(page, t.key)

  const buttons = page.locator('#strip button')
  await expect(buttons).toHaveCount(t.styles.length)
  expect(await buttons.evaluateAll(bs => bs.map(b => b.title))).toEqual(t.styles.map(s => s.label))
  const mine = t.styles.findIndex(s => s.key === t.key)
  await expect(buttons.nth(mine)).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
  await expect.poll(() => page.locator('#strip img').evaluateAll(is => is.every(i => i.complete && i.naturalWidth > 0))).toBe(true)

  const other = t.styles.findIndex(s => s.key !== t.key)
  await buttons.nth(other).click()
  await loaded(page, t.styles[other].key)
  await expect(page).toHaveURL(new RegExp(`#/p/${t.styles[other].key}$`))
  await expect(page.locator('#strip button').nth(other)).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
  await expect(page.locator('#cap')).toHaveText(t.styles[other].caption || t.prompt)
  await expect(page.locator('#cmd')).toHaveText(`/${t.command} · ${t.styles[other].label}`)

  // Back to the first style, then close: focus lands on that tile.
  await page.locator('#strip button').nth(mine).click()
  await loaded(page, t.key)
  await page.keyboard.press('Escape')
  await expect(dialog(page)).toBeHidden()
  await expect.poll(() => focusKey(page)).toBe(t.key)

  // A record with one image has no strip.
  if (single) {
    await page.locator(`.tile[data-key="${single.key}"] a`).evaluate(a => a.click())
    await loaded(page, single.key)
    await expect(page.locator('#strip button')).toHaveCount(0)
    await expect(page.locator('#strip')).toBeHidden()
  }
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
    await expect(page.locator('#strip button')).toHaveCount(t.styles.length > 1 ? t.styles.length : 0)
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
