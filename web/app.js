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
let v = '', next = '', since = '', busy = 0, done = 0, tail = null, cur = '', pushed = 0, seek = 0, timer = 0

const enc = encodeURIComponent
const still = () => matchMedia('(prefers-reduced-motion:reduce)').matches
const say = s => { $('live').textContent = s }
const src = (key, size) => '/img/' + key + (size ? '-' + size + '.webp?v=' + v : '.jpg')
const node = key => grid.querySelector('[data-key="' + key + '"]')

async function get(url) {
  const r = await fetch(url)
  if (r.status == 401) location.href = '/login'
  if (!r.ok) throw Error(r.status)
  return r.json()
}

function tile(t, i) { // i: place in the first row, else -1
  const li = el('li', 'tile'), a = el('a'), img = el('img')
  li.dataset.key = t.key
  a.href = '#/p/' + t.key
  img.alt = t.caption || t.prompt
  img.width = img.height = 240
  img.decoding = 'async'
  img.loading = i < 0 ? 'lazy' : 'eager'
  if (i >= 0 && i < 4) img.fetchPriority = 'high'
  img.sizes = (i < 0 ? 'auto, ' : '') + SIZES
  img.srcset = src(t.key, 240) + ' 240w, ' + src(t.key, 480) + ' 480w'
  img.src = src(t.key, 240)
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
    const j = await get('/api/tiles?limit=' + PER + (next ? '&cursor=' + enc(next) : ''))
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
    const n = (await get('/api/tiles/new?since=' + enc(since))).count
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
    const j = await get('/api/tiles/new?tiles=1&since=' + enc(since))
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
function show(key) {
  const t = data.get(key)
  cur = key
  if (full.dataset.key != key) {
    full.dataset.key = key
    full.className = 'wait' // hidden until loaded
    full.src = src(key)
  }
  full.alt = $('cap').textContent = t ? t.caption || t.prompt : ''
  $('cmd').textContent = t ? '/' + t.command + ' · ' + t.label : ''
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
  a?.focus()
}
// A tap or click on the large image hides the buttons, the caption and the strip, and the
// next one shows them again. A swipe is not a tap, so it does not get here.
full.onclick = () => dlg.classList.toggle('bare')
dlg.onclick = e => { if (e.target == dlg) dlg.close() }
addEventListener('keydown', e => {
  const d = dlg.open && !e.altKey && !e.metaKey && !e.ctrlKey && { ArrowLeft: -1, ArrowRight: 1 }[e.key]
  if (d) {
    e.preventDefault()
    step(d)
  }
})
// Swipe the large image: left for the next, right for the previous. One finger,
// starting on the image, not while zoomed. It follows the finger, then slides
// away or springs back.
let sx = NaN, sy = 0, sliding = 0
const slide = x => { full.style.transform = x ? 'translateX(' + x + ')' : '' }
dlg.addEventListener('touchstart', e => {
  const t = e.touches
  sx = t[1] || sliding || !full.parentNode.contains(e.target) || visualViewport.scale > 1.01 ? NaN : t[0].clientX
  sy = t[0].clientY
}, { passive: true })
dlg.ontouchmove = e => {
  const t = e.touches[0], dx = t.clientX - sx // NaN: not a swipe
  if (dx == dx && (/px/.test(full.style.transform) || Math.abs(dx) > 10 && Math.abs(dx) > Math.abs(t.clientY - sy))) {
    if (e.cancelable) e.preventDefault() // or the next tap is eaten
    full.style.transition = 'none'
    slide(dx + 'px')
  }
}
// Slide the image away towards the side d points from and bring in the one d
// places on, which waits on the other side until it has loaded. Returns false
// when there is no such image, or a slide is already under way.
function fling(d) {
  const k = near(d), c = cur
  if (sliding || !k) return false
  sliding = 1
  full.style.transition = ''
  slide(-d * 100 + '%')
  setTimeout(() => {
    sliding = 0
    if (cur != c) return slide() // something else moved on meanwhile
    full.style.transition = 'none'
    slide(d * 100 + '%')
    go(k)
  }, still() ? 0 : 180)
  return true
}
function release(e) {
  if (sliding) return
  const t = e.changedTouches[0], dx = t.clientX - sx, dy = t.clientY - sy
  const d = e.type == 'touchend' && !e.touches.length && Math.abs(dx) > 50 && Math.abs(dx) > 2 * Math.abs(dy) && (dx < 0 ? 1 : -1)
  sx = NaN
  full.style.transition = ''
  if (!d || !fling(d)) {
    full.className || slide() // spring back, unless it is waiting to one side
    if (d) step(d) // past the last loaded image: load more
  }
}
dlg.ontouchend = dlg.ontouchcancel = release
// Unzoomed, the image only lets the browser pan up and down (see app.css), which leaves
// sideways drags to the swipe. Zoomed in, the browser must be free to pan every way.
visualViewport.onresize = () => { full.style.touchAction = visualViewport.scale > 1.01 ? 'auto' : '' }

// A two-finger sideways swipe on a touchpad arrives as wheel events with a
// horizontal delta (a pinch arrives with ctrlKey set, and phones send touch
// events instead, so neither is affected). One swipe moves one image: after a
// move, the momentum that keeps arriving is ignored until it pauses or a new
// swipe makes the delta jump up again.
let wx = 0, wlast = 0, wtime = 0, wdone = 0
dlg.addEventListener('wheel', e => {
  const x = Math.abs(e.deltaX)
  if (e.ctrlKey || x <= Math.abs(e.deltaY) || !full.parentNode.contains(e.target)) return
  e.preventDefault() // not a scroll, and not the browser's swipe for Back
  if (e.timeStamp - wtime > 200 || x > wlast * 2 + 8) wdone = wx = 0 // a pause, or a new swipe
  wtime = e.timeStamp
  wlast = x
  if (wdone) return
  wx += e.deltaX
  if (Math.abs(wx) > 60) {
    wdone = 1
    const d = wx > 0 ? 1 : -1
    fling(d) || step(d)
  }
}, { passive: false })
function settle() {
  full.className = ''
  if (full.style.transform) {
    full.offsetWidth // reflow first
    full.style.transition = ''
    slide()
  }
}
full.onload = settle
full.onerror = () => {
  settle()
  full.dataset.key = '' // retry next time
  if (dlg.open && !data.has(cur)) { // a dead deep link
    dlg.close()
    say('Image not found')
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
