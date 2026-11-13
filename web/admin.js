// For admins only: hiding an image from the viewer (a trash can in the top
// left corner, level with the close button) and restoring images on the page of
// hidden images. The server sends this file to nobody
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

// The gallery: a trash can in the top left corner of the viewer, the size of the close button.
const grid = $('grid'), dlg = $('lb'), strip = $('strip')
const PER = 60 // tiles per batch, as in app.js
const LINK = /^#\/p\/([\w-]{22})$/
const tiles = () => [...grid.querySelectorAll('.tile')]
const node = key => grid.querySelector('[data-key="' + key + '"]')
// The image the viewer shows.
const cur = () => dlg.open && LINK.exec(location.hash)?.[1] || ''

// Batches of the grid hold 60 tiles each, all but the last: refill them in order.
function flow(all) {
  const subs = [...grid.querySelectorAll('.sub')]
  subs.forEach((s, n) => s.append(...all.slice(n * PER, n * PER + PER)))
}

const trash = el('button', '', 'nav')
trash.id = 'trash'
trash.type = 'button'
trash.title = 'Hide this image from the gallery'
trash.setAttribute('aria-label', trash.title)
const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
svg.setAttribute('viewBox', '0 0 24 24')
svg.setAttribute('aria-hidden', 'true')
const path = document.createElementNS('http://www.w3.org/2000/svg', 'path')
path.setAttribute('d', 'M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14M10 11v6M14 11v6')
svg.append(path)
trash.append(svg)
$('full').parentNode.append(trash)

// One press hides the image. Only the button does it (not a tap on the image, a swipe or a key),
// and for half a second afterwards it ignores presses, so that a double tap does not hide the
// next image as well.
let busy = 0
trash.onclick = async () => {
  const key = cur()
  if (busy || !key || !node(key)) return
  busy = 1
  try {
    await post('hide', key)
  } catch {
    busy = 0
    return say('Could not hide the image')
  }
  const all = tiles(), li = node(key), i = all.indexOf(li), th = i < 0 ? null : strip.children[i]
  const prev = all[i - 1]?.dataset.key, next = all[i + 1]?.dataset.key
  if (li) {
    all.splice(i, 1)
    li.remove()
    th?.remove()
    flow(all)
  }
  // On to the next image (the one before at the end), through the address, which app.js follows.
  if (cur() == key) {
    if (next || prev) location.replace('#/p/' + (next || prev))
    else dlg.close()
  }
  say('Image hidden')
  setTimeout(() => { busy = 0 }, 500)
}
})()
