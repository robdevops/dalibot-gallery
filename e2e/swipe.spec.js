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
    // was caught, and the finger takes it from there, to wherever the finger goes. The image it was on its way
    // to is the open one from then on (so a further swipe goes on from it), standing where it was caught.
    await touch('touchStart', 300)
    for (const x of [250, 200, 150, 100]) { await touch('touchMove', x, 12); await frame() }
    await touch('touchEnd', 0, 12) // on its way to the next image
    await expect.poll(shift, { intervals: [5] }).toBeLessThan(-215) // it has begun to move on
    const caught = await shift() + stage.width // where the next image was
    await touch('touchStart', 200)
    await page.waitForTimeout(100) // held: it does not go on
    expect(await fullKey(page)).toBe(keys[5])
    await expect(page.locator('#peek')).toHaveAttribute('src', `/img/${keys[4]}.jpg`) // the one before, beside it
    const held = await shift()
    expect(held).toBeLessThanOrEqual(caught + 1) // it did not come back
    expect(caught - held).toBeLessThan(160) // and stopped soon after
    await page.waitForTimeout(100)
    expect(await shift()).toBe(held)
    await touch('touchMove', 260) // dragged back to the right by 60 px: the canvas follows
    await expect.poll(async () => Math.round(await shift())).toBe(Math.round(held + 60))
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

// While the image beside it has not loaded, the space beside the large image is black: never the picture
// shown before, nor the open one again. And after a swipe that ends before the new image is in, the middle
// is black until it is.
test('an image that has not loaded yet is black space, not a copy of another', async ({ browser, baseURL }, testInfo) => {
  const context = await browser.newContext({
    baseURL, storageState: testInfo.project.use.storageState, viewport: { width: 375, height: 667 },
    hasTouch: true, isMobile: true,
  })
  const page = await context.newPage()
  try {
    await page.goto('/')
    await page.waitForSelector('#grid .tile')
    const keys = await domKeys(page)
    // The image after the open one takes 2.5 s to arrive, and the one after that 15 s (also when fetched ahead).
    const slow = { [keys[4]]: 2500, [keys[5]]: 15000 }
    await page.route(u => /\/img\/[\w-]{22}\.jpg$/.test(u.pathname), async route => {
      const k = Object.keys(slow).find(k => route.request().url().includes(k))
      if (k) await new Promise(r => setTimeout(r, slow[k]))
      await route.continue()
    })
    await page.locator('#grid .tile a').nth(3).tap()
    await loaded(page, keys[3])
    const cdp = await context.newCDPSession(page)
    const stage = await page.locator('#full').boundingBox()
    const y = Math.round(stage.y + stage.height / 2)
    let clock = Date.now() / 1000
    const touch = (type, x, ms = 16) => {
      clock += ms / 1000
      return cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type == 'touchEnd' ? [] : [{ x, y, id: 0 }], timestamp: clock })
    }
    const peek = () => page.locator('#peek').evaluate(i => ({ src: i.getAttribute('src') || '', opacity: getComputedStyle(i).opacity, shown: getComputedStyle(i).visibility == 'visible' }))

    // The one before has loaded (fetched ahead): dragged right, it is beside the image at once.
    await touch('touchStart', 100)
    await touch('touchMove', 160)
    await touch('touchMove', 220)
    await expect.poll(peek).toEqual({ src: `/img/${keys[2]}.jpg`, opacity: '1', shown: true })
    // Dragged back over to the left: the next one has not arrived, so the space is black, not the one before.
    await touch('touchMove', 40)
    await expect.poll(async () => (await peek()).src).toBe(`/img/${keys[4]}.jpg`)
    expect((await peek()).opacity).toBe('0')
    expect((await peek()).shown).toBe(true)
    // When it arrives, it is there.
    await expect.poll(async () => (await peek()).opacity, { timeout: 8000 }).toBe('1')
    await touch('touchEnd', 0, 250) // held still: back
    await expect.poll(() => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).m41)).toBe(0)

    // A swipe on to the next, and at once to the one after it, which has not loaded: the middle is black
    // (the large image hidden, nothing standing in for it), then the image.
    await page.keyboard.press('ArrowRight')
    await loaded(page, keys[4])
    await touch('touchStart', 300)
    for (const x of [270, 235, 200]) { await touch('touchMove', x, 12); await page.waitForTimeout(20) }
    await touch('touchEnd', 0, 12)
    await expect.poll(() => page.locator('#full').getAttribute('src')).toBe(`/img/${keys[5]}.jpg`)
    const middle = await page.evaluate(() => {
      const f = document.getElementById('full'), p = document.getElementById('peek')
      return { full: getComputedStyle(f).visibility, peek: getComputedStyle(p).visibility == 'hidden' || getComputedStyle(p).opacity == '0' }
    })
    expect(middle).toEqual({ full: 'hidden', peek: true })
    // Moving on with a key while it waits: what stood in for that image does not stay for the next one.
    await page.keyboard.press('ArrowRight')
    await expect.poll(() => page.locator('#full').getAttribute('src')).toBe(`/img/${keys[6]}.jpg`)
    expect(await page.locator('#peek').evaluate(i => getComputedStyle(i).visibility)).toBe('hidden')
    await page.keyboard.press('ArrowLeft')
    await expect.poll(() => page.locator('#full').evaluate(i => i.complete && i.naturalWidth), { timeout: 20000 }).toBe(1024)
    await expect(page.locator('#full')).not.toHaveClass(/wait/)
  } finally {
    await context.close()
  }
})

// The mouse (or a touchpad pressed down) holds and swipes the image just as a finger does.
test('the mouse holds and swipes the large image as a finger does', async ({ page }) => {
  const keys = await domKeys(page)
  await page.locator(`.tile[data-key="${keys[3]}"] a`).click()
  await loaded(page, keys[3])
  const cdp = await page.context().newCDPSession(page)
  const box = await page.locator('#full').boundingBox()
  const y = Math.round(box.y + box.height / 2), cx = Math.round(box.x + box.width / 2)
  let clock = Date.now() / 1000
  const mouse = (type, x, ms = 16) => {
    clock += ms / 1000
    return cdp.send('Input.dispatchMouseEvent', { type, x, y, button: 'left', buttons: type == 'mouseReleased' ? 0 : 1, clickCount: 1, timestamp: clock })
  }
  const shift = () => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).m41)
  const bare = () => page.locator('dialog#lb').evaluate(d => d.classList.contains('bare'))

  // Held and dragged: the image follows the pointer, with the next one beside it.
  await mouse('mousePressed', cx)
  await mouse('mouseMoved', cx - 40)
  await mouse('mouseMoved', cx - 120)
  await expect.poll(shift).toBe(-120)
  await expect(page.locator('#peek')).toHaveAttribute('src', `/img/${keys[4]}.jpg`)
  // Held still and let go: back, and the image is the same; letting go is not a click.
  await mouse('mouseReleased', cx - 120, 250)
  await expect.poll(shift).toBe(0)
  expect(await fullKey(page)).toBe(keys[3])
  await page.waitForTimeout(300)
  expect(await bare()).toBe(false)

  // A quick flick goes on to the next image.
  await mouse('mousePressed', cx)
  for (const dx of [30, 65, 100]) { await mouse('mouseMoved', cx - dx, 12); await page.waitForTimeout(20) }
  await mouse('mouseReleased', cx - 100, 12)
  await loaded(page, keys[4])

  // Caught while it moves: a press stops it where it is. That press is part of the swipe, not a click
  // that hides or shows the controls.
  await page.mouse.move(cx, y - 1) // shown, for 2 s
  const shown = await bare()
  await mouse('mousePressed', cx)
  for (const dx of [50, 100, 150, 200]) { await mouse('mouseMoved', cx - dx, 12); await page.waitForTimeout(20) }
  await mouse('mouseReleased', cx - 200, 12)
  await expect.poll(shift, { intervals: [5] }).toBeLessThan(-215)
  await mouse('mousePressed', cx)
  const held = await shift()
  await page.waitForTimeout(150)
  expect(await shift()).toBe(held)
  await mouse('mouseReleased', cx, 250)
  await expect.poll(async () => Math.abs(await shift()) < 1 || Math.abs(await shift()) > box.width - 1).toBe(true)
  await page.waitForTimeout(400)
  expect(await bare()).toBe(shown)

  // Zoomed in, a drag moves the picture instead, and does not change the image.
  await page.waitForTimeout(500)
  const here = await fullKey(page)
  await page.keyboard.press('+')
  await mouse('mousePressed', cx)
  await mouse('mouseMoved', cx - 60)
  await mouse('mouseReleased', cx - 60)
  await expect.poll(() => page.locator('#full').evaluate(i => i.style.transform)).toMatch(/^translate\(-60px, 0px\) scale\(1\.5\)$/)
  expect(await fullKey(page)).toBe(here)

  // Zoomed in the middle of a swipe: the zoom wins, and the image is not drawn back at normal size.
  await page.keyboard.press('0')
  await page.waitForTimeout(300)
  await mouse('mousePressed', cx)
  await mouse('mouseMoved', cx - 60)
  await mouse('mouseMoved', cx - 150)
  await expect.poll(shift).toBe(-150)
  await page.keyboard.press('+')
  await mouse('mouseReleased', cx - 150, 250)
  await page.waitForTimeout(500)
  expect(await page.locator('#full').evaluate(i => i.style.transform)).toMatch(/scale\(1\.5\)$/)
})

// A swipe can follow a swipe at once, without waiting for each to come to rest: each goes on from the image
// the last one was on its way to.
test('swipe after swipe goes on without waiting for the last to come to rest', async ({ browser, baseURL }, testInfo) => {
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
    let clock = Date.now() / 1000
    const touch = (type, x, ms = 16) => {
      clock += ms / 1000
      return cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type == 'touchEnd' ? [] : [{ x, y, id: 0 }], timestamp: clock })
    }
    const moving = () => page.evaluate(() => !!document.getElementById('full').style.transform)
    // Three quick flicks to the left, each begun while the last is still moving.
    for (let i = 0; i < 3; i++) {
      await touch('touchStart', 300)
      for (const x of [270, 235, 200]) { await touch('touchMove', x, 12); await page.waitForTimeout(20) }
      await touch('touchEnd', 0, 12)
      if (i < 2) expect(await moving(), `flick ${i + 1} is still moving when the next begins`).toBe(true)
    }
    await loaded(page, keys[6]) // three on, none lost and none twice
    await expect(page).toHaveURL(new RegExp(`#/p/${keys[6]}$`))
    await expect.poll(() => page.locator('#full').evaluate(i => new DOMMatrix(getComputedStyle(i).transform).m41)).toBe(0)
    // And back, two at once.
    for (let i = 0; i < 2; i++) {
      await touch('touchStart', 80)
      for (const x of [110, 145, 180]) { await touch('touchMove', x, 12); await page.waitForTimeout(20) }
      await touch('touchEnd', 0, 12)
    }
    await loaded(page, keys[4])
  } finally {
    await context.close()
  }
})
