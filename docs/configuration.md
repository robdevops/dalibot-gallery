# More configuration

The settings every installation needs are in the [README](../README.md#configuration).
These are the others. All are environment variables, checked at start-up
like the rest, and all are listed in `.env.example`.

| Variable | Default | Notes |
|---|---|---|
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | For a local Bot API server or tests. |
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

`GALLERY_AUTH` no longer exists. If it is set to anything, the gallery
refuses to start; `GALLERY_PUBLIC=1` replaces `GALLERY_AUTH=off`. See
[Who can view and who can administer](../README.md#who-can-view-and-who-can-administer)
and [Upgrading](../README.md#upgrading).

The binary takes no arguments apart from `-healthcheck` (exit 0 if the
running gallery's `/healthz` answers 200) and `-version`.
