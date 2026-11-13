# Security model

**What the browser receives.** The page, one script, one stylesheet, the JSON
listing, and images. For each tile the listing holds: a 22-character key
(the start of a SHA-256 of the file id), the record id
(`<chat_id>:<message_id>`), the style key and label, the caption, the prompt,
the command, and the date. Note that the record id contains the chat id.

**What the browser never receives.** The Telegram bot token, any Telegram
file id, file path or URL, the AWS credentials, or any other table attribute.
Images are addressed only by key, and only keys in the current listing are
served, so the gallery cannot be used to fetch arbitrary Telegram files. The
tests scan every response, headers and body, for the seeded file ids and both tokens.

**Authentication.** One shared token, `GALLERY_TOKEN`:

- at `/login`, which sets a cookie (`HttpOnly`, `Secure`, `SameSite=Strict`,
  one year) holding an HMAC-SHA256 derived from the token, not the token itself; or
- as the password of HTTP Basic auth (any user name), for scripts.

A sign-in form posted from another site's page is refused with 403: the
browser's `Sec-Fetch-Site` header is checked, or, when it is absent, the
`Origin` header against `Host` (so the reverse proxy must pass `Host`
through). Requests with neither header, such as curl, are not affected.

Comparisons are constant-time. Everything needs authentication except
`/healthz`, `/login`, and the stylesheet `/app.css` and icon `/favicon.png`,
which the login page uses. There are no accounts or roles: whoever has the token sees everything,
and the only way to sign everyone out is to change the token.

**Guessing the token is rate-limited.** A wrong token counts the same
whether it is posted to `/login` or sent as a Basic password to any route:

- One client may present 5 wrong tokens per minute. After that it gets 429
  with `Retry-After` on `/login` and on every request that carries a Basic
  password, right or wrong, until the minute has passed. A client is an IPv4
  address or an IPv6 /64. Checking and counting are one step, so parallel
  requests cannot slip past the limit.
- All clients together may present 300 wrong tokens per minute. Above that,
  every sign-in and every Basic request gets 429 for up to a minute. This
  ceiling only matters against a guesser with many addresses. The price is
  that such an attacker (at least 60 addresses, each failing at full rate)
  can keep others from signing in for as long as the attack lasts.
- A valid session cookie is never limited or blocked, and requests without
  credentials do not count.
- The limit is only as good as the client address. Behind a reverse proxy
  that is not listed in `GALLERY_TRUSTED_PROXIES`, every visitor has the
  proxy's address and shares one allowance of 5; see [Reverse proxy](../README.md#reverse-proxy).
- The limiter slows guessing; it does not make a weak token safe. Use a long
  random one (`openssl rand -base64 32`). The 16-character minimum checks
  length, not randomness.

With `GALLERY_AUTH=off` the gallery has no login at all and relies entirely
on the proxy in front of it. It refuses to start with neither a token nor
`GALLERY_AUTH=off`.

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

**DynamoDB.** Read-only by policy and by code: the gallery only calls `Scan`.

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

**Logs.** Every log line passes through a filter that removes both tokens.
File ids, file paths, Telegram URLs and cookie values are not logged.

**Process.** The gallery listens on `127.0.0.1` by default, sets request
timeouts (header 5 s, read 10 s, write 60 s, idle 120 s) and a 16 KB header
limit, and is meant to run with a hard memory cap as a non-root user on a
read-only filesystem; the Compose file and the systemd unit both do that.
