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

// Whether every strip thumbnail that is in view has loaded (the others load lazily).
const visibleThumbsLoaded = page => page.locator('#strip').evaluate(s => {
  const box = s.getBoundingClientRect()
  const seen = [...s.querySelectorAll('img')].filter(i => {
    const r = i.getBoundingClientRect()
    return r.right > box.left && r.left < box.right
  })
  return seen.length > 0 && seen.every(i => i.complete && i.naturalWidth > 0)
})

test('near the end of the loaded tiles, more are fetched and the strip grows', async ({ page }) => {
  const api = await apiTiles(page.request)
  const keys = await domKeys(page)
  const n = keys.length
  expect(n).toBeLessThan(api.tiles.length)
  // Open the last loaded tile: the next page is fetched without any key press.
  await page.locator(`.tile[data-key="${keys[n - 1]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[n - 1])
  await expect.poll(() => page.locator('#grid .tile').count()).toBeGreaterThan(n)
  // The strip has a thumbnail for every loaded tile, so it grew too; the open image is still marked.
  await expect.poll(() => page.locator('#strip button').count()).toBeGreaterThan(n)
  await expect(page.locator('#strip button').nth(n - 1)).toHaveAttribute('aria-current', 'true')
  // The large image was not reloaded or hidden by that.
  expect(await page.locator('#full').evaluate(i => getComputedStyle(i).visibility)).toBe('visible')
  await page.keyboard.press('ArrowRight')
  await loaded(page, api.tiles[n].key)
})

test('the strip holds every loaded image in gallery order, whatever their prompt', async ({ page }) => {
  const keys = await domKeys(page)
  const strip = () => page.locator('#strip button')
  const stripKeys = () => page.locator('#strip img').evaluateAll(is => is.map(i => i.getAttribute('src').match(/\/img\/(.+)-240\.webp/)[1]))

  const i = 20
  await page.locator(`.tile[data-key="${keys[i]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[i])
  expect(await stripKeys()).toEqual(await domKeys(page))
  await expect(strip().nth(i)).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
  // Thumbnails in view have loaded; those far along the strip have not been fetched yet.
  await expect.poll(() => visibleThumbsLoaded(page)).toBe(true)
  expect(await page.locator('#strip img').evaluateAll(is => is.every(x => x.loading === 'lazy'))).toBe(true)

  // Selecting a neighbour moves there; the strip is not rebuilt, so the focus stays on that thumbnail.
  await strip().nth(i + 2).click()
  await loaded(page, keys[i + 2])
  await expect(page).toHaveURL(new RegExp(`#/p/${keys[i + 2]}$`))
  await expect(strip().nth(i + 2)).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('#strip [aria-current]')).toHaveCount(1)
  expect(await page.evaluate(() => document.activeElement?.getAttribute('aria-current'))).toBe('true')

  // The arrow keys move the mark too. With Alt held they are left to the browser (Back and Forward).
  await page.keyboard.press('ArrowLeft')
  await loaded(page, keys[i + 1])
  await expect(strip().nth(i + 1)).toHaveAttribute('aria-current', 'true')
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

  // The first image is the first thumbnail.
  await page.locator(`.tile[data-key="${keys[0]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[0])
  await expect(strip().nth(0)).toHaveAttribute('aria-current', 'true')
})

test('scrolling the strip to its end loads more, without moving the strip or the large image', async ({ page }) => {
  const keys = await domKeys(page)
  const n = keys.length
  await page.locator(`.tile[data-key="${keys[3]}"] a`).evaluate(a => a.click())
  await loaded(page, keys[3])
  await expect(page.locator('#strip button')).toHaveCount(n)

  // Scroll the strip all the way along, twice: each time more thumbnails arrive.
  let count = n
  for (let round = 0; round < 2; round++) {
    const at = await page.locator('#strip').evaluate(s => (s.scrollLeft = s.scrollWidth - s.clientWidth))
    await expect.poll(() => page.locator('#strip button').count()).toBeGreaterThan(count)
    count = await page.locator('#strip button').count()
    // Not thrown back to the open image.
    expect(await page.locator('#strip').evaluate(s => s.scrollLeft)).toBeGreaterThanOrEqual(at - 1)
  }
  expect(await page.locator('#grid .tile').count()).toBe(count)
  expect(await fullKey(page)).toBe(keys[3])
  await expect(page.locator('#strip button').nth(3)).toHaveAttribute('aria-current', 'true')
  // A thumbnail far along the strip opens that image.
  await page.locator('#strip button').nth(count - 1).click()
  await loaded(page, (await domKeys(page))[count - 1])
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

test('on a touch screen, swiping the large image moves to the next or previous one', async ({ browser, baseURL }, testInfo) => {
  const context = await browser.newContext({
    baseURL, storageState: testInfo.project.use.storageState, viewport: { width: 375, height: 667 },
    hasTouch: true, isMobile: true,
  })
  const page = await context.newPage()
  try {
    await page.goto('/')
    await page.waitForSelector('#grid .tile')
    const keys = await domKeys(page)
    await page.locator('#grid .tile a').nth(3).tap()
    await loaded(page, keys[3])
    const cdp = await context.newCDPSession(page)
    const box = await page.locator('#full').boundingBox()
    const y = box.y + box.height / 2
    // Real touch input, from (x1, y1) to (x2, y2) in a few moves.
    const swipe = async (x1, y1, x2, y2, extraFinger) => {
      const pt = (x, y, id = 0) => ({ x: Math.round(x), y: Math.round(y), id })
      const extra = extraFinger ? [pt(50, 500, 1)] : []
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [pt(x1, y1), ...extra] })
      for (let i = 1; i <= 4; i++) {
        await cdp.send('Input.dispatchTouchEvent', {
          type: 'touchMove', touchPoints: [pt(x1 + (x2 - x1) * i / 4, y1 + (y2 - y1) * i / 4), ...extra],
        })
      }
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    }

    await swipe(300, y, 80, y + 10) // to the left: the next image
    await loaded(page, keys[4])
    await expect(page).toHaveURL(new RegExp(`#/p/${keys[4]}$`))
    await swipe(80, y, 300, y - 10) // to the right: back again
    await loaded(page, keys[3])
    await swipe(80, y, 300, y) // and once more: the one before
    await loaded(page, keys[2])

    // These do nothing: a short drag, a mostly vertical one, and a two-finger gesture.
    await swipe(200, y, 170, y)
    await page.waitForTimeout(200)
    expect(await fullKey(page), 'after a short drag').toBe(keys[2])
    await swipe(200, y - 100, 120, y + 150)
    await page.waitForTimeout(200)
    expect(await fullKey(page), 'after a vertical drag').toBe(keys[2])
    await swipe(300, y, 80, y, true)
    await page.waitForTimeout(200)
    expect(await fullKey(page), 'after a two-finger gesture').toBe(keys[2])
    // Nor does a sideways drag that starts on the strip, which scrolls the strip.
    const st = await page.locator('#strip').boundingBox()
    await swipe(300, st.y + st.height / 2, 80, st.y + st.height / 2)
    await page.waitForTimeout(200)
    expect(await fullKey(page), 'after dragging the strip').toBe(keys[2])
    await expect(dialog(page)).toBeVisible()
  } finally {
    await context.close()
  }
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
