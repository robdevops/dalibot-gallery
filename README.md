# dalibot-gallery

A small, self-hosted web gallery for the images drawn by
[dalibot](https://github.com/robdevops/dalibot). It reads the bot's records,
from its DynamoDB table or from the SQLite database of a self-hosted bot,
and shows every image as an edge-to-edge grid of square tiles, newest first,
with infinite scroll and a full-size viewer.

- One tile per generated image (every style of every record is its own tile).
- Thumbnails are WebP, centre-cropped squares at 240 px and 480 px, made with
  libvips and cached on disk. The full-size image is the original Telegram JPEG, unchanged.
- The browser only ever talks to the gallery. Only the gallery reads the
  bot's records (from AWS, or from the bot's SQLite file) and talks to
  Telegram (`getFile` and the file download, nothing else).
- The gallery never writes to the table or to the database file, and never
  changes the bot. An admin can [hide](docs/admin.md) images from the
  gallery; that is the gallery's own list, kept on its own disk.
- One Go binary with the page, script and stylesheet embedded (under 20 KB of
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
[SQLite](#a-self-hosted-bot-sqlite) ·
[Security model](docs/security.md) · [Reverse proxy](#reverse-proxy) ·
[Running without a container](#running-without-a-container) ·
[Hiding images](docs/admin.md) · [Cache](docs/cache.md) · [Upgrading](#upgrading) ·
[Development and tests](docs/development.md) · [Performance](docs/performance.md) ·
[Limitations](docs/limitations.md)

## Quick start (container)

You need Docker with Compose, the bot's Telegram token, and a read-only AWS
key for the bot's table (see [docs/aws-access.md](docs/aws-access.md)). If
your bot is self-hosted and keeps its records in SQLite, you need no AWS key:
follow these steps with the differences under
[A self-hosted bot (SQLite)](#a-self-hosted-bot-sqlite).

```sh
git clone https://github.com/robdevops/dalibot-gallery
cd dalibot-gallery
cp .env.example .env
chmod 600 .env
# Edit .env: GALLERY_TOKEN (openssl rand -base64 32) or GALLERY_PUBLIC=1,
# TELEGRAM_BOT_TOKEN, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, and AWS_REGION /
# DALIBOT_TABLE / GALLERY_STORE_DAYS if the bot does not use the defaults.
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml logs -f
```

The gallery is now on `http://127.0.0.1:8080` of the host, and only there.
Open it and sign in with `GALLERY_TOKEN` (with `GALLERY_PUBLIC=1` there is
nothing to sign in to; see [Who can view](#who-can-view-and-who-can-administer)).
To reach it from anywhere else, put a TLS [reverse proxy](#reverse-proxy) in
front. The session cookie is marked `Secure`, so the login page works over
HTTPS, and over plain HTTP only on `localhost` / `127.0.0.1`.

What `deploy/compose.yaml` sets up:

| | |
|---|---|
| Port | `127.0.0.1:8080` on the host only |
| User | uid/gid 10001, not root |
| Root filesystem | read-only; `/tmp` is a 16 MB tmpfs |
| Capabilities | all dropped, `no-new-privileges` |
| Memory | hard limit 512 MB; at most 256 processes/threads |
| Cache | named volume `cache` at `/var/cache/dalibot-gallery`; can be deleted at any time |
| State | named volume `state` at `/var/lib/dalibot-gallery`: the list of images an admin has [hidden](docs/admin.md); keep it. With the cache, the only writable paths |
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
  -v dalibot-gallery-state:/var/lib/dalibot-gallery \
  dalibot-gallery
```

The image sets `GALLERY_LISTEN=0.0.0.0:8080`,
`GALLERY_CACHE_DIR=/var/cache/dalibot-gallery` and
`GALLERY_STATE_DIR=/var/lib/dalibot-gallery`; do not set them in `.env`
when you use `docker run --env-file` (Compose overrides them for you). Write
values in `.env` without quotes: `docker run --env-file` and systemd pass quotes through literally.

How the image is built, what has and has not been tested, and the health
check are described in [docs/container.md](docs/container.md).

### A self-hosted bot (SQLite)

A dalibot that runs on your own machine keeps its records in a SQLite file
(`var/dalibot.db` in the bot's directory) instead of DynamoDB. To show
those, on the same machine:

1. In `.env`, leave out every AWS line and add:

   ```sh
   GALLERY_SOURCE=sqlite
   ```

2. Let the bot's group read the database (the bot creates it for its own
   user only):

   ```sh
   sudo chgrp dalibot /opt/dalibot/var /opt/dalibot/var/dalibot.db
   sudo chmod g+rx /opt/dalibot/var
   sudo chmod g+r /opt/dalibot/var/dalibot.db
   ```

   (`dalibot` is the bot's group; see
   [docs/sqlite.md](docs/sqlite.md#permissions).)

3. In `deploy/compose.sqlite.yaml`, set the bot's `var` directory and the id
   of the bot's group, then start with both files:

   ```sh
   docker compose -f deploy/compose.yaml -f deploy/compose.sqlite.yaml up -d --build
   ```

The directory is mounted read-only, and the gallery opens the file
read-only: it never writes to it. [docs/sqlite.md](docs/sqlite.md) has the
details, `docker run` and systemd, and what happens while the bot is
writing or the file is missing.

## Configuration

Everything is an environment variable. Every value is checked at start-up; a
mistake stops the gallery with exit status 2 and a message naming each
variable at fault. Secrets are never printed. `.env.example` lists them all.

These are the ones to look at before the first start:

| Variable | Default | Notes |
|---|---|---|
| `GALLERY_TOKEN` | (this or `GALLERY_PUBLIC=1`) | The shared token for viewing, at least 16 characters. With it set, nobody sees anything without signing in. |
| `GALLERY_PUBLIC` | (unset) | `1` lets anyone who can reach the gallery view it, with no login. Set this or `GALLERY_TOKEN`, never both. |
| `GALLERY_ADMIN_TOKEN` | (unset) | Optional. A second token, different from `GALLERY_TOKEN` and at least 16 characters, that signs in as an admin at the same `/login`. Without it there are no admin features. |
| `TELEGRAM_BOT_TOKEN` | (required) | The bot's token. Used only for `getFile` and the file download. |
| `GALLERY_SOURCE` | `dynamodb` | Where the bot's records are: `dynamodb` (the bot's table in AWS) or `sqlite` (the database file of a self-hosted bot). Only the chosen source's settings are read. |
| AWS credentials | standard chain | With `dynamodb` only. `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, an instance or task role, and so on (see [docs/aws-access.md](docs/aws-access.md)). |
| `AWS_REGION` | `ap-southeast-1` | With `dynamodb` only. Falls back to `AWS_DEFAULT_REGION`. |
| `DALIBOT_TABLE` | `dalibot` | With `dynamodb` only. The bot's AWS DynamoDB table. |
| `GALLERY_SQLITE_PATH` | (required with `sqlite`) | With `sqlite` only. The bot's database file (its `DALIBOT_DB`). Opened read-only. See [docs/sqlite.md](docs/sqlite.md). |
| `GALLERY_STORE_DAYS` | `90` | Must equal the bot's `DALIBOT_STORE_DAYS`. The table has no creation time, so the gallery dates each record as `ttl − GALLERY_STORE_DAYS × 86400`. A wrong value shifts every date by the difference. Records with a `created` attribute (pictures imported by forwarding them to the bot) use that date instead. With `sqlite` the date is the time the row was last written, and this setting only decides when a row counts as expired. |
| `GALLERY_LISTEN` | `127.0.0.1:8080` | `host:port`. The container image sets `0.0.0.0:8080`. |
| `GALLERY_TRUSTED_PROXIES` | `127.0.0.0/8,::1` | Comma list of IP addresses or CIDR ranges of your reverse proxies, as the gallery sees them. Only from these peers is `X-Forwarded-For` believed (for the login limiter and the log). It grants no access. `none` trusts nobody. `deploy/compose.yaml` sets it to its network's gateway. See [docs/reverse-proxy.md](docs/reverse-proxy.md). |

The rest have defaults that suit most installations: which chats to show,
the refresh interval, cache size, thumbnail quality and workers, and
debugging. They are in [docs/configuration.md](docs/configuration.md).

### Who can view and who can administer

These are two separate questions, each answered by one setting.

**Who can view.** Choose one:

- `GALLERY_TOKEN=<a long random string>`: only people who have the token.
  They sign in at `/login`; scripts can send it as the HTTP Basic password.
- `GALLERY_PUBLIC=1`: anyone who can reach the gallery, with no login.

The gallery refuses to start with neither, so it is never left open by
accident, and with both, because they contradict each other.

**Who can administer.** Nobody, unless you set `GALLERY_ADMIN_TOKEN`. It is a
second token, used at the same `/login` page (or as the Basic password); the
session it gives can view as well. In a public gallery the page has a small
"Admin" link at its foot that leads there, and "Sign out" next to it once
signed in.

A public gallery with an admin:

```sh
GALLERY_PUBLIC=1
GALLERY_ADMIN_TOKEN=<openssl rand -base64 32>
```

In a public gallery a visitor is never sent a Telegram chat id, and image
responses may be stored by shared caches (`Cache-Control: public`). Details
are in [docs/security.md](docs/security.md).

**`GALLERY_AUTH` is gone.** Earlier versions had `GALLERY_AUTH=off` for a
gallery behind a proxy. If the variable is still set, to anything, the
gallery stops at start-up and says so. Remove it, and set `GALLERY_PUBLIC=1`
if the gallery is meant to be viewable without a login. See
[Upgrading](#upgrading).

### Hiding images

An admin can hide an image: a trash can in the viewer removes it from the gallery for everyone at once, and a page
of hidden images (linked at the foot of the gallery) puts it back. Hiding
never touches the bot's data: the gallery keeps its own list of hidden
images in `GALLERY_STATE_DIR`, which, unlike the cache, must be kept. To
delete a picture for good, use the bot's `#delete`. See
[docs/admin.md](docs/admin.md).

## AWS access

This applies to `GALLERY_SOURCE=dynamodb`, the default. With
`GALLERY_SOURCE=sqlite` the gallery needs no AWS identity and reads no AWS
setting.

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

### A public gallery behind a proxy

To let visitors in without a password, set `GALLERY_PUBLIC=1` (and leave
`GALLERY_TOKEN` unset). The proxy needs nothing special: the example files
work as they are. The gallery then serves everyone who can reach it, so:

- Keep the gallery's own port away from anyone who should not see it. The
  default `GALLERY_LISTEN` is loopback, and the Compose file publishes the
  port on `127.0.0.1` only.
- To limit who can view, do it at the proxy (`allow` / `deny`, its own
  password or SSO), or use `GALLERY_TOKEN` instead of `GALLERY_PUBLIC`. If
  the proxy asks for its own password and you set `GALLERY_ADMIN_TOKEN`,
  also stop the proxy from passing that password on (nginx:
  `proxy_set_header Authorization "";`, see `deploy/nginx.conf`), so the
  gallery does not read it as a guess at the admin token (see
  [docs/reverse-proxy.md](docs/reverse-proxy.md)).
- The proxy must not present a token on visitors' behalf, and listing it in
  `GALLERY_TRUSTED_PROXIES` gives it no access: that setting only says whose
  `X-Forwarded-For` to believe.
- Image responses are `Cache-Control: public` in this mode, so a caching
  proxy or CDN may store them. Pages and the listing are always `no-store`.

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

The C compiler also builds the SQLite library that is compiled into the
binary; no SQLite package needs to be installed.

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
   edit it). Leave `GALLERY_CACHE_DIR` and `GALLERY_STATE_DIR` unset.

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

**The service reads `/etc/dalibot-gallery/env`, not the `.env` in your
checkout.** Step 3 copies `.env` there once; later edits to `.env` change
nothing. From then on, edit the live file and restart:

```sh
sudoedit /etc/dalibot-gallery/env
sudo systemctl restart dalibot-gallery
```

You can delete the checkout's `.env` afterwards so there is only one copy.

The unit runs the gallery as a throw-away user (`DynamicUser`) with
`ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `NoNewPrivileges`, an
empty capability set, `MemoryMax=512M`,
`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` and
`SystemCallFilter=@system-service`, among others. The cache is
`/var/cache/dalibot-gallery` (`CacheDirectory=`) and the list of hidden
images is in `/var/lib/dalibot-gallery` (`StateDirectory=`); leave
`GALLERY_CACHE_DIR` and `GALLERY_STATE_DIR` unset. The unit does not set `MemoryDenyWriteExecute`, because some libvips
builds generate code at run time. A configuration error (exit status 2) is not restarted.

For a self-hosted bot's SQLite database, the unit has two commented lines
that give the gallery's user the bot's group and make the bot's directory
visible; see [docs/sqlite.md](docs/sqlite.md#systemd).

## Upgrading

The gallery has no database of its own and nothing to migrate. The cache is kept across
upgrades: originals are reused, and thumbnails are regenerated only if the
pipeline version changed. The one file of state, the list of hidden images,
is kept as well.

**If you used `GALLERY_AUTH`.** The variable has been removed, and a gallery
that still has it set, to any value, stops at start-up with a message saying
so, rather than start with a different meaning. What to do:

- `GALLERY_AUTH=off` (no login in the gallery, a proxy in front): remove the
  line and add `GALLERY_PUBLIC=1`. Viewing still needs no login. If your
  proxy did its own sign-in, it still does; the gallery itself is open to
  whatever can reach its port, as it was. If that sign-in is the proxy's
  own Basic password and you add `GALLERY_ADMIN_TOKEN`, add
  `proxy_set_header Authorization "";` (or your proxy's equivalent) too.
- nginx presenting `GALLERY_TOKEN` for every visitor (the old "no password
  for visitors" recipe): that still works, but is no longer the way. Remove
  the `Authorization` line (and `/etc/nginx/gallery-auth.conf`) from nginx,
  unset `GALLERY_TOKEN` and set `GALLERY_PUBLIC=1`. A visitor is then never
  sent a chat id, which the token-holding proxy could not prevent.
- `GALLERY_AUTH=token` or an empty value: remove the line. `GALLERY_TOKEN`
  alone has always meant that.

**SQLite source.** New: `GALLERY_SOURCE=sqlite` reads a self-hosted bot's
database file. Nothing changes for an existing installation: the default is
still DynamoDB, with the same settings. Building from source now also
compiles SQLite into the binary, with the C compiler the build already needed.

**Hiding images.** New: an admin can hide images (see
[docs/admin.md](docs/admin.md)). The list of hidden images is state, so give
it a directory of its own that is not wiped with the cache:

- Compose: `deploy/compose.yaml` now has a second volume, `state`. Nothing
  to do but `up -d --build`.
- `docker run`: add `-v dalibot-gallery-state:/var/lib/dalibot-gallery`.
  Without it Docker makes an anonymous volume, which is lost when the
  container is removed.
- systemd: install the unit again (it now has `StateDirectory=`), then
  `sudo systemctl daemon-reload`.
- Anything else: set `GALLERY_STATE_DIR`. Without it the list is kept in the
  cache directory, and the gallery warns at start-up when an admin token is
  set.

**Pages open during the upgrade.** The cursors in the listing changed form
(their format changed). A gallery
page that was open in a browser before the upgrade reloads itself the next
time it loads more images or checks for new ones. The same happens after a
change of `TELEGRAM_BOT_TOKEN`, from which the cursor keys are derived.

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
restart. Settings live in `/etc/dalibot-gallery/env`; edit that file, not the checkout's `.env`.
After a system update of libvips, rebuild as well.

## More documentation

| Document | What it covers |
|---|---|
| [docs/aws-access.md](docs/aws-access.md) | The read-only IAM policy, creating the user and key, troubleshooting |
| [docs/admin.md](docs/admin.md) | Hiding images as an admin: how it works, where the list is kept, what an admin can and cannot do |
| [docs/sqlite.md](docs/sqlite.md) | Reading a self-hosted bot's SQLite database: settings, permissions, container and systemd, what the gallery does to the file |
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
