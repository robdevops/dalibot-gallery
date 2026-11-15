(() => {
'use strict'
const $ = id => document.getElementById(id)
const el = (tag, cls) => {
  const e = document.createElement(tag)
  if (cls) e.className = cls
  return e
}
const grid = $('grid'), end = $('end'), pill = $('pill'), dlg = $('lb'), full = $('full'), strip = $('strip')
const SIZES = '(min-width:1600px) 17vw, (min-width:1200px) 20vw, (min-width:768px) 25vw, (min-width:480px) 34vw, 50vw'
const PER = 60 // tiles per batch: divisible by 2 to 6 columns
const LINK = /^#\/p\/([\w-]{22})$/
const data = new Map() // key -> tile
const cols = +getComputedStyle(grid).getPropertyValue('--cols') || 2
let tries = 0, v = '', next = '', since = '', busy = 0, done = 0, tail = null, cur = '', pushed = 0, seek = 0, timer = 0

const enc = encodeURIComponent
const style = new URLSearchParams(location.search).get('style') || '' // a listing of one style
const sq = style ? '&style=' + enc(style) : ''
const still = () => matchMedia('(prefers-reduced-motion:reduce)').matches
const say = s => { $('live').textContent = s }
const src = (key, size) => '/img/' + key + (size ? '-' + size + '.webp?v=' + v : '.jpg')
const node = key => grid.querySelector('[data-key="' + key + '"]')

async function get(url) {
  const r = await fetch(url)
  if (r.status == 401) location.href = '/login'
  // 400: a cursor the server no longer accepts (new version or bot token). Start afresh, but not in a loop.
  if (r.status == 400 && performance.now() > 5e3) location.reload()
  if (!r.ok) throw Error(r.status)
  return r.json()
}

function tile(t, i) { // i: place in the first row, else -1
  const li = el('li', 'tile'), a = el('a'), img = el('img')
  let n = 0
  li.dataset.key = t.key
  a.href = '#/p/' + t.key
  img.alt = t.caption || t.prompt
  img.width = img.height = 240
  img.decoding = 'async'
  img.loading = i < 0 ? 'lazy' : 'eager'
  if (i >= 0 && i < 4) img.fetchPriority = 'high'
  img.sizes = (i < 0 ? 'auto, ' : '') + SIZES
  const load = () => {
    const q = n ? '&r=' + n : ''
    img.srcset = src(t.key, 240) + q + ' 240w, ' + src(t.key, 480) + q + ' 480w'
    img.src = src(t.key, 240) + q
  }
  // A thumbnail the server could not make is a 16 px stand-in, or an error. Show the tile as waiting
  // (not as a black hole), and ask again, less and less often.
  const check = ok => {
    li.classList.toggle('lost', !ok)
    if (!ok && n < 12) setTimeout(() => { if (li.isConnected) { n++; load() } }, Math.min(2e3 * 2 ** n, 6e4))
  }
  img.onload = () => check(img.naturalWidth > 40) // a thumbnail is 240 px, scaled by its density; the stand-in 16 px
  img.onerror = () => check(0)
  load()
  a.append(img)
  li.append(a)
  return li
}

// Top up the last batch, then add whole batches.
function place(lis) {
  const pages = []
  for (let i = 0; i < lis.length;) {
    if (!tail || tail.childElementCount >= PER) {
      const page = el('li', 'page')
      tail = el('ul', 'sub')
      page.append(tail)
      pages.push(page)
    }
    tail.append(...lis.slice(i, i += PER - tail.childElementCount))
  }
  grid.append(...pages)
}

// Strip thumbnail: one per loaded tile.
function thumb(t) {
  const li = el('li'), b = el('button'), img = el('img')
  b.title = img.alt = t.caption || t.prompt || t.label
  b.onclick = () => go(t.key)
  img.loading = 'lazy'
  img.src = src(t.key, 240)
  b.append(img)
  li.append(b)
  return li
}

function add(tiles, top) {
  const lis = [], ths = [], had = data.has(cur)
  for (const t of tiles) {
    if (top) node(t.key)?.remove()
    else if (data.has(t.key)) continue
    lis.push(tile(t, !top && data.size < cols ? data.size : -1))
    ths.push(thumb(t))
    data.set(t.key, t)
  }
  if (top) { // new tiles go first; re-batch
    lis.push(...grid.querySelectorAll('.tile'))
    grid.replaceChildren()
    tail = null
  }
  place(lis)
  if (top) strip.replaceChildren(...keys().map(k => thumb(data.get(k))))
  else strip.append(...ths)
  if (dlg.open && (top || !had) && data.has(cur)) show(cur) // strip rebuilt, or its tile arrived
  return lis.length
}

async function more() {
  if (busy || done) return
  busy = 1
  try {
    const j = await get('/api/tiles?limit=' + PER + sq + (next ? '&cursor=' + enc(next) : ''))
    if (!next) since = j.newest || ''
    v = j.v
    const first = !data.size, n = add(j.tiles)
    next = j.next
    done = !next
    if (!first) say('Loaded ' + n + ' more images')
    io.unobserve(end)
    if (!done) io.observe(end) // still in view: fires again
    else if (!data.size) end.textContent = 'No images yet'
    else end.hidden = true
    // A deep link further down: keep loading.
    if (cur && !data.has(cur) && seek++ < 20) setTimeout(more)
  } catch {
    say('Could not load images')
    setTimeout(() => { io.unobserve(end); io.observe(end) }, 5e3)
  }
  busy = 0
}

const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) more() },
  { rootMargin: '0px 0px 1500px 0px' })

// "N new": poll while visible; insert only on click.
async function poll() {
  if (!v) return
  try {
    const n = (await get('/api/tiles/new?since=' + enc(since) + sq)).count
    if (n && pill.hidden) say(n + ' new images')
    pill.textContent = n + ' new'
    pill.hidden = !n
  } catch {}
}
function watch() {
  clearInterval(timer)
  if (document.visibilityState == 'visible') {
    timer = setInterval(poll, 6e4)
    poll()
  }
}
pill.onclick = async () => {
  try {
    const j = await get('/api/tiles/new?tiles=1&since=' + enc(since) + sq)
    if (j.count > j.tiles.length || j.v != v) return location.reload()
    add(j.tiles, 1)
    since = j.newest || since
    pill.hidden = true
    say('Loaded ' + j.tiles.length + ' new images')
    grid.querySelector('a')?.focus({ preventScroll: true })
    scrollTo({ top: 0, behavior: still() ? 'auto' : 'smooth' })
  } catch {}
}

// Lightbox
const keys = () => [...grid.querySelectorAll('.tile')].map(e => e.dataset.key)

// Zoom and pan with the keyboard, a double click and the mouse (a pinch, on a trackpad or a
// touch screen, is left to the browser). z is the scale, 1 to 4; px, py the offset, kept so the image covers its frame.
let z = 1, px = 0, py = 0
function pan(dx, dy, smooth) {
  const r = full.parentNode.getBoundingClientRect(), bx = (z - 1) * r.width / 2, by = (z - 1) * r.height / 2
  px = Math.max(-bx, Math.min(bx, px + dx))
  py = Math.max(-by, Math.min(by, py + dy))
  full.style.transition = smooth && !still() ? '' : 'none'
  full.style.transform = z > 1 ? 'translate(' + px + 'px,' + py + 'px) scale(' + z + ')' : ''
  full.style.cursor = z > 1 ? 'grab' : ''
  touch()
}
// What a finger on the image may do. Zoomed here: drag it (our handlers). The page zoomed by the
// browser: pan every way. Neither: only up and down (app.css), leaving sideways drags to the swipe.
const pinched = () => visualViewport.scale > 1.01 // the page zoomed by the browser: a pinch on a touch screen
function touch() {
  const zoomed = pinched()
  full.style.touchAction = z > 1 ? 'none' : zoomed ? 'auto' : ''
  dlg.ontouchmove = zoomed ? null : move // so that the browser pans without waiting for this script
}
// Zoom to nz, keeping the screen point (x, y) where it is (the centre if not given).
function zoom(nz, x, y, smooth) {
  const r = full.parentNode.getBoundingClientRect()
  const mx = x == null ? 0 : x - r.left - r.width / 2, my = y == null ? 0 : y - r.top - r.height / 2
  nz = Math.max(1, Math.min(4, nz))
  px = mx - nz / z * (mx - px)
  py = my - nz / z * (my - py)
  z = nz
  pan(0, 0, smooth)
}
function show(key) {
  const t = data.get(key)
  if (anim || x) { // moved by other means (a key, the strip) while it was moving
    stop()
    x = 0
    draw()
  }
  if (cur != key) tries = 0
  cur = key
  if (full.dataset.key != key) {
    full.dataset.key = key
    full.className = 'wait' // hidden until loaded
    full.src = src(key)
    if (z > 1) zoom(1)
  }
  full.alt = $('cap').textContent = t ? t.caption || t.prompt : ''
  // The style is a link to the images of that style; while they are shown, to all of them.
  const a = el('a')
  if (t) {
    a.href = t.style == style ? '/' : '/?style=' + enc(t.style)
    a.title = t.style == style ? 'Show all images' : 'Show only ' + t.label + ' images'
    a.textContent = t.label
  }
  $('cmd').replaceChildren(...(t ? ['/' + t.command + ' · ', a] : []))
  // Mark and centre it in the strip.
  const all = keys(), i = all.indexOf(key), c = strip.children[i]?.firstChild
  if (i > all.length - 10) more()
  strip.querySelector('[aria-current]')?.removeAttribute('aria-current')
  if (!dlg.open) dlg.showModal()
  if (c) {
    c.setAttribute('aria-current', 'true')
    strip.scrollLeft = c.offsetLeft - strip.clientWidth / 2 + 28
  }
}
strip.onscroll = () => { // near its end: load more
  if (strip.scrollLeft + strip.clientWidth > strip.scrollWidth - 900) more()
}
function go(key) {
  history.replaceState(null, '', '#/p/' + key)
  show(key)
}
// The key d tiles from the open one.
function near(d) {
  const all = keys(), i = all.indexOf(cur)
  return i < 0 ? '' : all[i + d]
}
function step(d) {
  const k = near(d)
  if (k) go(k)
  else if (d > 0) more()
}
function route() {
  const m = LINK.exec(location.hash)
  if (m) show(m[1])
  else if (dlg.open) dlg.close()
}
dlg.onclose = () => {
  if (dlg.open) return // reopened already (Back then Forward)
  if (LINK.test(location.hash)) {
    if (pushed) history.back()
    else history.replaceState(null, '', location.pathname + location.search)
  }
  const a = node(cur)?.firstChild
  cur = ''
  pushed = 0
  dlg.className = '' // the buttons and caption are shown again next time
  if (z > 1) zoom(1)
  a?.focus()
}
// A tap or click on the large image hides the buttons, caption and strip; the next shows them.
// Dragging moves the zoomed image, with the mouse or a finger. A press that ends somewhere
// else is a drag, not a click. With a mouse a click waits a moment, in case it is the first
// of a double click (which zooms): otherwise the buttons would flash off and on.
let drag = null, dragged = 0, down = [0, 0], mouse = 0, timer2 = 0
const bare = () => dlg.classList.toggle('bare')
full.onclick = e => {
  clearTimeout(timer2)
  if (dragged || e.detail && Math.max(Math.abs(e.clientX - down[0]), Math.abs(e.clientY - down[1])) > 6) dragged = 0
  else if (mouse && e.detail) timer2 = setTimeout(bare, 250)
  else bare()
}
full.onpointerdown = e => {
  dragged = 0
  down = [e.clientX, e.clientY]
  mouse = e.pointerType == 'mouse'
  if (z > 1 && !e.button && !e.ctrlKey) {
    drag = down
    full.setPointerCapture(e.pointerId)
  }
}
full.onpointermove = e => {
  if (!drag) return
  if (!(e.buttons & 1)) return void (drag = null) // the release never reached us
  const dx = e.clientX - drag[0], dy = e.clientY - drag[1]
  if (dragged || Math.abs(dx) + Math.abs(dy) > 3) {
    dragged = 1
    drag = [e.clientX, e.clientY]
    pan(dx, dy)
  }
}
full.onpointerup = full.onpointercancel = full.onlostpointercapture = () => { drag = null }
// A double click with the mouse zooms in on that spot, or back out.
full.ondblclick = e => {
  if (mouse) {
    clearTimeout(timer2)
    zoom(z > 1 ? 1 : 2, e.clientX, e.clientY, 1)
  }
}
dlg.onclick = e => { if (e.target == dlg) dlg.close() }
// Arrow keys: the next or previous image, or, while zoomed, move the image. Space is the next
// image (Shift+Space the previous), except on a strip thumbnail, where it picks that one.
// + and = zoom in, - zooms out, 0 is normal size.
addEventListener('keydown', e => {
  if (!dlg.open || e.altKey || e.metaKey || e.ctrlKey) return
  const a = { ArrowLeft: [1, 0], ArrowRight: [-1, 0], ArrowUp: [0, 1], ArrowDown: [0, -1] }[e.key]
  const f = { '+': 1.5, '=': 1.5, '-': 1 / 1.5, 0: 0 }[e.key]
  if (e.key == ' ' && !strip.contains(document.activeElement)) step(e.shiftKey ? -1 : 1)
  else if (a && z > 1) pan(a[0] * 80, a[1] * 80, 1)
  else if (a && a[0]) step(-a[0])
  else if (f != null) zoom(z * f, null, null, 1)
  else return
  e.preventDefault()
})
// Swiping is direct control of a canvas: the large image sits at x, its neighbours (fetched
// ahead) a width to either side, and a finger sets x. On letting go, x carries on at the speed
// of the finger, as a spring, to the nearest place of rest: here (0) or a neighbour (+-W). A
// touch while it moves catches it where it is. One finger, starting on the image, not zoomed.
let sx = NaN, sy = 0, x = 0, anim = 0, pend = 0, trail = []
const W = () => full.offsetWidth
const peek = el('img') // the neighbour on the side x points to; one picture does for both
peek.id = 'peek'
peek.alt = ''
peek.draggable = false
full.after(peek)
let peeked = ''
function draw() {
  full.style.transition = 'none'
  full.style.transform = x ? 'translateX(' + x + 'px)' : ''
  const d = x < 0 ? 1 : -1, k = x && near(d)
  peek.style.visibility = k ? 'visible' : ''
  if (k) {
    if (peeked != k) peek.src = src(peeked = k)
    peek.style.transform = 'translateX(calc(' + d * 100 + '% + ' + x + 'px))'
  }
}
function stop() {
  cancelAnimationFrame(anim)
  anim = 0
}
dlg.addEventListener('touchstart', e => {
  const t = e.touches
  stop() // catch it
  sx = t[1] || pend || z > 1 || !full.parentNode.contains(e.target) || pinched() ? NaN : t[0].clientX - x
  sy = t[0].clientY
  trail = []
}, { passive: true })
const move = e => {
  const t = e.touches[0], dx = t.clientX - sx // NaN: not a swipe
  if (dx == dx && z == 1 && (x || Math.abs(dx) > 10 && Math.abs(dx) > Math.abs(t.clientY - sy))) {
    if (e.cancelable) e.preventDefault() // or the next tap is eaten
    // Past the first or last image it gives, but only a little.
    x = Math.max(-W(), Math.min(W(), dx * (near(dx < 0 ? 1 : -1) ? 1 : .3)))
    trail = trail.filter(p => e.timeStamp - p[0] < 100).concat([[e.timeStamp, x]])
    draw()
  }
}
// Carry x to rest at `to` (0 or +-W), starting at speed v (px/s): a critically damped spring,
// which never passes `to`. On reaching a neighbour it takes the place of the large image.
function run(to, v) {
  const k = 800, c = 2 * Math.sqrt(k), x0 = x
  let t0 = 0
  const frame = t => {
    const dt = t0 ? Math.min((t - t0) / 1e3, .032) : 0
    t0 = t
    v += (-k * (x - to) - c * v) * dt
    x += v * dt
    if (to ? Math.abs(x) > W() || Math.abs(x - to) < .5 : x * x0 <= 0 || Math.abs(x) < .5) x = to
    if (x == to) return arrive(to)
    draw()
    anim = requestAnimationFrame(frame)
  }
  anim = still() ? 0 : requestAnimationFrame(frame)
  if (!anim) arrive(x = to)
}
function arrive(to) {
  anim = 0
  if (!to) return draw()
  const k = near(to < 0 ? 1 : -1)
  x = 0
  full.style.transform = ''
  peek.style.transform = 'translateX(0)' // it stands in for the large image, which is hidden until loaded
  if (k) pend = 1
  if (k) go(k)
  else draw()
}
// The wheel of a touchpad: a push to the next or previous image. False if there is none, or one is moving.
function fling(d) {
  if (anim || pend || !near(d)) return false
  run(-d * W(), -d * 2200)
  return true
}
function release(e) {
  if (z > 1) { // zoomed during a swipe: the zoom wins
    sx = NaN
    stop()
    x = 0
    draw()
    return pan(0, 0)
  }
  if (pend || e.touches.length) return
  const live = sx == sx && e.type == 'touchend'
  const tr = trail.filter(q => e.timeStamp - q[0] < 100) // the last moments: held still before letting go is no flick
  const v = live && tr.length > 1 ? Math.max(-4e3, Math.min(4e3, (tr.at(-1)[1] - tr[0][1]) / (tr.at(-1)[0] - tr[0][0] || 1) * 1e3)) : 0
  const p = x + v * .15, d = p < 0 ? 1 : -1 // where it is heading, and which way that is
  const far = live && Math.abs(p) > W() * .35 // far enough, or fast enough, to leave the image
  sx = NaN
  if (far && !near(d)) step(d) // past the last loaded image: load more
  if (x || v) run(far && near(d) ? -d * W() : 0, v)
}
dlg.ontouchend = dlg.ontouchcancel = release
dlg.ontouchmove = move
visualViewport.onresize = touch
// The limits of a zoomed image depend on the size of its frame.
addEventListener('resize', () => { if (z > 1) pan(0, 0) })

// A two-finger sideways swipe on a touchpad arrives as wheel events with a
// horizontal delta, followed by momentum that dies away. One swipe moves one
// image: after a move, events are ignored for a quarter of a second, and then
// until they pause, turn round, or grow again. Momentum only shrinks, so two
// deltas in a row well above the smallest seen since the move are a new swipe
// (one alone can be the unevenness of the same swipe).
let wx = 0, wtime = 0, wmoved = -1, wmin = 0, wdir = 0, wup = 0
// A touchpad pinch (a wheel event with ctrlKey) is the browser's: it is ignored here, without
// disturbing a swipe. While the image is zoomed (with the keyboard or a double click), two fingers pan it.
dlg.addEventListener('wheel', e => {
  const x = Math.abs(e.deltaX), t = e.timeStamp
  if (e.ctrlKey || !full.parentNode.contains(e.target)) return
  if (z > 1) {
    e.preventDefault()
    pan(-e.deltaX, -e.deltaY)
    wtime = wmoved = t // what follows is momentum: not a swipe to another image
    wmin = x
    wdir = Math.sign(e.deltaX)
    return
  }
  if (x <= Math.abs(e.deltaY)) return
  e.preventDefault() // not a scroll, and not the browser's swipe for Back
  if (wmoved >= 0) {
    wup = x > wmin * 2 + 6 ? wup + 1 : 0
    wmin = Math.min(wmin, x)
  }
  if (t - wtime > 200 || wmoved >= 0 && t - wmoved > 250 && (wup > 1 || x > 3 && Math.sign(e.deltaX) == -wdir)) {
    wx = 0
    wmoved = -1
  }
  wtime = t
  if (wmoved >= 0) return
  wx += e.deltaX
  if (Math.abs(wx) > 60) {
    wmoved = t
    wmin = x
    wup = 0
    wdir = Math.sign(wx)
    anim || fling(wdir) || step(wdir)
  }
}, { passive: false })
function settle() {
  full.className = ''
  pend = 0
  if (!x) peek.style.visibility = ''
}
// Once the large image is in, fetch the next and the previous one, so that moving on shows
// it at once. The browser keeps them: originals never change.
const ahead = [new Image(), new Image()]
full.onload = () => {
  $('cap').textContent = full.alt
  settle()
  ;[1, -1].forEach((d, i) => {
    const k = near(d)
    if (k) ahead[i].src = src(k)
  })
}
full.onerror = () => {
  settle()
  const key = full.dataset.key
  full.dataset.key = '' // retry next time
  if (dlg.open && !data.has(cur)) { // a dead deep link
    dlg.close()
    say('Image not found')
  } else if (dlg.open && tries < 8) { // Telegram may have failed for a moment: say so, and ask again
    $('cap').textContent = 'Image unavailable, trying again. ' + full.alt
    setTimeout(() => { if (dlg.open && cur == key && !full.dataset.key) { tries++; show(key) } }, Math.min(3e3 * 2 ** tries, 3e4))
  }
}
$('x').onclick = () => dlg.close()
$('prev').onclick = () => step(-1)
$('next').onclick = () => step(1)
grid.onclick = e => { if (e.target.closest('a')) pushed = 1 }
addEventListener('hashchange', route)
addEventListener('popstate', route)
document.onvisibilitychange = watch

route()
io.observe(end)
watch()
})()
