# More configuration

The settings every installation needs are in the [README](../README.md#configuration).
These are the others. All are environment variables, checked at start-up
like the rest, and all are listed in `.env.example`.

| Variable | Default | Notes |
|---|---|---|
| `GALLERY_AUTH` | (unset) | `off` disables the gallery's own login, for use behind a proxy that authenticates every request. Setting both this and `GALLERY_TOKEN` is an error. |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | For a local Bot API server or tests. |
| `GALLERY_DEFAULT_COMMAND` | `dream` | Shown for records that carry no command. Display only. |
| `GALLERY_CHATS` | (empty) | Comma list of chat ids to show (negative for groups). Empty shows every chat. |
| `GALLERY_REFRESH` | `60s` | How often the table is re-read. A duration or a number of seconds, 5 s to 24 h. |
| `GALLERY_CACHE_DIR` | `$CACHE_DIRECTORY` or `./cache` | `$CACHE_DIRECTORY` is what systemd's `CacheDirectory=` sets. The container image sets `/var/cache/dalibot-gallery`. |
| `GALLERY_CACHE_MAX_MB` | `2048` | Size limit for originals plus thumbnails (minimum 16). Thumbnails are evicted above it; originals are not, so size it for your originals. See [Cache](cache.md). |
| `GALLERY_WEBP_QUALITY` | `76` | 1–100. Changing it changes every thumbnail URL. |
| `GALLERY_IMAGE_WORKERS` | CPUs available | Thumbnail pipelines that may run at once, 1–64. The default is the number of CPUs the process may use: the machine's, or fewer under a container CPU limit (for example `--cpus 2`) or `GOMAXPROCS`. |
| `GALLERY_WARM` | `120` | After each refresh, thumbnails for this many of the newest tiles are made in the background. `0` turns it off. |
| `GALLERY_DEBUG` | (unset) | `1` logs at debug level, including every image, asset and health request. Not validated. |

The binary takes no arguments apart from `-healthcheck` (exit 0 if the
running gallery's `/healthz` answers 200) and `-version`.
