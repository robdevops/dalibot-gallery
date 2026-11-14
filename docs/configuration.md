# More configuration

The settings every installation needs are in the [README](../README.md#configuration).
These are the others. All are environment variables, checked at start-up
like the rest, and all are listed in `.env.example`.

| Variable | Default | Notes |
|---|---|---|
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | For a local Bot API server or tests. |
| `AWS_REGION` | `ap-southeast-1` | With `dynamodb` only. Falls back to `AWS_DEFAULT_REGION`. |
| `DALIBOT_TABLE` | `dalibot` | With `dynamodb` only. The bot's AWS DynamoDB table. |
| `GALLERY_STORE_DAYS` | `90` | Must equal the bot's `DALIBOT_STORE_DAYS`. The table has no creation time, so the gallery dates each record as `ttl − GALLERY_STORE_DAYS × 86400`. A wrong value shifts every date by the difference. Records with a `created` attribute (pictures imported by forwarding them to the bot) use that date instead. With `sqlite` the date is the time the row was last written, and this setting only decides when a row counts as expired. |
| `GALLERY_LISTEN` | `127.0.0.1:8080` | `host:port`. The container image sets `0.0.0.0:8080`. |
| `GALLERY_TRUSTED_PROXIES` | `127.0.0.0/8,::1` | Comma list of IP addresses or CIDR ranges of your reverse proxies, as the gallery sees them. Only from these peers is `X-Forwarded-For` believed (for the login limiter and the log). It grants no access. `none` trusts nobody. `deploy/compose.yaml` sets it to its network's gateway. See [reverse-proxy.md](reverse-proxy.md). |
| `GALLERY_DEFAULT_COMMAND` | `dream` | Shown for records that carry no command. Display only. |
| `GALLERY_CHATS` | (empty) | Comma list of chat ids to show (negative for groups). Empty shows every chat. |
| `GALLERY_REFRESH` | `60s` | How often the bot's records are re-read. A duration or a number of seconds, 5 s to 24 h. |
| `GALLERY_CACHE_DIR` | `$CACHE_DIRECTORY` or `./cache` | `$CACHE_DIRECTORY` is what systemd's `CacheDirectory=` sets. The container image sets `/var/cache/dalibot-gallery`. |
| `GALLERY_STATE_DIR` | `$STATE_DIRECTORY`, else the cache directory | Where the gallery keeps the list of images an admin has hidden ([Hiding images](admin.md)). This is state, not cache: keep it and back it up. `$STATE_DIRECTORY` is what systemd's `StateDirectory=` sets; the container image sets `/var/lib/dalibot-gallery`. With neither, the list is kept in the cache directory, and the gallery warns at start-up (when an admin token is set or something is hidden) that deleting the cache would show every hidden image again. Created mode 0700 if missing. |
| `GALLERY_CACHE_MAX_MB` | `2048` | Size limit for originals plus thumbnails (minimum 16). Thumbnails are evicted above it; originals are not, so size it for your originals. See [Cache](cache.md). |
| `GALLERY_WEBP_QUALITY` | `76` | 1–100. Changing it changes every thumbnail URL. |
| `GALLERY_IMAGE_WORKERS` | CPUs available | Thumbnail pipelines that may run at once, 1–64. The default is the number of CPUs the process may use: the machine's, or fewer under a container CPU limit (for example `--cpus 2`) or `GOMAXPROCS`. |
| `GALLERY_WARM` | `120` | After each refresh, thumbnails for this many of the newest tiles are made in the background. `0` turns it off. |
| `GALLERY_DEBUG` | (unset) | `1` logs at debug level, including every image, asset and health request. Not validated. |

`GALLERY_SOURCE` and `GALLERY_SQLITE_PATH`, for a self-hosted bot that keeps
its records in SQLite, are described in [SQLite](sqlite.md).

The binary takes no arguments apart from `-healthcheck` (exit 0 if the
running gallery's `/healthz` answers 200) and `-version`.
