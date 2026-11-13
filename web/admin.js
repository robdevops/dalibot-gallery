// For admins only: hiding an image in the viewer, undoing that, and restoring
// images on the page of hidden images. The server sends this file to nobody
// else, and only an admin's page refers to it. It works on the page app.js
// builds, without changing app.js: the grid, the strip and the address are
// all it needs.
(() => {
'use strict'
const $ = id => document.getElementById(id)
const el = (tag, text, cls) => {
  const e = document.createElement(tag)
  if (text) e.textContent = text
  if (cls) e.className = cls
  return e
}
const say = s => { $('live').textContent = s }
async function post(what, key) {
  const r = await fetch('/api/admin/' + what + '/' + key, { method: 'POST' })
  if (!r.ok) throw Error(r.status)
}

// The page of hidden images: each has a Restore button.
const list = $('hidden')
if (list) {
  list.onclick = async e => {
    const b = e.target.closest('[data-restore]')
    if (!b) return
    b.disabled = true
    try {
      await post('restore', b.dataset.restore)
    } catch {
      b.disabled = false
      b.textContent = 'Not restored. Try again'
      return say('Could not restore the image')
    }
    const li = b.closest('li'), near = li.nextElementSibling || li.previousElementSibling
    li.remove()
    $('none').hidden = !!list.children.length
    say('Image restored')
    ;(near ? near.querySelector('button') : document.querySelector('.back a')).focus()
  }
  return
}

// The gallery: a Hide button under the large image.
const grid = $('grid'), dlg = $('lb'), full = $('full'), strip = $('strip')
const PER = 60 // tiles per batch, as in app.js
const LINK = /^#\/p\/([\w-]{22})$/
const tiles = () => [...grid.querySelectorAll('.tile')]
const node = key => grid.querySelector('[data-key="' + key + '"]')
// The image the viewer shows.
const cur = () => dlg.open && LINK.exec(location.hash)?.[1] || ''

// <p id="adm"> [Hide] | Hide this image from the gallery? [Cancel] [Hide image] | note | Image hidden. [Undo]
const row = el('p'), hide = el('button', 'Hide'), ask = el('span', '', 'ask'), no = el('button', 'Cancel'),
  yes = el('button', 'Hide image', 'go'), note = el('span', '', 'note'), did = el('span', '', 'did'),
  back = el('button', 'Undo'), bar = el('div')
row.id = 'adm'
bar.id = 'undo'
for (const b of [hide, no, yes, back]) b.type = 'button'
ask.append(el('span', 'Hide this image from the gallery?'), no, yes)
ask.hidden = true
row.append(hide, ask, note)
$('cmd').after(row)
document.body.append(bar)

// Hiding takes two steps, on two different buttons, and neither is the
// image: a tap, a swipe or a key held down cannot do it. The second step
// counts only for the image the first was about, and not within half a
// second of it (a double click).
let armed = '', at = 0
function reset(msg) {
  armed = ''
  hide.hidden = false
  ask.hidden = true
  note.textContent = msg || ''
  if (msg) say(msg)
}
hide.onclick = () => {
  armed = cur()
  if (!armed || !node(armed)) return reset('This image is not in the gallery')
  at = performance.now()
  note.textContent = ''
  hide.hidden = true
  ask.hidden = false
  no.focus()
}
no.onclick = () => {
  reset()
  hide.focus()
}
// Another image in the viewer: the question was about the one before.
new MutationObserver(() => reset()).observe(full, { attributes: true, attributeFilter: ['data-key'] })

// Batches of the grid hold 60 tiles each, all but the last: refill them in order.
// False if they do not fit (the last batch is full and app.js has not made another).
function flow(all) {
  const subs = [...grid.querySelectorAll('.sub')]
  if (all.length > subs.length * PER) return false
  subs.forEach((s, n) => s.append(...all.slice(n * PER, n * PER + PER)))
  return true
}

// What Undo needs: the tile and its strip thumbnail as they were, and its neighbours.
let undo = null, timer = 0
function offer(text, u) {
  clearTimeout(timer)
  undo = u
  did.replaceChildren(el('span', text), ...(u ? [back] : []))
  place()
  say(text)
  if (text) timer = setTimeout(offer, 3e4, '') // then it goes away
}
// Inside the viewer while it is open (nothing outside a modal dialog can be used), else at the foot of the page.
function place() {
  if (did.firstChild?.textContent) (dlg.open ? row : bar).append(did)
  else did.remove()
}
new MutationObserver(place).observe(dlg, { attributes: true, attributeFilter: ['open'] })

yes.onclick = async () => {
  const key = armed
  if (!key || key != cur() || performance.now() - at < 500) return
  yes.disabled = no.disabled = true
  try {
    await post('hide', key)
  } catch {
    return reset('Could not hide the image')
  } finally {
    yes.disabled = no.disabled = false
  }
  const all = tiles(), li = node(key), i = all.indexOf(li), th = i < 0 ? null : strip.children[i]
  const prev = all[i - 1]?.dataset.key, next = all[i + 1]?.dataset.key
  reset()
  if (li) {
    all.splice(i, 1)
    li.remove()
    th?.remove()
    flow(all)
  }
  // On to the next image (the one before at the end), through the address, which app.js follows.
  if (cur() == key) {
    hide.focus()
    if (next || prev) location.replace('#/p/' + (next || prev))
    else dlg.close()
  }
  offer('Image hidden.', li && { key, li, th, prev, next })
}

back.onclick = async () => {
  const u = undo
  if (!u) return
  back.disabled = true
  try {
    await post('restore', u.key)
  } catch {
    return say('Could not restore the image')
  } finally {
    back.disabled = false
  }
  // Back where it was, if the tiles around it are still as they were; otherwise start afresh.
  const all = tiles()
  const i = u.next ? all.indexOf(node(u.next)) : all.at(-1)?.dataset.key == u.prev ? all.length : -1
  all.splice(i, 0, u.li)
  if (i < 0 || !flow(all)) return location.reload()
  if (u.th) strip.insertBefore(u.th, strip.children[i] || null)
  offer('Image restored.')
  if (dlg.open) location.replace('#/p/' + u.key)
  else u.li.firstChild.focus()
}
})()
