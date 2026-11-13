# Cache and PIPELINE_VERSION

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

An image an admin has [hidden](admin.md) keeps its files while its record
lasts, so that restoring it needs no second download from Telegram (which
may no longer be possible). The list of hidden images is not in the cache
but in `GALLERY_STATE_DIR`; only when that is unset does it fall back to
the cache directory, as `hidden.json`, and then deleting the cache shows
every hidden image again.

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
If `GALLERY_STATE_DIR` is not set, keep `hidden.json` when you do.
