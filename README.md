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
- One Go binary with the page, script and stylesheet embedded (under 16 KB of
  JavaScript and CSS, no framework, no build step). It links libvips through cgo,
  so libvips must be installed wherever it runs; the container image takes care of that.

| Grid | Viewer |
|---|---|
| ![The grid on a desktop](docs/screenshots/desktop-grid.jpg) | ![The viewer on a desktop](docs/screenshots/desktop-lightbox.jpg) |

Screenshots from the end-to-end tests, which run the real binary against a
local stand-in for DynamoDB and Telegram that serves the sample images in
`testdata/telegram/`. Phone versions are in `docs/screenshots/`.

Contents: [Quick start](#quick-start-container) ·
[Configuration](#configuration) · [AWS access](#aws-access) ·
[Security model](docs/security.md) · [Reverse proxy](#reverse-proxy) ·
[Running without a container](#running-without-a-container) ·
[Cache](docs/cache.md) · [Upgrading](#upgrading) ·
[Development and tests](docs/development.md) · [Performance](docs/performance.md) ·
[Limitations](docs/limitations.md)

## Quick start (container)

You need Docker with Compose, the bot's Telegram token, and a read-only AWS
key for the bot's table (see [docs/aws-access.md](docs/aws-access.md)).

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

How the image is built, what has and has not been tested, and the health
check are described in [docs/container.md](docs/container.md).

## Configuration

Everything is an environment variable. Every value is checked at start-up; a
mistake stops the gallery with exit status 2 and a message naming each
variable at fault. Secrets are never printed. `.env.example` lists them all.

These are the ones to look at before the first start:

| Variable | Default | Notes |
|---|---|---|
| `GALLERY_TOKEN` | (required) | The shared access token, at least 16 characters. Not needed only with `GALLERY_AUTH=off` (see note below). |
| `TELEGRAM_BOT_TOKEN` | (required) | The bot's token. Used only for `getFile` and the file download. |
| AWS credentials | standard chain | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, an instance or task role, and so on (see [docs/aws-access.md](docs/aws-access.md)). |
| `AWS_REGION` | `ap-southeast-1` | Falls back to `AWS_DEFAULT_REGION`. |
| `DALIBOT_TABLE` | `dalibot` | The bot's AWS DynamoDB table. |
| `GALLERY_STORE_DAYS` | `90` | Must equal the bot's `DALIBOT_STORE_DAYS`. The table has no creation time, so the gallery dates each record as `ttl − GALLERY_STORE_DAYS × 86400`. A wrong value shifts every date by the difference. Records with a `created` attribute (pictures imported by forwarding them to the bot) use that date instead. |
| `GALLERY_LISTEN` | `127.0.0.1:8080` | `host:port`. The container image sets `0.0.0.0:8080`. |
| `GALLERY_TRUSTED_PROXIES` | `127.0.0.0/8,::1` | Comma list of IP addresses or CIDR ranges of your reverse proxies, as the gallery sees them. Only from these peers is `X-Forwarded-For` believed (for the login limiter and the log). `none` trusts nobody. `deploy/compose.yaml` sets it to its network's gateway. See [docs/reverse-proxy.md](docs/reverse-proxy.md). |

Note on `GALLERY_AUTH=off`: it turns the gallery's own login off, so anything
that can reach the gallery's port sees every image. It is meant for a
reverse proxy that does its own sign-in in front. Without `GALLERY_TOKEN` the
gallery refuses to start unless this is set, so that it is never left open
by accident. It is described in [docs/configuration.md](docs/configuration.md).

The rest have defaults that suit most installations: turning the login off,
which chats to show, the refresh interval, cache size, thumbnail quality and
workers, and debugging. They are in [docs/configuration.md](docs/configuration.md).

## AWS access

The gallery needs its own read-only AWS identity for the bot's table: an IAM
policy allowing `dynamodb:Scan` on that one table, attached to a new user
whose access key goes in `.env`. Do not reuse the bot's identity.

[docs/aws-access.md](docs/aws-access.md) has the policy, step-by-step
instructions for the AWS console and the AWS CLI, where to put the key, and
troubleshooting.

## Reverse proxy

The gallery speaks plain HTTP and expects a proxy in front that terminates
TLS. Examples are in `deploy/`:

| File | For |
|---|---|
| `deploy/nginx.conf` | nginx on the host, proxying to `127.0.0.1:8080` |
| `deploy/Caddyfile` | Caddy on the host, proxying to `127.0.0.1:8080` |
| `deploy/traefik-labels.yaml` | Traefik's Docker provider; a Compose override: `docker compose -f deploy/compose.yaml -f deploy/traefik-labels.yaml up -d --build` |

Whatever proxy you use, there are a few rules to follow: leave the gallery's
security headers alone, do not cache at the proxy, and tell the gallery
which proxies to believe for the client address. They are in
[docs/reverse-proxy.md](docs/reverse-proxy.md).

### No password for visitors (nginx)

To let visitors in without a token while the gallery still refuses anyone
who does not come through nginx, keep `GALLERY_TOKEN` set and have nginx
present it on every request. The gallery accepts the token as the password
of HTTP Basic auth with any user name.

1. Encode `nginx:` followed by the token:

   ```sh
   printf 'nginx:%s' 'YOUR_GALLERY_TOKEN' | openssl base64 -A
   ```

2. Put the result in a file only root can read, for example
   `/etc/nginx/gallery-auth.conf` (`chmod 600`):

   ```nginx
   proxy_set_header Authorization "Basic PASTE_THE_BASE64_HERE";
   ```

3. In `deploy/nginx.conf`, inside `location /`, add
   `include /etc/nginx/gallery-auth.conf;`, and next to that location add
   `location = /login { return 302 /; }` so the unused login page is not shown.
4. `sudo nginx -t && sudo systemctl reload nginx`.

The separate file is only there to keep the token private: site files under
`sites-available` are usually world-readable, and base64 is an encoding, not
encryption. You can put the `proxy_set_header Authorization` line straight
into your site file's `location /` instead, as long as you then make that
file readable by root only (`sudo chmod 600`); nginx reads its configuration
as root.

Visitors now see the gallery straight away; a request that reaches port 8080
without passing through nginx still gets 401. nginx replaces any
`Authorization` header a visitor sends.

This makes the gallery public to everyone who can reach nginx. To limit
that, restrict the `server` block with `allow` / `deny`, or put nginx's own
`auth_basic` or SSO in front. `GALLERY_AUTH=off` also removes the password,
but then nothing protects the gallery's port itself, and images are sent as
`Cache-Control: public`.

## Running without a container

### libvips

The gallery is distributed as source only, so running it without a container
means building it, and building needs more than the libvips run-time
library: the libvips headers, a C compiler and pkg-config. Install these
first. Each command also brings in the run-time library, version 8.14 or
later required.

| System | Install before building |
|---|---|
| Debian, Ubuntu 24.04+ | `sudo apt install libvips-dev pkg-config gcc` |
| Fedora | `sudo dnf install vips-devel pkgconf-pkg-config gcc` |
| Arch | `sudo pacman -S libvips pkgconf gcc` |
| Alpine | `apk add vips-dev pkgconf build-base` |
| macOS (Homebrew) | `brew install vips pkg-config` |
| FreeBSD | `pkg install vips pkgconf` |

You also need Go 1.27 or later. An older Go (1.21 or later) downloads the
1.27 toolchain by itself on the first build if it can reach the internet.

Install the newest libvips your distribution ships and keep it, libwebp and
libjpeg-turbo patched: they parse image data.

The run-time library alone (`libvips42t64` on Debian 13 and Ubuntu 24.04,
`libvips42` on Debian 12, `vips` on Fedora and Alpine) is enough only on a
machine that receives a binary built elsewhere on the same distribution
release. The optional `vips` command comes from `libvips-tools` (Debian,
Ubuntu) or `vips-tools` (Fedora, Alpine) and is included elsewhere.

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

With the packages from [libvips](#libvips) and Go installed, from the
repository root:

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

Do these in order, from the repository root. The first step makes the
`dalibot-gallery` binary that the second one installs; without it `install`
fails with `cannot stat 'dalibot-gallery'`.

1. Install the build requirements under [libvips](#libvips) (on Debian or
   Ubuntu: `sudo apt install libvips-dev pkg-config gcc`). Without them the
   build stops with `exec: "pkg-config": executable file not found`. Then
   build and check the binary:

   ```sh
   go build -trimpath -o dalibot-gallery ./cmd/gallery
   ./dalibot-gallery -version
   ```

2. Make sure `.env` exists and is filled in (`cp .env.example .env`, then
   edit it). Leave `GALLERY_CACHE_DIR` unset.

3. Install the binary, the settings and the unit:

   ```sh
   sudo install -m 0755 dalibot-gallery /usr/local/bin/dalibot-gallery
   sudo install -d -m 0700 /etc/dalibot-gallery
   sudo install -m 0600 .env /etc/dalibot-gallery/env
   sudo install -m 0644 deploy/dalibot-gallery.service /etc/systemd/system/
   ```

4. Start it and watch the log:

   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now dalibot-gallery
   journalctl -u dalibot-gallery -f
   ```

If the service was enabled before the binary was in place, finish steps 1
to 3 and then run `sudo systemctl restart dalibot-gallery`.

The unit runs the gallery as a throw-away user (`DynamicUser`) with
`ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `NoNewPrivileges`, an
empty capability set, `MemoryMax=512M`,
`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` and
`SystemCallFilter=@system-service`, among others. The cache is
`/var/cache/dalibot-gallery` (`CacheDirectory=`); leave `GALLERY_CACHE_DIR`
unset. The unit does not set `MemoryDenyWriteExecute`, because some libvips
builds generate code at run time. A configuration error (exit status 2) is not restarted.

## Upgrading

There is no database and nothing to migrate. The cache is kept across
upgrades: originals are reused, and thumbnails are regenerated only if the
pipeline version changed.

**Container:**

```sh
git pull
docker compose -f deploy/compose.yaml up -d --build
```

Use `docker compose -f deploy/compose.yaml build --pull --no-cache` now and
then, even without a new release, to pick up security updates to libvips and
its codecs.

**systemd:** the running service uses the binary in `/usr/local/bin`, so new
code, including new defaults, takes effect only after you rebuild, reinstall
and restart. From the repository root:

```sh
git pull
go build -trimpath -o dalibot-gallery ./cmd/gallery
./dalibot-gallery -version
sudo install -m 0755 dalibot-gallery /usr/local/bin/dalibot-gallery
sudo systemctl restart dalibot-gallery
journalctl -u dalibot-gallery -n 20
```

The first log line prints the settings in effect (secrets are redacted), so
you can check a changed default there. If `deploy/dalibot-gallery.service`
changed, install it again and run `sudo systemctl daemon-reload` before the
restart. If you changed `.env`, install it again as `/etc/dalibot-gallery/env`.
After a system update of libvips, rebuild as well.

## More documentation

| Document | What it covers |
|---|---|
| [docs/aws-access.md](docs/aws-access.md) | The read-only IAM policy, creating the user and key, troubleshooting |
| [docs/configuration.md](docs/configuration.md) | The settings beyond the essentials above |
| [docs/container.md](docs/container.md) | How the image is built, what has been tested, the health check |
| [docs/reverse-proxy.md](docs/reverse-proxy.md) | Rules for any proxy: security headers, caching, client addresses and `GALLERY_TRUSTED_PROXIES` |
| [docs/security.md](docs/security.md) | What the browser can and cannot see, authentication, headers, the Telegram allowlist, image handling |
| [docs/cache.md](docs/cache.md) | The disk cache, its size limit, eviction and `PIPELINE_VERSION` |
| [docs/development.md](docs/development.md) | Running the tests, the local dev stack, the browser tests |
| [docs/performance.md](docs/performance.md) | Measured thumbnail timings and page weight |
| [docs/limitations.md](docs/limitations.md) | Known limitations and what is out of scope |

## Licence

[MIT](LICENSE)
