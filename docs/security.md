# Security model

**What the browser receives.** The page, one script, one stylesheet, the JSON
listing, and images. For each tile the listing holds: a 22-character key
(the start of a SHA-256 of the file id), the style key and label, the
caption, the prompt, the command, and the date. When viewing needs the
token, and for an admin, it also holds the record id
(`<chat_id>:<message_id>`), which contains the Telegram chat id. A visitor
of a public gallery is never sent it; see "A public gallery and chat ids" below.

**What the browser never receives.** The Telegram bot token, any Telegram
file id, file path or URL, the AWS credentials, the path of the bot's
database file, or any other attribute of a record.
Images are addressed only by key, and only keys in the current listing are
served, so the gallery cannot be used to fetch arbitrary Telegram files. The
tests scan every response, headers and body, for the seeded file ids and the tokens.

**Who can view, who can administer.** Two separate settings:

- Viewing is either open to everyone who can reach the gallery
  (`GALLERY_PUBLIC=1`) or needs the shared token `GALLERY_TOKEN`. Exactly
  one of the two must be set: with neither the gallery refuses to start, so
  it is never open by accident, and with both as well, because they contradict.
- Administering needs `GALLERY_ADMIN_TOKEN`, which is optional, must differ
  from `GALLERY_TOKEN` and has the same minimum length. Without it there is
  no admin session and no admin route. An admin can also view.

A token is presented in one of two ways:

- at `/login`, which sets a cookie (`HttpOnly`, `Secure`, `SameSite=Strict`,
  one year). One form takes either token and gives the matching session; or
- as the password of HTTP Basic auth (any user name), for scripts. The admin
  token as the password counts as admin.

Nothing else grants anything. In particular `GALLERY_TRUSTED_PROXIES` only
says whose `X-Forwarded-For` to believe for the limiter and the log; a
request from a trusted proxy is treated like any other.

**Sessions.** The cookie `gallery_session` holds an HMAC-SHA256, never a
token: for a view session the message `dalibot-gallery session v1` keyed
with `GALLERY_TOKEN`, for an admin session the message
`dalibot-gallery admin session v1` keyed with `GALLERY_ADMIN_TOKEN`. The two
differ in key and in message, so a view session cannot act as admin, and
knowing the view token or a view cookie does not help to make an admin
cookie. Comparisons are constant-time, and a presented token is always
compared with both. There is one cookie: signing in as admin replaces a view
session. `POST /logout` ("Sign out" at the foot of the page) drops it. There
are no accounts: whoever has a token has what it gives, and the only way to
sign everyone out is to change the token.

A sign-in or sign-out posted from another site's page is refused with 403:
the browser's `Sec-Fetch-Site` header is checked, or, when it is absent, the
`Origin` header against `Host` (so the reverse proxy must pass `Host`
through). Requests with neither header, such as curl, are not affected.

**Open routes.** `/healthz` always. `/login`, `/logout`, the stylesheet
`/app.css` and the icon `/favicon.png` whenever there is something to sign
in to. When viewing needs the token, everything else needs a session or the
Basic password. In a public gallery everything is open, and `/login` and
`/logout` exist only if an admin token is set (otherwise they are 404).
The admin's routes (`/admin/hidden`, `/api/admin/…`) and files (`/admin.js`,
`/admin.css`) are the exception: they exist only with an admin token and
are served only to an admin. Everyone else, with a view session or none,
gets the answer for a path that does not exist, the same for every key.

**The page knows whether the visitor is an admin.** The server renders
`<html data-admin="1">` for an admin and no such attribute for anyone else.
That is a hint for the page only; the server checks the session on every
request and never relies on it.

**Hiding images.** An admin can hide images from the gallery
([Hiding images](admin.md)). This writes only the gallery's own list,
`hidden.json` in `GALLERY_STATE_DIR`; it never touches the bot's table or
database file. The bot's `#delete` removes a record; the gallery's hide only
hides. A change needs an admin session (or the admin token as the Basic
password), must be a `POST`, and is refused with `403` when it comes from
another site's page (`http.CrossOriginProtection`, as for `/login`; the
cookie is `SameSite=Strict`, but a Basic password a browser remembers is
not). A hidden image is left out of the listing and the page without
JavaScript, and its image URLs answer `404` as for a key that never
existed, so nobody can tell a hidden image from a missing one. An admin is
still served a hidden image, with `Cache-Control: no-store` and no `ETag`.
The list holds image keys and times only. Hidden is not secret: the image
is still in the bot's records and in Telegram, and copies fetched before it
was hidden (in browsers, or in a shared cache in front of a public gallery)
are not recalled.

**Guessing a token is rate-limited.** A wrong token counts the same whether
it is posted to `/login` or sent as a Basic password to any route, and one
guess is a guess at both tokens, counted once:

- One client may present 5 wrong tokens per minute. After that it gets 429
  with `Retry-After` on `/login` and on every request that carries a Basic
  password, right or wrong, view token or admin token, until the minute has
  passed. A client is an IPv4 address or an IPv6 /64. Checking and counting
  are one step, so parallel requests cannot slip past the limit.
- In a public gallery, and for a client that already has a view session, a
  wrong or blocked Basic password does not stop viewing: the request is
  served as an ordinary visitor's. It is still counted, and while the client
  is blocked the admin token is not evaluated, so nothing in the response
  tells a blocked guesser whether a guess was right.
- All clients together may present 300 wrong tokens per minute. Above that,
  every sign-in and every Basic request gets 429 for up to a minute. This
  ceiling only matters against a guesser with many addresses. The price is
  that such an attacker (at least 60 addresses, each failing at full rate)
  can keep others from signing in for as long as the attack lasts.
- A valid session cookie is never limited or blocked, and requests without
  credentials do not count. A Basic password is only looked at when it could
  raise what the request may do, so in a public gallery without an admin
  token it is ignored altogether.
- The same wrong Basic password sent again by the same client within the
  minute is not counted again: it is no new guess. Browsers resend a Basic
  password with every request, and so does a proxy that does its own Basic
  sign-in and forwards the header; counting each request would block such
  a visitor, `/login` included, after one page. Each different wrong
  password still counts. The limiter keeps only a keyed fingerprint of a
  wrong password (random key per process), never the password. Tokens
  posted to `/login` are always counted.
- The limit is only as good as the client address. Behind a reverse proxy
  that is not listed in `GALLERY_TRUSTED_PROXIES`, every visitor has the
  proxy's address and shares one allowance of 5; see [Reverse proxy](reverse-proxy.md).
- The limiter slows guessing; it does not make a weak token safe. Use long
  random ones (`openssl rand -base64 32`). The 16-character minimum checks
  length, not randomness.

**A public gallery and chat ids.** A record's id is
`<chat_id>:<message_id>`. With `GALLERY_PUBLIC=1` nothing a visitor receives
holds it:

- The JSON listing has no `id` field for anyone but an admin.
- Cursors (`next`, `newest`, the `since` parameter, the "Load more" link of
  the page without JavaScript) hold a tile's date, its place within its
  record, and the image key of the record's original picture. All three are
  public already; no cursor holds a record id or chat id, and none is read
  from one.
- Deep links (`#/p/<key>`), image URLs and ETags hold only the image key, a
  hash of the Telegram file id.
- Records with the same date to the second are listed in the order of their
  ids. That order is visible; the ids are not.

Captions and prompts are shown as people wrote them. If someone typed a chat
id, a name or a phone number into a prompt, a public gallery shows it.
`GALLERY_CHATS` limits a gallery to chosen chats.

**Caching.** Image responses are `Cache-Control: private, max-age=31536000,
immutable` when viewing needs the token, so that no shared cache keeps what
is behind the login, and `public, …` in a public gallery. Pages and the JSON
listing are `no-store` in every mode: they differ between a visitor and an
admin.

**Headers** on every response the gallery's handler writes (every route and status, including 401, 404, 405 and 429):

```
Content-Security-Policy: default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy: accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=(), interest-cohort=()
X-Frame-Options: DENY
Cross-Origin-Resource-Policy: same-origin
Cross-Origin-Opener-Policy: same-origin
```

There are no inline scripts or styles and no third-party resources. The
gallery does not send `Strict-Transport-Security`; the proxy examples add it.

One gap: a request that Go's HTTP server rejects before it reaches the
handler gets Go's own short `text/plain` answer without these headers. That
covers a malformed request line, a missing `Host` header (400), headers over
16 KB (431) and `OPTIONS *`. Those answers are fixed strings that contain
nothing from the request, and Go offers no hook to add headers to them.
Behind a reverse proxy, clients normally never see them: the proxy rejects
such requests itself.

**Telegram allowlist.** The Telegram client can build exactly two requests:
`POST /bot<token>/getFile` and `GET /file/bot<token>/<file_path>`. It never
calls `getUpdates`, `setWebhook`, `deleteWebhook` or any send or set method,
so it cannot take the bot's updates or change the bot. A test fails if the
fake Telegram server sees any other path. Downloads are capped at 20 MiB,
with timeouts, at most 4 at a time; failures are remembered (24 h for a file
Telegram rejects, 5 minutes otherwise) and the tile shows a plain placeholder.

**DynamoDB.** Read-only by policy and by code: the gallery only calls `Scan`, `DescribeTable` and, to follow the table's stream, `DescribeStream`, `GetShardIterator` and `GetRecords`.
Whatever the source, the only files the gallery writes are its cache and
its list of hidden images.

**SQLite.** With `GALLERY_SOURCE=sqlite` the bot's database file is opened
read-only for each read (`mode=ro` and `query_only`) and closed again; the
only statement is one `SELECT`. The gallery never creates the file, writes
to it or takes a write lock, and it refuses a database in WAL mode, because
reading one would create files next to it. Mounting the bot's directory
read-only (as `deploy/compose.sqlite.yaml` does) enforces the same from
outside. The tests check that the file and its directory are byte for byte
unchanged after reading, and that write statements fail on the gallery's
connection. Details are in [SQLite](sqlite.md).

**Image decoding.** Before libvips sees a file it must start with the JPEG
magic bytes and its header must declare at most 10,000 px per side and 50
megapixels. libvips runs with `VIPS_BLOCK_UNTRUSTED=1` and with every loader
except JPEG blocked, so PNG, TIFF, SVG and the rest are rejected even if
Telegram were to serve them. The JPEG loader runs with `fail_on=error`, and
every downloaded original is decoded in full once before it is stored: a
file whose body is truncated or corrupt shows the placeholder and is neither
cached nor served.

**Memory.** Decoding a progressive JPEG takes memory in proportion to its
pixel count, whatever the thumbnail size. So besides the
`GALLERY_IMAGE_WORKERS` limit, the images being decoded at one moment may
hold at most 50 megapixels between them: the largest image the header check
accepts is decoded alone, while Telegram's usual 1024×1024 photos
(1 megapixel) are not held back. In one measurement, four concurrent
requests for a 49-megapixel progressive JPEG peaked at 202 MB of resident
memory (651 MB before the budget existed). The gallery is still meant to run
under a hard memory cap (512 MB in the Compose file and the systemd unit):
that one file is the only large input measured, and a 50-megapixel image
with full-resolution colour would need roughly twice as much (an estimate, not measured).

**Logs.** Every log line passes through a filter that removes the gallery's
tokens and the bot token. File ids, file paths, Telegram URLs and cookie
values are not logged. An admin sign-in is logged with the client address,
and so is every hide and restore, with the image key.

**Process.** The gallery listens on `127.0.0.1` by default, sets request
timeouts (header 5 s, read 10 s, write 60 s, idle 120 s) and a 16 KB header
limit, and is meant to run with a hard memory cap as a non-root user on a
read-only filesystem; the Compose file and the systemd unit both do that.
