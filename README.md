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

The image is built on Ubuntu 24.04 (libvips 8.15.1, the version the tests ran
against); the Go toolchain is copied from the official `golang:1.24` image.
Both base images can be replaced with the build arguments `GO_IMAGE` and
`BASE_IMAGE`, but the build and runtime stages must stay on the same
distribution release and the package names in the Dockerfile are Ubuntu's.
The image is about 330 MB, nearly all of it libvips' dependencies.

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
| `GALLERY_REFRESH` | `60s` | How often the table is re-read. A duration or a number of seconds, 5 s to 24 h. |
| `GALLERY_CACHE_DIR` | `$CACHE_DIRECTORY` or `./cache` | `$CACHE_DIRECTORY` is what systemd's `CacheDirectory=` sets. The container image sets `/var/cache/dalibot-gallery`. |
| `GALLERY_CACHE_MAX_MB` | `2048` | Thumbnails are evicted above this size (minimum 16). See [Cache](#cache-and-pipeline_version). |
| `GALLERY_WEBP_QUALITY` | `76` | 1–100. Changing it changes every thumbnail URL. |
| `GALLERY_IMAGE_WORKERS` | number of CPUs | Thumbnail pipelines that may run at once, 1–64. |
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

Comparisons are constant-time. `/login` allows 5 failed attempts per client
address per minute and 30 per minute overall, then answers 429. Everything
needs authentication except `/healthz`, `/login`, `/app.js` and `/app.css`.
There are no accounts or roles: whoever has the token sees everything, and
the only way to sign everyone out is to change the token.

With `GALLERY_AUTH=off` the gallery has no login at all and relies entirely
on the proxy in front of it. It refuses to start with neither a token nor
`GALLERY_AUTH=off`.

**Headers** on every response:

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
Telegram were to serve them.

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
- **Do not cache at the proxy.** Images are sent with
  `Cache-Control: public, max-age=31536000, immutable` so that browsers keep
  them, but they are behind the login. A shared cache in the proxy or a CDN
  would hand them to anyone who has the URL.
- **Set `X-Forwarded-For`.** When the connection comes from a loopback or
  private address, the login limiter counts by the last entry of
  `X-Forwarded-For`, which is the one your proxy appended. nginx's
  `$proxy_add_x_forwarded_for`, Caddy and Traefik all do this by default. A
  consequence: if the gallery is reachable directly from a private network
  without a proxy, a client there can vary that header to avoid the
  per-client limit; the overall limit of 30 per minute still applies.
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
prints it. Only Ubuntu 24.04 with libvips 8.15.1 has been tested; Ubuntu
22.04 ships libvips 8.12, which is too old. A binary must be built on the
same distribution release it runs on (it is dynamically linked; on Alpine it
links musl).

### Building from source

Go 1.24.7 or later in the 1.24 series, libvips headers, gcc and pkg-config:

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
evicted for size, so the directory can exceed the limit if the originals
alone are larger; budget roughly 300 KB per image for originals.

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

Requirements: Go 1.24, libvips with headers, and for the integration tests
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
1.3.2, libjpeg-turbo 2.1.5, govips v2.17.0, Go 1.24.7) with the **12 real
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
  64,170 bytes; the other eleven are 24,810–59,550 bytes.
- Not measured there: the Telegram download, the disk cache, HTTP serving and memory.

Separately, the integration test measures a 240 px tile served from a warm
cache over loopback HTTP and fails above a 5 ms median; one recorded run of
300 requests gave a median of 220 µs, p95 401 µs, maximum 1.2 ms.

## Limitations and out of scope

- **Images disappear with the bot's TTL.** When DynamoDB expires a record
  (90 days after its last write by default), its tiles and cached files go too.
- **Read-only.** No uploads, likes, comments, search, or writing to the table.
- **One shared token.** No users or roles, and no per-session sign-out.
  HTTP Basic attempts are not rate-limited; only `/login` is.
- **DynamoDB only.** The bot's self-hosted SQLite mode is not supported (the
  listing sits behind an interface so a reader could be added).
- **JPEG originals only**, which is what Telegram serves for photos. Anything
  else shows the placeholder. Photo-variation albums are not stored by the
  bot, so they cannot appear.
- **WebP thumbnails only**; no AVIF or JPEG XL.
- "Newest" means most recently drawn: adding a style to an old message brings
  its tiles back to the top. The date is derived from the TTL, so it is only
  right if `GALLERY_STORE_DAYS` matches the bot.
- Image responses are cacheable as `public`; see [Reverse proxy](#reverse-proxy).
- The container image takes libvips from Ubuntu's `universe` component, whose
  security updates are not guaranteed by Canonical without Ubuntu Pro. Rebuild
  often, or set `BASE_IMAGE` and adapt the package names for another distribution.
- The whole table is scanned on every refresh and the listing is held in
  memory; this suits a table bounded by a TTL, not an unbounded one.

## Licence

MIT; see [LICENSE](LICENSE).
