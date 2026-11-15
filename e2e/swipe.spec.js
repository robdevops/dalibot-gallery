// Swiping by speed: how fast the finger goes matters, so these run one at a time, not beside other tests.
const { test, expect, apiTiles, domKeys } = require('./helpers')

const dialog = page => page.locator('dialog#lb')

const fullKey = page => page.locator('#full').evaluate(i => i.dataset.key)
async function loaded(page, key) {
  await expect(page.locator('#full')).toHaveAttribute('src', `/img/${key}.jpg`)
  await expect.poll(() => page.locator('#full').evaluate(i => i.complete && i.naturalWidth)).toBe(1024)
}

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('#grid .tile')
})

// The canvas is under the finger the whole time: a touch while it is still moving catches it where it is,
// and the speed of the finger on letting go decides, not only how far it went.
test('a moving image can be caught by a finger, and a flick carries it to the next one', async ({ browser, baseURL }, testInfo) => {
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
    const stage = await page.locator('#full').boundingBox()
    const y = Math.round(stage.y + stage.height / 2)
    // The events carry timestamps of the test's own choosing, so that the speed of the finger is what the test
    // says (CDP itself takes a varying time per call, more when the machine is busy).
    let clock = Date.now() / 1000
    const touch = (type, x, ms = 16) => {
      clock += ms / 1000
      return cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type == 'touchEnd' ? [] : [{ x, y, id: 0 }], timestamp: clock })
    }
    const frame = () => page.waitForTimeout(20) // let the page see the move before the next
    const shift = () => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).m41)

    // A quick flick (100 px at about 2800 px/s) is enough to go on to the next image, though it is far from half way.
    await touch('touchStart', 300)
    for (const x of [270, 235, 200]) { await touch('touchMove', x, 12); await frame() }
    await touch('touchEnd', 0, 12)
    await loaded(page, keys[4])

    // The same distance slowly (about 300 px/s), and held still before letting go, is not: it springs back.
    await touch('touchStart', 300)
    for (const x of [270, 235, 200]) { await touch('touchMove', x, 120); await frame() }
    await touch('touchEnd', 0, 250)
    await expect.poll(shift).toBe(0)
    expect(await fullKey(page)).toBe(keys[4])

    // Let go of a drag that is carrying it to the next image, and touch again while it moves: it stops where it
    // was caught, and the finger takes it from there, to wherever the finger goes.
    await touch('touchStart', 300)
    for (const x of [250, 200, 150, 100]) { await touch('touchMove', x, 12); await frame() }
    await touch('touchEnd', 0, 12) // on its way to the next image
    await expect.poll(shift, { intervals: [5] }).toBeLessThan(-215) // it has begun to move on
    const caught = await shift()
    await touch('touchStart', 200)
    await page.waitForTimeout(100) // held: it does not go on
    const held = await shift()
    expect(held).toBeLessThanOrEqual(caught) // it did not come back
    expect(Math.abs(held - caught)).toBeLessThan(160) // and stopped soon after
    await page.waitForTimeout(100)
    expect(await shift()).toBe(held)
    await touch('touchMove', 260) // dragged back to the right by 60 px: the canvas follows
    await expect.poll(shift).toBe(held + 60)
    await touch('touchEnd', 0, 250) // held still: it rests at the nearer place
    await expect.poll(async () => Math.abs(await shift()) < 1 || Math.abs(await shift()) > 374).toBe(true)
  } finally {
    await context.close()
  }
})

test('a two-finger sideways swipe on a touchpad moves one image per swipe', async ({ page }) => {
  const keys = await domKeys(page)
  await page.locator(`.tile[data-key="${keys[10]}"] a`).click()
  await loaded(page, keys[10])
  const box = await page.locator('#full').boundingBox()
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  // A swipe as a touchpad reports it: wheel events that build up, then momentum that dies away.
  const swipe = async sign => {
    for (const dx of [4, 12, 30, 45, 40, 30, 22, 16, 11, 8, 5, 3, 2, 1, 1]) {
      await page.mouse.wheel(sign * dx, 0)
      await page.waitForTimeout(16)
    }
  }
  const settledAt = async key => {
    await loaded(page, key)
    await expect.poll(() => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).m41)).toBe(0)
    await page.waitForTimeout(250) // the swipe's momentum has ended
    expect(await fullKey(page)).toBe(key)
  }

  await swipe(1) // fingers to the left: the next image, and only one
  await settledAt(keys[11])
  await swipe(1)
  await settledAt(keys[12])
  await swipe(-1) // fingers to the right: the previous image
  await settledAt(keys[11])

  // Two swipes in quick succession, the second starting before the first one's momentum has ended.
  for (const dx of [4, 12, 30, 45, 40, 30, 22, 16, 11, 8]) {
    await page.mouse.wheel(dx, 0)
    await page.waitForTimeout(16)
  }
  await loaded(page, keys[12])
  await page.waitForTimeout(250)
  await page.mouse.wheel(8, 0) // momentum still trickling in
  await swipe(1)
  await settledAt(keys[13])

  // A real touchpad's deltas are uneven. Such a swipe still moves one image, and the new image
  // comes in from the right, the side it is on, never from the left.
  await page.evaluate(() => {
    window.track = []
    const f = document.getElementById('full'), p = document.getElementById('peek')
    const x = e => Math.round(new DOMMatrix(getComputedStyle(e).transform).m41)
    const tick = () => {
      track.push([f.dataset.key, x(f), x(p), getComputedStyle(p).visibility == 'visible'])
      requestAnimationFrame(tick)
    }
    tick()
  })
  for (const dx of [3, 14, 2, 25, 6, 40, 10, 45, 30, 8, 28, 12, 20, 5, 9, 2, 6, 1, 3, 1]) {
    await page.mouse.wheel(dx, 0)
    await page.waitForTimeout(16)
  }
  await settledAt(keys[14])
  const track = await page.evaluate(() => track)
  expect(track.filter(([k, x]) => k === keys[13] && x > 0), 'the old image leaves to the left').toEqual([])
  expect(track.filter(([, , x, shown]) => shown && x < 0), 'the new image (the one beside it) arrives from the right').toEqual([])
  expect(track.some(([, , x, shown]) => shown && x > 0), 'it was seen coming in').toBe(true)
  await swipe(-1)
  await settledAt(keys[13])

  // A second swipe made straight away, while the first one's momentum is still arriving and with no
  // pause between them, counts too. (With the next image fetched ahead, this is how people page.)
  const from = await fullKey(page)
  const at = keys.indexOf(from)
  const wheel = async list => {
    for (const dx of list) {
      await page.mouse.wheel(dx, 0)
      await page.waitForTimeout(16)
    }
  }
  const fingers = [4, 14, 32, 48, 44, 36] // on the pad
  const coasting = [30, 26, 22, 19, 16, 14, 12, 11, 10, 9, 8, 7, 7, 6, 6, 5] // momentum, about a quarter of a second
  await wheel([...fingers, ...coasting])
  await wheel([3, 9, 22, 40, 52, 44, 36]) // fingers down again: the momentum stops and a new swipe builds
  await wheel([30, 24, 19, 15, 12, 9, 7, 5, 4, 3, 2, 1, 1])
  await settledAt(keys[at + 2])
  // And one in the other direction, started during the momentum of a forward swipe.
  await wheel([...fingers, ...coasting])
  await wheel([-3, -9, -22, -40, -52, -44, -36, -28, -20, -14, -9, -5, -3, -1])
  await settledAt(keys[at + 2])
  await wheel([-4, -14, -32, -48, -44, -36, -28, -20, -14, -9, -5, -3, -1])
  await settledAt(keys[at + 1])
  await swipe(-1)
  await settledAt(keys[at])

  // A swipe usually carries a slight pinch with it (the fingers drift together or apart). That must
  // not zoom the image, and must not stop the swipe.
  const scale = () => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).a)
  const here = keys.indexOf(await fullKey(page))
  for (const [dx, pinch] of [[4, -1], [14, -2], [32, -1.5], [48, -1], [44, 0], [36, 1], [30, 0.5], [22, 0], [16, 0], [11, 0], [8, 0], [5, 0], [3, 0], [2, 0], [1, 0]]) {
    await page.mouse.wheel(dx, 0)
    if (pinch) {
      await page.keyboard.down('Control')
      await page.mouse.wheel(0, pinch)
      await page.keyboard.up('Control')
    }
    await page.waitForTimeout(16)
  }
  await settledAt(keys[here + 1])
  expect(await scale()).toBe(1)
  await swipe(-1)
  await settledAt(keys[here])
  expect(await scale()).toBe(1)

  // Left alone: vertical scrolling, a pinch (wheel with Ctrl), and a slow drift that never adds up to a swipe.
  await page.mouse.wheel(0, 300)
  await page.keyboard.down('Control')
  await page.mouse.wheel(120, 0)
  await page.keyboard.up('Control')
  await page.mouse.wheel(20, 0)
  await page.waitForTimeout(400)
  expect(await fullKey(page)).toBe(keys[13])

  // Over the strip a sideways swipe scrolls the strip and does not change the image.
  const strip = await page.locator('#strip').boundingBox()
  await page.mouse.move(strip.x + strip.width / 2, strip.y + strip.height / 2)
  const before = await page.locator('#strip').evaluate(st => st.scrollLeft)
  await swipe(1)
  await page.waitForTimeout(400)
  expect(await fullKey(page)).toBe(keys[13])
  expect(await page.locator('#strip').evaluate(st => st.scrollLeft)).toBeGreaterThan(before)

  // At the first image a swipe to the right does nothing and leaves the image in place.
  await page.keyboard.press('Escape')
  await page.locator(`.tile[data-key="${keys[0]}"] a`).click()
  await loaded(page, keys[0])
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await swipe(-1)
  await settledAt(keys[0])
})
