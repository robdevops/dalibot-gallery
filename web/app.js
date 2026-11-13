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
const PER = 60 // tiles per batch: divisible by every column count (2 to 6)
const LINK = /^#\/p\/([\w-]{22})$/
const data = new Map() // key -> tile, for every rendered tile: its keys are the de-duplication set
const cols = +getComputedStyle(grid).getPropertyValue('--cols') || 2
let v = '', next = '', since = '', busy = 0, done = 0, tail = null, cur = '', pushed = 0, seek = 0, timer = 0

const say = s => { $('live').textContent = s }
const src = (key, size) => '/img/' + key + (size ? '-' + size + '.webp?v=' + v : '.jpg')
const node = key => grid.querySelector('[data-key="' + key + '"]')

async function get(url) {
  const r = await fetch(url)
  if (r.status == 401) location.href = '/login'
  if (!r.ok) throw Error(r.status)
  return r.json()
}

function tile(t, i) { // i: position on the page for the first row, else -1
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

// Append tiles: top up the last batch to PER, then add whole new batches in one fragment.
function place(lis) {
  const frag = document.createDocumentFragment()
  for (let i = 0; i < lis.length;) {
    if (!tail || tail.childElementCount >= PER) {
      const page = el('li', 'page')
      tail = el('ul', 'sub')
      page.append(tail)
      frag.append(page)
    }
    tail.append(...lis.slice(i, i += PER - tail.childElementCount))
  }
  grid.append(frag)
}

function add(tiles, top) {
  const lis = [], had = data.has(cur)
  for (const t of tiles) {
    if (top) node(t.key)?.remove()
    else if (data.has(t.key)) continue
    lis.push(tile(t, !top && data.size < cols ? data.size : -1))
    data.set(t.key, t)
  }
  if (top) { // new tiles go first; re-batch so every batch is full again
    lis.push(...grid.querySelectorAll('.tile'))
    grid.replaceChildren()
    tail = null
  }
  place(lis)
  if (!had && data.has(cur)) show(cur) // the deep-linked tile arrived: fill in its details
  return lis.length
}

async function more() {
  if (busy || done) return
  busy = 1
  try {
    const j = await get('/api/tiles?limit=' + PER + (next ? '&cursor=' + encodeURIComponent(next) : ''))
    if (!next) since = j.newest || ''
    v = j.v
    const first = !data.size, n = add(j.tiles)
    next = j.next
    done = !next
    if (!first) say('Loaded ' + n + ' more images')
    io.unobserve(end)
    if (!done) io.observe(end) // still in view on a tall screen: fires again
    else if (!data.size) end.textContent = 'No images yet'
    else end.hidden = true
    // A deep link to an image further down: keep loading until its tile is here.
    if (cur && !data.has(cur) && seek++ < 20) setTimeout(more)
  } catch {
    say('Could not load images')
    setTimeout(() => { io.unobserve(end); io.observe(end) }, 5e3)
  }
  busy = 0
}

const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) more() },
  { rootMargin: '0px 0px 1500px 0px' })

// "N new": poll only while the page is visible; nothing is inserted until the click.
async function poll() {
  if (!v) return
  try {
    const n = (await get('/api/tiles/new?since=' + encodeURIComponent(since))).count
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
    const j = await get('/api/tiles/new?tiles=1&since=' + encodeURIComponent(since))
    if (j.count > j.tiles.length || j.v != v) return location.reload()
    add(j.tiles, 1)
    since = j.newest || since
    pill.hidden = true
    say('Loaded ' + j.tiles.length + ' new images')
    grid.querySelector('a')?.focus({ preventScroll: true })
    scrollTo({ top: 0, behavior: matchMedia('(prefers-reduced-motion:reduce)').matches ? 'auto' : 'smooth' })
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
  // Strip: its neighbours in gallery order.
  const all = keys(), i = all.indexOf(key)
  strip.replaceChildren(...(i < 0 ? [] : all.slice(Math.max(0, i - 4), i + 5)).map(k => {
    const s = data.get(k), li = el('li'), b = el('button'), img = el('img')
    b.type = 'button'
    b.title = img.alt = s.caption || s.prompt
    if (k == key) b.setAttribute('aria-current', 'true')
    b.onclick = () => go(k)
    img.width = img.height = 56
    img.src = src(k, 240)
    b.append(img)
    li.append(b)
    return li
  }))
  if (!dlg.open) dlg.showModal()
}
function go(key) {
  history.replaceState(null, '', '#/p/' + key)
  show(key)
}
function step(d) {
  const all = keys(), i = all.indexOf(cur)
  if (i >= 0 && all[i + d]) go(all[i + d])
  else if (d > 0) more()
}
function route() {
  const m = LINK.exec(location.hash)
  if (m) show(m[1])
  else if (dlg.open) dlg.close()
}
dlg.addEventListener('close', () => {
  if (dlg.open) return // reopened before this event ran (a quick Back then Forward)
  if (LINK.test(location.hash)) {
    if (pushed) history.back()
    else history.replaceState(null, '', location.pathname + location.search)
  }
  const a = node(cur)?.firstChild
  cur = ''
  pushed = 0
  a?.focus()
})
dlg.addEventListener('click', e => { if (e.target == dlg) dlg.close() })
addEventListener('keydown', e => {
  const d = dlg.open && { ArrowLeft: -1, ArrowRight: 1 }[e.key]
  if (d) {
    e.preventDefault()
    step(d)
  }
})
full.onload = () => (full.className = '')
full.onerror = () => { // a deep link to an image that is gone
  if (dlg.open && !data.has(cur)) {
    dlg.close()
    say('Image not found')
  }
}
$('x').onclick = () => dlg.close()
$('prev').onclick = () => step(-1)
$('next').onclick = () => step(1)
grid.addEventListener('click', e => { if (e.target.closest('a')) pushed = 1 })
addEventListener('hashchange', route)
addEventListener('popstate', route)
document.addEventListener('visibilitychange', watch)

route()
io.observe(end)
watch()
})()
