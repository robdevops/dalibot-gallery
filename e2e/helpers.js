// Shared fixtures and helpers for the end-to-end tests.
const base = require('playwright/test')

const TOKEN = () => process.env.E2E_TOKEN
const ADMIN_TOKEN = () => process.env.E2E_ADMIN_TOKEN
const CONTROL = () => process.env.E2E_CONTROL
// The public dev stack (GALLERY_PUBLIC=1), or undefined when the run has none.
const PUBLIC_URL = () => process.env.E2E_PUBLIC_URL
// The chats of the dev stack's records (internal/devstack), without the sign.
const CHAT_IDS = ['1001111111111', '1002222222222', '424242']

// Nothing the browser receives may contain any of these: the gallery token,
// the admin token, the dev stack's bot token and file_id prefix, or anything
// naming Telegram's file API. (Kept in step with internal/devstack.)
const FORBIDDEN = () => [
  TOKEN(),
  ADMIN_TOKEN(),
  '123456:DEV-fake_bot_token_abcdef',
  'AgACAgUAAxkBAAIDEVSTACK',
  'file_id',
  'file_path',
  'api.telegram.org',
  '/file/bot',
]
// The general shape of a Telegram photo file_id, for text responses.
const FILE_ID = /AgAC[A-Za-z0-9_-]{30,}/

// watch(page) records console errors, uncaught exceptions, CSP violations and
// failed requests, and scans every response (headers and body) for secrets.
// done() waits for the scans and returns what was found.
async function watch(page) {
  const problems = []
  const leaks = []
  const scans = []
  let responses = 0
  await page.exposeBinding('__cspViolation', (_, v) => { problems.push('CSP violation: ' + v) })
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', e =>
      window.__cspViolation(e.violatedDirective + ' blocked ' + e.blockedURI))
  })
  page.on('console', m => {
    if (m.type() === 'error') problems.push('console error: ' + m.text())
    else if (/content security policy/i.test(m.text())) problems.push('console: ' + m.text())
  })
  page.on('pageerror', e => problems.push('page error: ' + e.message))
  page.on('requestfailed', r => {
    const why = r.failure()?.errorText || ''
    if (!/ERR_ABORTED/.test(why)) problems.push('request failed: ' + r.url() + ' ' + why)
  })
  page.on('response', r => {
    responses++
    scans.push((async () => {
      const where = r.request().method() + ' ' + new URL(r.url()).pathname + ' (' + r.status() + ')'
      let text = ''
      try {
        text += JSON.stringify(await r.allHeaders())
      } catch {}
      const type = r.headers()['content-type'] || ''
      try {
        const body = await r.body()
        text += '\n' + body.toString('latin1')
        if (!/^image\//.test(type) && FILE_ID.test(body.toString('latin1'))) leaks.push(where + ' contains something shaped like a file_id')
      } catch {} // redirects and aborted loads have no body
      for (const s of FORBIDDEN()) if (s && text.includes(s)) leaks.push(where + ' contains ' + JSON.stringify(s))
    })())
  })
  return {
    problems,
    async done() {
      // A body can stay pending for ever when the page navigated away mid-load.
      await Promise.race([Promise.all(scans), new Promise(r => setTimeout(r, 5000))])
      return { problems, leaks, responses }
    },
  }
}

// Every test gets a watched page (set up before the first navigation); a test
// fails if the page logged an error, broke the CSP or received a secret. Tests
// that expect an error (a 401, a missing image) remove it from
// `watched.problems` themselves.
const test = base.test.extend({
  watched: [async ({ page }, use, testInfo) => {
    const w = await watch(page)
    await use(w)
    const { problems, leaks, responses } = await w.done()
    testInfo.annotations.push({ type: 'responses scanned', description: String(responses) })
    base.expect(problems, 'console errors, CSP violations or failed requests').toEqual([])
    base.expect(leaks, 'secrets in responses').toEqual([])
  }, { auto: true }],
})

// The whole tile list from the API, in order. It signs in with the token as
// the HTTP Basic password, because Playwright's request context (unlike the
// browser) does not send a Secure cookie over plain http to 127.0.0.1.
const basic = () => ({ Authorization: 'Basic ' + Buffer.from('e2e:' + TOKEN()).toString('base64') })
async function apiTiles(request, limit = 120) {
  const tiles = []
  let cursor = '', newest = '', v = ''
  for (let i = 0; i < 100; i++) {
    const r = await request.get('/api/tiles?limit=' + limit + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { headers: basic() })
    base.expect(r.status(), 'GET /api/tiles').toBe(200)
    const j = await r.json()
    if (!cursor) { newest = j.newest; v = j.v }
    tiles.push(...j.tiles)
    cursor = j.next
    if (!cursor) return { tiles, newest, v }
  }
  throw new Error('the API never reached the last page')
}

// Keys of the rendered tiles, in document order.
const domKeys = page => page.$$eval('#grid .tile', els => els.map(e => e.dataset.key))

// Geometry of a grid: every tile square, laid out in whole rows with no gap
// between neighbours and none at the left or right edge of the page.
// Returns {count, cols, size, worst} where worst is the largest deviation in
// CSS pixels from the ideal layout.
async function gridGeometry(page, selector = '#grid .tile') {
  return page.evaluate(sel => {
    const rects = [...document.querySelectorAll(sel)].map(e => e.getBoundingClientRect())
    const width = document.documentElement.clientWidth
    const out = { count: rects.length, cols: 0, size: 0, worst: 0, width, inner: window.innerWidth, issues: [] }
    if (!rects.length) return out
    const cols = out.cols = Math.round(width / rects[0].width)
    out.size = rects[0].width
    const note = (what, d, i) => {
      d = Math.abs(d)
      if (d > out.worst) out.worst = d
      if (d > 0.05 && out.issues.length < 10) out.issues.push(what + ' at tile ' + i + ': ' + d.toFixed(3) + ' px')
    }
    rects.forEach((r, i) => {
      note('not square', r.width - r.height, i)
      const col = i % cols
      if (col === 0) note('gap at the left edge', r.left, i)
      else {
        note('horizontal gap', r.left - rects[i - 1].right, i)
        note('row misaligned', r.top - rects[i - 1].top, i)
      }
      if (col === cols - 1) note('gap at the right edge', width - r.right, i)
      if (i >= cols) note('vertical gap', r.top - rects[i - cols].bottom, i)
    })
    return out
  }, selector)
}

// Counts the pixels of a PNG that are not (nearly) white, by decoding it on a
// canvas in a blank page. Used with white stand-in images to prove that no
// background shows between tiles once the page is painted.
async function nonWhitePixels(context, png) {
  const p = await context.newPage()
  try {
    await p.goto('about:blank')
    return await p.evaluate(async b64 => {
      const bin = Uint8Array.from(atob(b64), c => c.charCodeAt(0))
      const bmp = await createImageBitmap(new Blob([bin], { type: 'image/png' }))
      const c = new OffscreenCanvas(bmp.width, bmp.height)
      const g = c.getContext('2d')
      g.drawImage(bmp, 0, 0)
      const d = g.getImageData(0, 0, bmp.width, bmp.height).data
      let bad = 0
      for (let i = 0; i < d.length; i += 4) if (d[i] < 250 || d[i + 1] < 250 || d[i + 2] < 250) bad++
      return { bad, width: bmp.width, height: bmp.height }
    }, png.toString('base64'))
  } finally {
    await p.close()
  }
}

// Waits until every tile image that intersects the viewport has loaded.
// (Polled from here, not with waitForFunction, so that it also works in pages
// where JavaScript is disabled.)
async function viewportImagesLoaded(page, selector = '.tile img') {
  await base.expect.poll(() => page.evaluate(sel => {
    const imgs = [...document.querySelectorAll(sel)].filter(i => {
      const r = i.getBoundingClientRect()
      return r.bottom > 0 && r.top < innerHeight
    })
    return imgs.length > 0 && imgs.every(i => i.complete && i.naturalWidth > 0)
  }, selector).catch(() => false), { message: 'tile images in the viewport loaded', timeout: 20_000 }).toBe(true)
}

async function control(path, method = 'GET') {
  const r = await fetch(CONTROL() + path, { method })
  if (!r.ok) throw new Error(method + ' ' + path + ': ' + r.status + ' ' + await r.text())
  return r.json()
}

module.exports = {
  test, expect: base.expect, devices: base.devices,
  TOKEN, ADMIN_TOKEN, PUBLIC_URL, CHAT_IDS, basic, watch, apiTiles, domKeys, gridGeometry, nonWhitePixels, viewportImagesLoaded, control,
}
