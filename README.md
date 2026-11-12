# dalibot-gallery

A small, self-hosted web gallery for the images drawn by
[dalibot](https://github.com/robdevops/dalibot). It reads the bot's DynamoDB
table and shows every image as an edge-to-edge grid of square tiles, newest
first, with infinite scroll and a full-size viewer.

- One tile per generated image (every style of every record is its own tile).
- Thumbnails are WebP, centre-cropped squares at 240 px and 480 px, made with
  libvips and cached on disk. The full-size image is the original Telegram JPEG, unchanged.
- The browser only ever talks to the gallery. Only the gallery talks to AWS
  (read-only) and to Telegram (`getFile` and the file download, nothing else).
- The gallery never writes to the table and never changes the bot.
- One Go binary with the page, script and stylesheet embedded (under 10 KB of
  JavaScript and CSS, no framework, no build step). It links libvips through cgo,
  so libvips must be installed wherever it runs; the container image takes care of that.

| Grid, 1440 px wide | Viewer |
|---|---|
| ![The grid on a desktop](docs/screenshots/desktop-1440-grid.jpg) | ![The viewer on a desktop](docs/screenshots/desktop-1440-lightbox.jpg) |

Screenshots from the end-to-end tests, which run the real binary against a
local stand-in for DynamoDB and Telegram that serves the 12 sample images in
`testdata/telegram/`. Phone versions are in `docs/screenshots/`.

Contents: [Quick start](#quick-start-container) ·
[Configuration](#configuration) · [AWS access](#aws-access) ·
[Security model](#security-model) · [Reverse proxy](#reverse-proxy) ·
[Running without a container](#running-without-a-container) ·
[Cache](#cache-and-pipeline_version) · [Upgrading](#upgrading) ·
[Development and tests](#development-and-tests) · [Performance](#measured-performance) ·
[Limitations](#limitations-and-out-of-scope)

## Quick start (container)

You need Docker with Compose, the bot's Telegram token, and a read-only AWS
key for the bot's table (see [AWS access](#aws-access)).

```sh
git clone https://github.com/robdevops/dalibot-gallery
cd dalibot-gallery
cp .env.example .env
chmod 600 .env
# Edit .env: GALLERY_TOKEN (openssl rand -base64 32), TELEGRAM_BOT_TOKEN,
# AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, and AWS_REGION / DALIBOT_TABLE /
# GALLERY_STORE_DAYS if the bot does not use the defaults.
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml logs -f
```

The gallery is now on `http://127.0.0.1:8080` of the host, and only there.
Open it and sign in with `GALLERY_TOKEN`. To reach it from anywhere else, put
a TLS [reverse proxy](#reverse-proxy) in front. The session cookie is marked
`Secure`, so the login page works over HTTPS, and over plain HTTP only on
`localhost` / `127.0.0.1`.

What `deploy/compose.yaml` sets up:

| | |
|---|---|
| Port | `127.0.0.1:8080` on the host only |
| User | uid/gid 10001, not root |
| Root filesystem | read-only; `/tmp` is a 16 MB tmpfs |
| Capabilities | all dropped, `no-new-privileges` |
| Memory | hard limit 512 MB; at most 256 processes/threads |
| Cache | named volume `cache` at `/var/cache/dalibot-gallery` (the only writable path) |
| Health | `gallery -healthcheck` every 30 s (the binary asks its own `/healthz`; the image has no curl) |

Without Compose, the same thing is:

```sh
docker build -f deploy/Dockerfile -t dalibot-gallery .
docker run -d --name dalibot-gallery --restart unless-stopped \
  --env-file .env \
  -p 127.0.0.1:8080:8080 \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --memory 512m --pids-limit 256 \
  -v dalibot-gallery-cache:/var/cache/dalibot-gallery \
  dalibot-gallery
```

The image sets `GALLERY_LISTEN=0.0.0.0:8080` and
`GALLERY_CACHE_DIR=/var/cache/dalibot-gallery`; do not set either in `.env`
when you use `docker run --env-file` (Compose overrides them for you). Write
values in `.env` without quotes: `docker run --env-file` and systemd pass quotes through literally.

The image is built with Go 1.27 on `golang:1.27-alpine` and runs on
`alpine:3` (Alpine 3.24 with libvips 8.18.2 when this was written). Alpine
packages libvips' heavier loaders (PDF, ImageMagick, HEIF, JPEG XL)
separately and the image installs none of them, so it is about 125 MB,
against more than 300 MB on Debian or Ubuntu. Both images can be replaced
with the build arguments `GO_IMAGE` and `BASE_IMAGE`, but the build and
runtime stages must stay on the same Alpine release (the binary links musl
and libvips dynamically) and the package names in the Dockerfile are Alpine's.

**What has and has not been tested.** The image was built and run in the
development sandbox only (Docker 29, x86-64):

- Built from this Dockerfile, unchanged, but with `GO_IMAGE` and `BASE_IMAGE`
  set to copies of `golang:1.27-alpine` and `alpine:3` that add the sandbox
  proxy's certificate, because the sandbox has no direct internet access.
  The default build arguments (the plain images from Docker Hub) have not
  been built end to end.
- The Go tests were run inside the build stage (Go 1.27.4, musl, libvips
  8.18.2), except the integration test, which needs moto and ran on the
  host only (Ubuntu 24.04, libvips 8.15.1).
- Run with the `docker run` flags above against the dev stack (a local
  DynamoDB stand-in and a fake Telegram): healthy, uid 10001, read-only root,
  no capabilities, thumbnails and originals served, 15 MiB of the 512 MiB
  in use after warming 240 thumbnails, clean exit on `docker stop`.
- `deploy/compose.yaml` was brought up and its client-address handling
  checked through the published port with an earlier Ubuntu-based image;
  it has not been run again with the Alpine image. The nginx, Caddy and
  Traefik examples were each run in front of that earlier container before
  `GALLERY_TRUSTED_PROXIES` existed; they have not been run again since,
  and the Traefik address in `deploy/traefik-labels.yaml` is a placeholder
  you must change.
- Not tested: real AWS, the real Telegram API, certificate issuance, any
  other machine or architecture, the unit under a running systemd.

`/healthz` answers 503 until the first read of the table has succeeded, so
the container is "starting" or "unhealthy" while AWS is unreachable or the
credentials are wrong; the log says why. Traefik does not route to a
container that is not healthy.

## Configuration

Everything is an environment variable. Every value is checked at start-up; a
mistake stops the gallery with exit status 2 and a message naming each
variable at fault. Secrets are never printed. `.env.example` lists them all.

| Variable | Default | Notes |
|---|---|---|
| `GALLERY_TOKEN` | (required) | The shared access token, at least 16 characters. Not needed only with `GALLERY_AUTH=off`. |
| `GALLERY_AUTH` | (unset) | `off` disables the gallery's own login, for use behind a proxy that authenticates every request. Setting both this and `GALLERY_TOKEN` is an error. |
| `TELEGRAM_BOT_TOKEN` | (required) | The bot's token. Used only for `getFile` and the file download. |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | For a local Bot API server or tests. |
| AWS credentials | standard chain | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, an instance or task role, and so on. |
| `AWS_REGION` | `us-east-2` | Falls back to `AWS_DEFAULT_REGION`. |
| `DALIBOT_TABLE` | `dalibot` | The bot's table. |
| `GALLERY_STORE_DAYS` | `90` | Must equal the bot's `DALIBOT_STORE_DAYS`. The table has no creation time, so the gallery dates each record as `ttl − GALLERY_STORE_DAYS × 86400`. A wrong value shifts every date by the difference. |
| `GALLERY_DEFAULT_COMMAND` | `dream` | Shown for records that carry no command. Display only. |
| `GALLERY_CHATS` | (empty) | Comma list of chat ids to show (negative for groups). Empty shows every chat. |
| `GALLERY_LISTEN` | `127.0.0.1:8080` | `host:port`. The container image sets `0.0.0.0:8080`. |
| `GALLERY_TRUSTED_PROXIES` | `127.0.0.0/8,::1` | Comma list of IP addresses or CIDR ranges of your reverse proxies, as the gallery sees them. Only from these peers is `X-Forwarded-For` believed (for the login limiter and the log). `none` trusts nobody. `deploy/compose.yaml` sets it to its network's gateway. See [Reverse proxy](#reverse-proxy). |
| `GALLERY_REFRESH` | `60s` | How often the table is re-read. A duration or a number of seconds, 5 s to 24 h. |
| `GALLERY_CACHE_DIR` | `$CACHE_DIRECTORY` or `./cache` | `$CACHE_DIRECTORY` is what systemd's `CacheDirectory=` sets. The container image sets `/var/cache/dalibot-gallery`. |
| `GALLERY_CACHE_MAX_MB` | `2048` | Size limit for originals plus thumbnails (minimum 16). Thumbnails are evicted above it; originals are not, so size it for your originals. See [Cache](#cache-and-pipeline_version). |
| `GALLERY_WEBP_QUALITY` | `76` | 1–100. Changing it changes every thumbnail URL. |
| `GALLERY_IMAGE_WORKERS` | CPUs available | Thumbnail pipelines that may run at once, 1–64. The default is the number of CPUs the process may use: the machine's, or fewer under a container CPU limit (for example `--cpus 2`) or `GOMAXPROCS`. |
| `GALLERY_WARM` | `120` | After each refresh, thumbnails for this many of the newest tiles are made in the background. `0` turns it off. |
| `GALLERY_DEBUG` | (unset) | `1` logs at debug level, including every image, asset and health request. Not validated. |

The binary takes no arguments apart from `-healthcheck` (exit 0 if the
running gallery's `/healthz` answers 200) and `-version`.

## AWS access

Give the gallery its own read-only identity. Do not reuse the bot's: the
bot's policy can write, and it has no `Scan`.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadOnlyOneTable",
      "Effect": "Allow",
      "Action": ["dynamodb:Scan", "dynamodb:GetItem"],
      "Resource": "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dalibot"
    }
  ]
}
```

Replace `REGION`, `ACCOUNT_ID` and the table name. The current code only
calls `Scan`, so `dynamodb:GetItem` can be left out. Add `kms:Decrypt` on the
key only if the table is encrypted with a customer-managed KMS key.

To create the user with the AWS CLI (as an administrator), save the policy
above as `gallery-policy.json` and run:

```sh
aws iam create-user --user-name dalibot-gallery
aws iam put-user-policy --user-name dalibot-gallery \
  --policy-name dalibot-gallery-read --policy-document file://gallery-policy.json
aws iam create-access-key --user-name dalibot-gallery
```

Put the `AccessKeyId` and `SecretAccessKey` that the last command prints in
`.env`. On EC2 or ECS, attach the same policy to a role
instead and leave the key variables unset.

The gallery scans the whole table every `GALLERY_REFRESH` and serves every
request from memory. The table is bounded by the bot's 90-day TTL, so this is
small, but each scan is billed as read capacity for the whole table; raise
`GALLERY_REFRESH` if that matters.

## Security model

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
`/healthz`, `/login` and the stylesheet `/app.css`, which the login page
uses. There are no accounts or roles: whoever has the token sees everything,
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
  proxy's address and shares one allowance of 5; see [Reverse proxy](#reverse-proxy).
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

## Reverse proxy

The gallery speaks plain HTTP and expects a proxy in front that terminates
TLS. Examples are in `deploy/`:

| File | For |
|---|---|
| `deploy/nginx.conf` | nginx on the host, proxying to `127.0.0.1:8080` |
| `deploy/Caddyfile` | Caddy on the host, proxying to `127.0.0.1:8080` |
| `deploy/traefik-labels.yaml` | Traefik's Docker provider; a Compose override: `docker compose -f deploy/compose.yaml -f deploy/traefik-labels.yaml up -d --build` |

Whatever proxy you use:

- **Do not remove, replace or duplicate the gallery's security headers.** The
  examples add only `Strict-Transport-Security`.
- **Do not cache at the proxy.** With the gallery's login on, images are
  sent with `Cache-Control: private, max-age=31536000, immutable`: the
  browser keeps them for a year, and a shared cache that follows the
  standard does not store them. (The plan specified `public`; `private` was
  chosen because the images are behind the login. Browser caching is the
  same.) With `GALLERY_AUTH=off` they are sent as `public`, and it is then up
  to you not to put a shared cache between the authenticating proxy and the visitors.
- **Set `X-Forwarded-For`, and tell the gallery which proxies to believe.**
  The limiter on token guesses counts per client address. The gallery uses
  the address of the TCP peer, unless that peer is listed in
  `GALLERY_TRUSTED_PROXIES`; then it reads `X-Forwarded-For` from the right
  and takes the first address that is not itself a trusted proxy. nginx's
  `$proxy_add_x_forwarded_for`, Caddy and Traefik all append the client
  address by default. Set the variable to the address your proxy connects
  from, as the gallery sees it:
  - Proxy and gallery on the same host, no container: nothing to do. The
    default trusts loopback.
  - `deploy/compose.yaml` with a proxy on the host: already set. The
    container sees the proxy's connections as coming from the gateway of
    the Compose network, which the file pins to `172.30.80.1`.
  - `docker run` with a proxy on the host: the gateway of the Docker network
    in use (`docker network inspect bridge`, usually `172.17.0.1`).
  - Traefik in Docker: Traefik's address on the shared network; see the
    comments in `deploy/traefik-labels.yaml`.

  List only the proxies themselves. Anything that can connect to the gallery
  from a trusted address can pick its own client address and so get around
  the per-client limit (the overall limit still applies) and write false
  addresses to the log. If the proxy is not listed, nothing breaks, but all
  visitors count as one client and share 5 wrong tokens per minute.
- Nothing else is needed: no WebSockets, no path rewriting, and the only
  request body is the login form.

## Running without a container

### libvips

The binary needs the libvips shared library at run time, version 8.14 or
later, and its headers plus a C compiler and pkg-config to build. Install the
newest libvips your distribution ships and keep it, libwebp and libjpeg-turbo
patched: they parse image data.

| System | Run time | To build as well | `vips` command |
|---|---|---|---|
| Debian 13+, Ubuntu 24.04+ | `apt install libvips42t64` | `apt install libvips-dev pkg-config gcc` | `apt install libvips-tools` |
| Debian 12 | `apt install libvips42` | same | same |
| Fedora | `dnf install vips` | `dnf install vips-devel pkgconf-pkg-config gcc` | `dnf install vips-tools` |
| Arch | `pacman -S libvips` | `pacman -S libvips pkgconf gcc` | included |
| Alpine | `apk add vips` | `apk add vips-dev pkgconf build-base` | `apk add vips-tools` |
| macOS (Homebrew) | `brew install vips` | `brew install vips pkg-config` | included |
| FreeBSD | `pkg install vips` | `pkg install vips pkgconf` | included |

Check what you have:

```sh
vips --version                 # e.g. vips-8.15.1
pkg-config --modversion vips   # e.g. 8.15.1 (needs the headers / -dev package)
```

The gallery also logs the libvips version at start-up, and `gallery -version`
prints it. Ubuntu 24.04 (libvips 8.15.1), Debian 13 (8.16.1) and Alpine 3.24 (8.18.2)
have been tested; Ubuntu 22.04 ships libvips 8.12, which is too old. A binary must be built on the
same distribution release it runs on (it is dynamically linked; on Alpine it
links musl).

### Building from source

Go 1.27 or later, libvips headers, gcc and pkg-config:

```sh
go build -trimpath -o dalibot-gallery ./cmd/gallery
./dalibot-gallery -version     # dalibot-gallery pipeline v1, libvips 8.15.1
```

To try it in a shell (with `.env` filled in):

```sh
set -a; . ./.env; set +a
./dalibot-gallery
```

### systemd

```sh
sudo install -m 0755 dalibot-gallery /usr/local/bin/dalibot-gallery
sudo install -d -m 0700 /etc/dalibot-gallery
sudo install -m 0600 .env /etc/dalibot-gallery/env
sudo install -m 0644 deploy/dalibot-gallery.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now dalibot-gallery
journalctl -u dalibot-gallery -f
```

The unit runs the gallery as a throw-away user (`DynamicUser`) with
`ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `NoNewPrivileges`, an
empty capability set, `MemoryMax=512M`,
`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` and
`SystemCallFilter=@system-service`, among others. The cache is
`/var/cache/dalibot-gallery` (`CacheDirectory=`); leave `GALLERY_CACHE_DIR`
unset. The unit does not set `MemoryDenyWriteExecute`, because some libvips
builds generate code at run time. A configuration error (exit status 2) is not restarted.

## Cache and PIPELINE_VERSION

The cache directory holds two kinds of file, named by the tile key:

- `<key>.jpg`: the original, exactly as Telegram served it. Each is
  downloaded once and kept until its record leaves the table, so it survives
  a file id that later stops working.
- `<key>-<size>-v<tag>.webp`: the thumbnails.

Files are written to a temporary name and renamed into place. After every
refresh, files whose record is no longer in the listing (expired, deleted, or
excluded by `GALLERY_CHATS`) are removed, as are thumbnails from another
pipeline version. When the directory exceeds `GALLERY_CACHE_MAX_MB`,
thumbnails are evicted, least recently used first. Originals are never
evicted for size: they go only when their record leaves the table.

**Size the limit for your originals.** Budget roughly 300 KB per image for
the original plus 55 KB for its two thumbnails; the default 2048 MB holds
about 5,700 images in full. Beyond that, thumbnails of the least recently
viewed images are regenerated when needed (about 60 ms of CPU each). If the
originals alone reach the limit (about 6,800 images at the default),
thumbnails are still kept in a reserve of one eighth of the limit (256 MB
by default, about 4,600 images' worth), so
the directory can grow to the originals plus that reserve. The gallery logs
one warning when this starts; raise `GALLERY_CACHE_MAX_MB` then.

`PipelineVersion` is a constant in `internal/imaging/imaging.go`, currently
`1`. It is part of every thumbnail's file name, URL (`?v=`) and ETag. The tag
is the version alone at quality 76, and for example `1q80` at
`GALLERY_WEBP_QUALITY=80`. Thumbnails are served as immutable for a year, so
anything that changes their bytes must change the tag: **bump
`PipelineVersion` whenever you change the size, crop or encoder settings in
the code.** Changing the quality variable does this by itself. Requests for an
old tag get a 404 rather than new bytes under an old URL.

The whole directory can be deleted at any time; everything is fetched and
generated again on demand (images whose file id has since died are then gone).

## Upgrading

```sh
git pull
docker compose -f deploy/compose.yaml up -d --build
```

Use `docker compose -f deploy/compose.yaml build --pull --no-cache` now and
then, even without a new release, to pick up security updates to libvips and
its codecs. The cache volume is kept across upgrades; originals are reused,
and thumbnails are regenerated only if the pipeline version changed. For the
systemd install, rebuild the binary, install it over the old one and
`systemctl restart dalibot-gallery`. There is no database and nothing to migrate.

## Development and tests

Requirements: Go 1.27, libvips with headers, and for the integration tests
the [moto](https://github.com/getmoto/moto) server
(`pip install 'moto[server]'`; the tests look for `/opt/moto/bin/moto_server`
or `$MOTO_SERVER` and skip if it is missing). Nothing in the tests or the dev
stack contacts AWS or Telegram.

```sh
go vet ./...
staticcheck ./...
go test -race ./...
go test ./internal/imaging -run '^$' -fuzz FuzzValidate -fuzztime 30s   # the JPEG header validator
```

- Unit tests cover decoding the bot's item shapes, ordering and cursors, the
  Telegram client against a fake server (allowlist, redaction, 429, failure
  cache, size cap), the imaging pipeline, the cache and the HTTP server.
- `cmd/gallery/integration_test.go` builds the real binary and runs it
  against moto and a fake Telegram API with about 150 seeded items in 3 chats.
- `testdata/telegram/` holds 12 real images exactly as Telegram served them
  (1024×1024 progressive JPEG, 142–348 KB). The imaging tests, the benchmark
  and the fake Telegram server all use them. `testdata/dynamodb/items.json`
  holds items in the bot's shapes.

**Dev stack.** One command runs moto, the seed, a fake Telegram API and the
real gallery:

```sh
scripts/dev-stack.sh                 # http://127.0.0.1:8080, control server on :8081
scripts/dev-stack.sh -port 9000 -auth=false
scripts/dev-stack.sh -h              # every option
```

When ready it prints one line of JSON with the URL and the token
(`dev-gallery-token-0123456789`). The control server is for tests:
`curl -X POST 'http://127.0.0.1:8081/add?n=3'` writes three new records (they
appear after the next refresh, 5 s in the dev stack), and
`curl http://127.0.0.1:8081/state` reports counts and any Telegram request
other than the two allowed ones (`"forbidden"` must be `[]`).

**End to end.** `e2e/` holds Playwright (Chromium) tests. They start the dev
stack themselves, on port 18480 unless `E2E_PORT` says otherwise, and need
Playwright with its Chromium installed: `cd e2e && npm test`.

**Container.** To run the image against the dev stack, start the stack, take
`AWS_ENDPOINT_URL_DYNAMODB` and `TELEGRAM_API_BASE` from its gallery
process's environment, and run the image with `--network host`, those two
variables, the dev token and bot token, `AWS_ACCESS_KEY_ID=testing`,
`AWS_SECRET_ACCESS_KEY=testing` and a free `GALLERY_LISTEN` port.

`CONTRACTS.md` documents the interfaces between the packages and the HTTP API.

## Measured performance

From [BENCH.md](BENCH.md), which has the per-image table, the caveats and the
commands to reproduce. Measured on 2026-11-12 on **one machine** (a 4-vCPU
virtual machine, Intel Xeon @ 2.30 GHz, Ubuntu 24.04, libvips 8.15.1, libwebp
1.3.2, libjpeg-turbo 2.1.5, govips v2.17.0, Go 1.24.7; the project has since
moved to Go 1.27 and govips v2.20.0, and the figures have not been measured
again) with the **12 real
Telegram images** in `testdata/telegram/`. These are not synthetic images,
but they are a small sample on a shared machine: treat the numbers as an
indication, not a guarantee.

| | 480 px | 240 px |
|---|---:|---:|
| Time per thumbnail, one at a time (mean) | 42.4 ms | 21.0 ms |
| Time per thumbnail, one at a time (median) | 42.4 ms | 20.3 ms |
| First call in the process | 52.4 ms | 19.9 ms |
| Bytes per thumbnail (mean) | 42,795 | 13,036 |
| Bytes per thumbnail (smallest – largest) | 24,810 – 64,170 | 7,226 – 21,186 |
| Throughput with 4 workers | 82.2 per second | 154.4 per second |

A whole tile (both thumbnails of one image) takes about 63 ms on one core,
and 4 workers make 54.0 tiles per second. Every call decodes the JPEG again,
so this is what the first request for an uncached tile costs, without the
Telegram download.

Next to the figures the project was planned on (measured earlier with a
different Go binding, bimg, on 13 Telegram images, also on 4 vCPU):

| | Plan (bimg, 13 images) | This build (govips, 12 images) |
|---|---:|---:|
| 480 px WebP thumbnail, cold | about 51–57 ms | 42.4 ms mean (52.4 ms for the first call) |
| 480 px WebP throughput, 4 cores | about 75 per second | 82.2 per second |
| Bytes per 480 px tile | about 44 KB | 42.8 KB mean |

Caveats, from BENCH.md:

- The machine was shared with other jobs during the run (load average about
  3), and times vary: an earlier run gave 44.3 ms and 22.1 ms per thumbnail
  and 70.2 / 145.7 per second with 4 workers. Byte sizes do not vary.
- One image misses the plan's 60 KB target at 480 px: `telegram-10.jpg` is
  64,170 bytes; the other eleven are 24,810–59,550 bytes. Quality 76 and
  effort 4 are fixed, so this is an accepted exception: the test names that
  one file, gives it a 65 KB ceiling, and fails if any other sample exceeds 60 KB.
- The table was recorded before the loader got `fail_on=error`. The bytes
  are identical. A rerun of the Go benchmarks afterwards gave 46.1 ms and
  23.3 ms per thumbnail and 74.6 / 145.8 per second, inside the spread above.
- A cold tile also pays for one full check decode of the original (about
  12–13 ms on the same machine) before the thumbnails are made.
- Not measured there: the Telegram download, the disk cache, HTTP serving and memory.

Separately, the integration test measures a 240 px tile served from a warm
cache over loopback HTTP and fails above a 5 ms median; one recorded run of
300 requests gave a median of 220 µs, p95 401 µs, maximum 1.2 ms.

### In the browser

From the end-to-end tests (`e2e/perf.spec.js`): headless Chromium against the
dev stack on the same machine, empty browser cache, thumbnails already in the
gallery's cache. The page, script, stylesheet and first JSON page are 43 kB
together; every tile was fetched at 480 px, about 43 kB each.

| Profile | Page + tiles in view | Whole first visit, throttled to 10 Mbit/s and 40 ms | Whole first visit, loopback |
|---|---:|---:|---:|
| 1440×900, 1x and 2x | 884 kB (20 tiles) | 1,767 kB (40 images) | 2,579 kB (59 images) |
| 1920×1080 | 1,099 kB (24 tiles) | **2,131 kB (48 images)** | 2,579 kB (59 images) |
| iPhone 15 profile | 422 kB (8 tiles) | 884 kB (20 images) | 1,687 kB (38 images) |

- **The plan's "first screen transfers at most 2 MB" holds for the tiles in
  view on every profile, but not for everything a first visit downloads at
  1920×1080.** There Chromium's lazy-loading margin fetches 48 tiles, 2.13 MB
  in all, 6% over. The test asserts the first column everywhere and the
  second column at 1440×900 and on the phone profile; the 1920×1080 figure
  is reported, not asserted.
- With no network latency at all (loopback) Chromium fetches every lazy image
  of the first batch at once, so those totals are larger; they are reported only.
- A second visit transfers 33 kB and no image bytes.
- A tile from a warm cache, 400 requests over 20 tiles on loopback: median
  0.18–0.19 ms, p95 0.34–0.40 ms, maximum 0.9 ms for both sizes (the test
  fails above 5 ms). A cold tile was not timed end to end.
- Chromium only. The phone runs are Chromium with a device profile, not
  Safari, and the network is simulated.

## Limitations and out of scope

- **Images disappear with the bot's TTL.** When DynamoDB expires a record
  (90 days after its last write by default), its tiles and cached files go too.
- **Read-only.** No uploads, likes, comments, search, or writing to the table.
- **One shared token.** No users or roles, and no per-session sign-out.
  Many addresses failing together can block sign-ins for a while; see
  [Security model](#security-model).
- **DynamoDB only.** The bot's self-hosted SQLite mode is not supported (the
  listing sits behind an interface so a reader could be added).
- **JPEG originals only**, which is what Telegram serves for photos. Anything
  else shows the placeholder. Photo-variation albums are not stored by the
  bot, so they cannot appear.
- **WebP thumbnails only**; no AVIF or JPEG XL.
- "Newest" means most recently drawn: adding a style to an old message brings
  its tiles back to the top. The date is derived from the TTL, so it is only
  right if `GALLERY_STORE_DAYS` matches the bot.
- With `GALLERY_AUTH=off`, image responses are cacheable as `public`; see [Reverse proxy](#reverse-proxy).
- Responses that Go's HTTP server writes itself for malformed requests carry
  no security headers; see [Security model](#security-model).
- Rebuild the container image often to pick up security updates to Alpine,
  libvips and its codecs.
- The whole table is scanned on every refresh and the listing is held in
  memory; this suits a table bounded by a TTL, not an unbounded one.

## Licence

MIT; see [LICENSE](LICENSE).
