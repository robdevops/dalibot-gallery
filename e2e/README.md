# End-to-end tests

Playwright drives Chromium against the real gallery binary. Nothing here
contacts AWS or Telegram: `scripts/dev-stack.sh` runs a moto DynamoDB server
seeded with about 150 records, a fake Telegram Bot API that serves the sample
JPEGs in `testdata/telegram`, and the gallery built from this checkout. A run
starts two such stacks: the main one, where viewing needs the token, and a
public one (`GALLERY_PUBLIC=1`) for `access.spec.js` and `admin.spec.js`. Both have an admin token.

## Running

Needs: Go, libvips with headers, moto's server (`/opt/moto/bin/moto_server` or
`$MOTO_SERVER`), Node, and Playwright with its Chromium.

```sh
cd e2e
npm install                       # only if Playwright is not already installed
npx playwright install chromium   # likewise
npx playwright test --trace off   # or: npm test
```

The run starts the main dev stack on port 18480 (control server on 18481) and
the public one on 18482 (18483), signs in once through `/login` of the main
one, runs every `*.spec.js` with one worker, and stops the stacks. It takes
about three and a half minutes. Other options:

| Setting | Effect |
|---|---|
| `E2E_PORT=19000` | use another port (and the next three) |
| `E2E_URL=http://127.0.0.1:8080` | do not start a stack; test one that is already running (`scripts/dev-stack.sh -refresh 5s -admin-token dev-admin-token-0123456789`). `E2E_TOKEN`, `E2E_ADMIN_TOKEN` and `E2E_CONTROL` default to the dev stack's tokens and port + 1 |
| `E2E_PUBLIC_URL=http://127.0.0.1:8082` | with `E2E_URL`: a public stack that is already running (`scripts/dev-stack.sh -port 8082 -auth=false -admin-token dev-admin-token-0123456789`). Without it the public-mode tests are skipped |
| `E2E_SCREENSHOTS=1` | write the screenshots to `docs/screenshots/` instead of `e2e/artifacts/screenshots/` (`npm run screenshots` does only this) |

`e2e/artifacts/` holds the dev stacks' logs (`dev-stack.log`,
`dev-stack-public.log`), `perf.json` and the screenshots of the last run.

`login.spec.js` submits one wrong token per run. `/login` allows five failures
a minute per client, so more than five runs within a minute against the same
stack will fail there.

## What is covered

| File | Checks |
|---|---|
| `access.spec.js` | On the main stack: the saved session is a view session and its page is not an admin's; the foot of the page links to `/login`, where the admin token gives a different, equally protected cookie and a page marked `data-admin`; "Sign out" drops the session; each token works as the Basic password with its own level. On the public stack: anyone views with no login and no cookie; every response while scrolling to the end, opening the viewer and polling is searched for the dev stack's chat ids (also inside anything that decodes as base64), and so is every server-rendered page without JavaScript; the listing has no `id`; a cursor the visitor makes is refused; images are `Cache-Control: public` and pages `no-store`; the admin signs in from the "Admin" link, is the only one sent record ids, and signs out to an ordinary visitor's page. |
| `admin.spec.js` | Hiding images. A viewer (main stack) and an anonymous visitor (public stack) are sent no admin script or stylesheet, have no Hide, Undo or Restore controls and no "Hidden images" link, and get 404 from the admin files and routes, also for a POST from the page itself. As the admin: `admin.js` and `admin.css` are loaded and `private`; a click on the image, a drag from the image onto Hide, a double click on Hide, Enter held down, "Hide image" straight after Hide, the arrow keys and a touchpad swipe never hide; the question moves the focus to Cancel and is withdrawn when another image opens; hiding with the keyboard and with the mouse removes the tile and its strip thumbnail, moves the viewer to the next image (the one before at the end), offers Undo inside the viewer or at the foot of the page, and refills the grid's batches with no gap; at once the listing lacks the image and its URLs are 404 for viewers (and `no-store` for the admin); Undo puts it back in its place; the page of hidden images shows it and restores it with the keyboard; a failed request changes nothing and says so; on a phone and on a short laptop screen the Hide control, Cancel and "Hide image" lie inside the viewer's box and on screen, a tap on the image never asks, and a double click that zooms and a drag of the zoomed image onto Hide never hide. On the public stack, another visitor's reloaded page, the page without JavaScript and the image URLs lose the image at once and get it back after Undo, and the admin's list carries no chat id. Whatever a test hides is restored afterwards. |
| `grid.spec.js` | At 375, 390, 479, 480, 800, 1001, 1366, 1440 and 1920 px wide (1x, 2x and 3x): every tile is square, neighbours' bounding rects touch, no gap at either edge, `gap` is 0. Then, with white stand-in images, a screenshot of the first screen and of the seam between two batches must be entirely white, so no background shows between tiles once painted. Order equals the API's (newest first). Scrolling loads every page to the last tile with no duplicate keys and no page requested twice. A page holding cursors the server no longer accepts (from before an upgrade) reloads itself once, on scrolling and on polling for new images, and then loads the next page. |
| `lightbox.spec.js` | Opens on the clicked tile with the 1024 px original; ArrowLeft, ArrowRight and the buttons move between tiles (and load the next page at the end); the strip holds a thumbnail of every loaded image in gallery order, centred on the open one, loads more when it is scrolled to its end or the open image nears it, and keeps the keyboard focus; on a touch screen the large image follows a sideways swipe, slides away and the next or previous one slides in (or it springs back), and a tap straight after a swipe still works; a tap or click on the image hides the buttons, caption and strip and the next one shows them; a two-finger sideways swipe on a touchpad moves one image per swipe; Space and Shift+Space go to the next and previous image; the + = - 0 keys and a double click zoom the image up to 4x (a pinch is left to the browser), and a mouse drag, a finger, two fingers on the trackpad or the arrow keys move it; the next and previous large images are fetched ahead once the open one has loaded; the previous image is hidden while the next loads, and a failed image is retried; Esc, the close button and a click on the backdrop close it; focus returns to the tile; Back closes and Forward reopens; deep links `#/p/<key>`, also for a tile several pages down and for a key that does not exist; layout on a phone. |
| `login.spec.js` | Anonymous visitors are sent to `/login` and get 401 from the API and images; a wrong token is refused; the right one sets an HttpOnly, Secure, SameSite=Strict cookie that does not contain the token; a forged cookie fails; HTTP Basic works; wrong Basic passwords are rate-limited like failed logins (429 after five, also for the right token and for `/login`); `/app.js` needs the login and `/app.css` does not; security headers on every kind of response. |
| `noscript.spec.js` | With JavaScript off: the server-rendered first page is flush and in order, tiles link to the original, and "Load more" walks every page to the end with no duplicates or gaps. |
| `perf.spec.js` | Bytes transferred on a first visit (empty browser cache, warm server cache) against the budget of 2 MB, on four device profiles; nothing but the page and JSON on a second visit; latency of a warm tile (median and p95 must be under 5 ms). |
| `screenshots.spec.js` | The grid and the lightbox at 1440x900 with a device scale factor of 2, and on the iPhone 15 profile. |
| `zz-new.spec.js` | Runs last because it adds records: the "N new" pill appears, counts, inserts nothing until clicked, then prepends the new tiles in order and scrolls to the top; no polling while the page is hidden; the fake Telegram saw only `getFile` and file downloads. |

Every page in every test is also watched (`helpers.js`): a console error, an
uncaught exception, a CSP violation or a failed request fails the test, and so
does any response whose headers or body contain the gallery token, the admin
token, the bot token, a `file_id`, a `file_path` or a Telegram URL.

## Notes on two of the measurements

**"N new" without waiting a minute.** The page polls once a minute while it is
visible and whenever it becomes visible again. The test dispatches the
`visibilitychange` event the browser itself sends when a tab returns to the
front. Neither the page nor the server has a test hook; the stack only runs
with `GALLERY_REFRESH=5s`, the shortest interval the gallery accepts.

**First-screen bytes.** The budget applies to the page plus the tiles in view,
on every profile. Browsers also fetch `loading=lazy` images within a margin
below the viewport (1250 px in Chromium), and that total is measured on a link
of 10 Mbit/s with 40 ms latency. It is asserted at 1440x900 and on the phone,
and only reported at 1920x1080, where it is slightly over 2 MB (48 tiles of
about 43 kB). Straight over loopback, with no latency at all, Chromium fetches
every lazy image of the first batch at once; that figure is reported too, but
it is not what a visitor over a network gets.
