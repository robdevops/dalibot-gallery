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
// Whether img shows the picture at url (while another is on its way, the old one counts as loaded).
const ready = (img, url) => img.complete && img.naturalWidth && img.currentSrc.endsWith(url)

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

// The style groups a gallery can be narrowed to, and the runs of words that name them: one list,
// the server's (internal/styles). Words are split and folded as there: letters and digits, lower
// case, no accents, "&" as "and".
const groups = JSON.parse(dlg.dataset.groups || '[]') // [[key, label, [[word, ...], ...]], ...]
const fold = w => w == '&' ? 'and' : w.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase()
// A link to the gallery of one group; inside that gallery, back to all of them.
function tag(key, label, text) {
  const a = el('a')
  a.href = key == style ? '/' : '/?style=' + enc(key)
  a.title = key == style ? 'Show all images' : 'Show only ' + label + ' images'
  a.textContent = text
  return a
}
// The text with each run of words that names a group made a link to it (the longest run wins),
// and the groups it named, first found first.
function linked(text) {
  const out = [], hits = [], ws = [...text.matchAll(/[\p{L}\p{N}]+|&/gu)].map(m => [m.index, m.index + m[0].length, fold(m[0])])
  let at = 0
  for (let i = 0; i < ws.length;) {
    let best
    for (const [key, label, terms] of groups) for (const t of terms) {
      if (t.length > (best?.[2] || 0) && t.every((w, j) => ws[i + j]?.[2] == w)) best = [key, label, t.length]
    }
    if (!best) { i++; continue }
    const a = ws[i][0], b = ws[i + best[2] - 1][1]
    out.push(text.slice(at, a), tag(best[0], best[1], text.slice(a, b)))
    hits.push(best)
    at = b
    i += best[2]
  }
  out.push(text.slice(at))
  return [out, hits]
}
// The caption, its style words linked, after a note if there is one.
// One line; a longer one opens on a tap or Enter, and is closed again for the next image.
function caption(note) {
  const c = $('cap')
  c.replaceChildren(...(note ? [note] : []), ...linked(full.alt)[0])
  c.classList.remove('open')
  fits()
}
function fits() {
  const c = $('cap'), long = c.classList.contains('open') || c.scrollHeight > c.clientHeight + 1
  c.classList.toggle('long', long)
  c.tabIndex = long ? 0 : -1
  if (long) c.setAttribute('aria-expanded', c.classList.contains('open'))
  else c.removeAttribute('aria-expanded')
}
$('cap').onclick = e => {
  const c = $('cap')
  if (e.target.closest('a') || !c.classList.contains('long')) return
  c.classList.toggle('open')
  fits()
}
$('cap').onkeydown = e => { if (e.key == 'Enter') $('cap').onclick(e) }

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
  if (anim || canvas()) { // a swipe under way ends here
    stop()
    sx = NaN
    still0()
  }
  const r = full.parentNode.getBoundingClientRect()
  const mx = x == null ? 0 : x - r.left - r.width / 2, my = y == null ? 0 : y - r.top - r.height / 2
  nz = Math.max(1, Math.min(4, nz))
  px = mx - nz / z * (mx - px)
  py = my - nz / z * (my - py)
  z = nz
  pan(0, 0, smooth)
}
function show(key, keep) {
  const t = data.get(key)
  if ((anim || x) && !keep) { // moved by other means (a key, the strip) while it was moving
    stop()
    x = 0
    draw()
  }
  if (cur != key) tries = 0
  cur = key
  if (full.dataset.key != key) {
    if (peeked != key) { // a stand-in is for its own image only
      pend = 0
      peek.style.visibility = ''
    }
    full.dataset.key = key
    full.src = src(key)
    full.className = ready(full, src(key)) ? '' : 'wait' // black until loaded
    if (z > 1) zoom(1)
  }
  full.alt = t ? t.caption || t.prompt : ''
  caption()
  // The style is a link to its gallery: the button's, or else the first group the caption names.
  const g = t && (t.style != '_' ? [t.style, t.label] : linked(full.alt)[1][0] || [t.style, t.label])
  $('cmd').replaceChildren(...(t ? ['/' + t.command + ' · ', tag(g[0], g[1], g[1])] : []))
  // Mark and centre it in the strip.
  const all = keys(), i = all.indexOf(key), c = strip.children[i]?.firstChild
  if (i > all.length - 10) more()
  strip.querySelector('[aria-current]')?.removeAttribute('aria-current')
  if (!dlg.open) {
    dlg.showModal()
    wake(1)
  }
  if (c) {
    c.setAttribute('aria-current', 'true')
    strip.scrollLeft = c.offsetLeft - strip.clientWidth / 2 + 28
  }
}
strip.onscroll = () => { // near its end: load more
  if (strip.scrollLeft + strip.clientWidth > strip.scrollWidth - 900) more()
}
function go(key, keep) {
  history.replaceState(null, '', '#/p/' + key)
  show(key, keep)
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
  clearTimeout(idle)
  stop() // whatever was moving or held
  x = held = pend = 0
  sx = NaN
  draw()
  dlg.className = '' // the buttons and caption are shown again next time
  if (z > 1) zoom(1)
  a?.focus()
}
// A tap or click on the large image hides the buttons, caption and strip; the next shows them.
// Dragging moves the zoomed image, with the mouse or a finger. A press that ends somewhere
// else is a drag, not a click. With a mouse a click waits a moment, in case it is the first
// of a double click (which zooms): otherwise the buttons would flash off and on.
let drag = null, dragged = 0, down = [0, 0], mouse = 0, timer2 = 0, idle = 0
// The buttons (not caption and strip) also go 2 s after being shown ('calm'), back on a mouse
// move or a key, kept while hovered or focused; a swipe changes nothing. A tap hides or shows
// all ('bare'), buttons included: with only the buttons gone, two taps bring them back.
const set = (c, on) => { if (dlg.classList.contains(c) != !!on) dlg.classList.toggle(c, !!on) }
const bare = () => {
  const on = !dlg.classList.contains('bare')
  set('bare', on)
  on ? clearTimeout(idle) : wake(1)
}
function wake(show) {
  if (show) set('calm', 0)
  clearTimeout(idle)
  idle = setTimeout(rest, 2e3)
}
function rest() {
  if (!dlg.open) return
  if (dlg.querySelector('.nav:focus-visible, .meta :focus-visible') || mouse && dlg.querySelector('.nav:hover')) return wake(0)
  set('calm', 1)
}
dlg.addEventListener('pointermove', e => { if ((mouse = e.pointerType == 'mouse') && !e.buttons) wake(1) }) // a drag is a swipe: no change
dlg.addEventListener('pointerdown', e => { mouse = e.pointerType == 'mouse' })
full.onclick = e => {
  clearTimeout(timer2)
  if (dragged || e.detail && Math.max(Math.abs(e.clientX - down[0]), Math.abs(e.clientY - down[1])) > 6) dragged = 0
  else if (mouse && e.detail) timer2 = setTimeout(bare, 250)
  else bare()
}
// With the mouse (or a touchpad pressed down) the image is held and swiped like it is with a
// finger, through the same canvas; zoomed in, it is panned instead.
let held = 0 // the mouse is swiping
full.onpointerdown = e => {
  dragged = 0
  down = [e.clientX, e.clientY]
  mouse = e.pointerType == 'mouse'
  if (e.button || e.ctrlKey) return
  if (z > 1) drag = down // zoomed: a finger or the mouse moves the picture
  else if (e.pointerType == 'touch') return // a finger swipes through its own events
  else {
    e.preventDefault() // no text selection, no dragging the picture out
    grab(e.clientX, e.clientY, 1)
    held = 1
  }
  full.setPointerCapture(e.pointerId)
}
full.onpointermove = e => {
  if (held) {
    if (!(e.buttons & 1)) return letGo(held = 0, e.timeStamp) // the release never reached us
    if (follow(e.clientX, e.clientY, e.timeStamp)) dragged = 1
    return
  }
  if (!drag) return
  if (!(e.buttons & 1)) return void (drag = null) // the release never reached us
  const dx = e.clientX - drag[0], dy = e.clientY - drag[1]
  if (dragged || Math.abs(dx) + Math.abs(dy) > 3) {
    dragged = 1
    drag = [e.clientX, e.clientY]
    pan(dx, dy)
  }
}
full.onpointerup = full.onpointercancel = full.onlostpointercapture = e => {
  drag = null
  if (!held) return
  held = 0
  letGo(e.type == 'pointerup', e.timeStamp)
}
// A double click with the mouse zooms in on that spot, or back out.
full.ondblclick = e => {
  if (mouse) {
    clearTimeout(timer2)
    zoom(z > 1 ? 1 : 2, e.clientX, e.clientY, 1)
  }
}
// A click outside the box closes; so does one where the caption and strip were, while a tap has hidden them.
dlg.onclick = e => { if (e.target == dlg || dlg.classList.contains('bare') && !e.target.closest('.stage')) dlg.close() }
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
  else return set('bare', 0), wake(1) // Tab and the rest: the controls are wanted, all of them
  e.preventDefault()
})
// Swiping is direct control of a canvas: the large image sits at x, its neighbours (fetched
// ahead) a width to either side, and a finger sets x. On letting go, x carries on at the speed
// of the finger, as a spring, to the nearest place of rest: here (0) or a neighbour (+-W). A
// touch while it moves catches it where it is. One finger, starting on the image, not zoomed.
let sx = NaN, sy = 0, x = 0, anim = 0, pend = 0, trail = [], target = 0, g0 = 0
const W = () => full.offsetWidth
const peek = el('img') // the neighbour on the side x points to; one picture does for both
peek.id = 'peek'
peek.alt = ''
peek.draggable = false
full.after(peek)
peek.onload = () => peek.classList.remove('wait')
let peeked = ''
function draw() {
  full.style.transition = 'none'
  full.style.transform = x ? 'translateX(' + x + 'px)' : ''
  const d = x < 0 ? 1 : -1, k = x && near(d)
  peek.style.visibility = k ? 'visible' : ''
  if (k) {
    if (peeked != k) { // black until it is in: never the picture it showed before
      peek.src = src(peeked = k)
      peek.classList.toggle('wait', !ready(peek, peek.getAttribute('src')))
    }
    peek.style.transform = 'translateX(calc(' + d * 100 + '% + ' + x + 'px))'
  }
}
function stop() {
  cancelAnimationFrame(anim)
  anim = 0
}
const canvas = () => x
function still0() { // the canvas back at rest, without a spring
  x = 0
  draw()
}
// A finger or the mouse takes hold of the canvas at (cx, cy); ok: it may swipe from here.
function grab(cx, cy, ok) {
  if (anim || x) dragged = 1 // caught while moving: the click that follows is no click
  hop() // on its way to the next image: that one is the open one now, so the next swipe goes on from it
  stop() // catch it
  sx = ok && z == 1 && !pinched() ? cx - x : NaN
  g0 = x // where this hold began
  sy = cy
  trail = []
}
// It moved to (cx, cy) at time ts. True if that moves the canvas.
function follow(cx, cy, ts) {
  const dx = cx - sx // NaN: not a swipe
  if (dx != dx || z > 1 || !x && (Math.abs(dx) <= 10 || Math.abs(dx) <= Math.abs(cy - sy))) return false
  // Past the first or last image it gives, but only a little.
  x = Math.max(-W(), Math.min(W(), dx * (near(dx < 0 ? 1 : -1) ? 1 : .3)))
  trail = trail.filter(p => ts - p[0] < 100).concat([[ts, x]])
  draw()
  return true
}
dlg.addEventListener('touchstart', e => {
  const t = e.touches
  grab(t[0].clientX, t[0].clientY, !t[1] && full.parentNode.contains(e.target))
}, { passive: true })
const move = e => {
  const t = e.touches[0]
  if (follow(t.clientX, t.clientY, e.timeStamp) && e.cancelable) e.preventDefault() // or the next tap is eaten
}
// Carry x to rest at `to` (0 or +-W), starting at speed v (px/s): a critically damped spring,
// which never passes `to`. On reaching a neighbour it takes the place of the large image.
function run(to, v) {
  const k = 800, c = 2 * Math.sqrt(k), x0 = x
  target = to
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
// While the canvas is on its way to a neighbour, that neighbour becomes the open image at once, the
// canvas moved by a width so that nothing jumps: a swipe can follow a swipe without waiting.
function hop() {
  const k = anim && target && near(target < 0 ? 1 : -1)
  if (!k) return
  stop()
  x -= target
  target = 0
  go(k, 1)
  draw()
}
// The wheel of a touchpad: a push to the next or previous image (from wherever one is moving to). False if there is none.
function fling(d) {
  hop()
  if (!near(d)) return false
  stop()
  run(-d * W(), -d * 2200)
  return true
}
function release(e) {
  if (z > 1 || !e.touches.length) letGo(e.type == 'touchend', e.timeStamp)
}
// Let go at time ts (live: by lifting the finger or the button, not by being interrupted).
function letGo(live, ts) {
  if (z > 1) { // zoomed during a swipe: the zoom wins
    sx = NaN
    stop()
    still0()
    return pan(0, 0)
  }
  live = live && sx == sx
  const tr = trail.filter(q => ts - q[0] < 100) // the last moments: held still before letting go is no flick
  const v = live && tr.length > 1 ? Math.max(-4e3, Math.min(4e3, (tr.at(-1)[1] - tr[0][1]) / (tr.at(-1)[0] - tr[0][0] || 1) * 1e3)) : 0
  // Where it is heading, from its place of rest and from where this hold began (after a swipe that
  // was still moving, the image now open can be far to one side): the larger counts.
  const a = x + v * .15, b = a - g0, p = Math.abs(b) > Math.abs(a) ? b : a, d = p < 0 ? 1 : -1
  const far = live && Math.abs(p) > W() * .35 // far enough, or fast enough, to leave the image
  sx = NaN
  if (far && !near(d)) step(d) // past the last loaded image: load more
  if (x || v) run(far && near(d) ? -d * W() : 0, v)
}
dlg.ontouchend = dlg.ontouchcancel = release
dlg.ontouchmove = move
visualViewport.onresize = touch
// The limits of a zoomed image depend on the size of its frame.
addEventListener('resize', () => {
  if (z > 1) pan(0, 0)
  if (dlg.open) fits()
})

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
    fling(wdir) || step(wdir)
  }
}, { passive: false })
function settle() {
  full.className = ''
  pend = 0
  if (!x) peek.style.visibility = ''
}
// Once the large image is in, fetch the four after it and the four before, so that moving on shows
// them at once (the browser keeps them: originals never change): nearest first, two at a time.
let gen = 0
const loading = new Set() // kept until loaded
full.onload = () => {
  caption()
  settle()
  const want = [1, -1, 2, -2, 3, -3, 4, -4].map(near).filter(Boolean), g = ++gen
  const pull = () => {
    const k = g == gen && want.shift() // moved on: this round stops
    if (!k) return
    const img = new Image()
    loading.add(img)
    img.onload = img.onerror = () => { loading.delete(img); pull() }
    img.src = src(k)
  }
  pull()
  pull()
}
full.onerror = () => {
  settle()
  const key = full.dataset.key
  full.dataset.key = '' // retry next time
  if (dlg.open && !data.has(cur)) { // a dead deep link
    dlg.close()
    say('Image not found')
  } else if (dlg.open && tries < 8) { // Telegram may have failed for a moment: say so, and ask again
    caption('Image unavailable, trying again. ')
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
