# Contracts between packages

This file is binding. It lists the exact Go signatures and wire formats that the
packages of this module share, so that they can be written independently.
Sections 1–3 describe code that already exists (read the source for details;
if this file and that code ever disagree, the code wins and this file is wrong).
Sections 4–8 are REQUIRED: implement exactly these names and signatures. A
package may export more, but nothing listed here may be missing or differ.

Module: `github.com/robdevops/dalibot-gallery`

## 0. Toolchain and dependencies (fixed; do not edit go.mod or go.sum)

- Go **1.27** (`go 1.27.0` in go.mod).
- `github.com/davidbyttow/govips/v2` **v2.20.0**. Tested against libvips 8.15.1
  and 8.16.1: start-up, the JPEG-only loader allowlist,
  `NewThumbnailFromBuffer` and `ExportWebp` all work.
  Facts about govips that matter (first checked against v2.17.0):
  - `vips.Startup(&vips.Config{ConcurrencyLevel: 1, MaxCacheSize: 0})`, `vips.Shutdown()`, `vips.Version` (a string constant).
  - `vips.NewThumbnailFromBuffer(buf []byte, width, height int, crop vips.Interesting) (*vips.ImageRef, error)`; use `vips.InterestingCentre`.
    It takes no import parameters. The pipeline therefore calls
    `vips.LoadThumbnailFromBuffer(buf, w, h, vips.InterestingCentre, vips.SizeBoth, params)` (the same libvips operation;
    `NewThumbnailFromBuffer` is that call with nil parameters) with `params.FailOnError.Set(true)`, which govips passes to the
    loader as `fail=TRUE`, libvips' older spelling of `fail_on=error`. A truncated or corrupt JPEG body is then an error.
  - `(*vips.ImageRef).ExportWebp(*vips.WebpExportParams) ([]byte, *vips.ImageMetadata, error)` with fields
    `StripMetadata bool`, `Quality int`, `ReductionEffort int` (and `Lossless`, `NearLossless`, `IccProfile`, `MinSize`, `MinKeyFrames`, `MaxKeyFrames`).
  - Quiet govips' own logging with `vips.LoggingSettings(handler, vips.LogLevelError)` before `Startup`.
- `github.com/mattn/go-sqlite3` v1.14.53: the `sqlite3` driver for `database/sql`, used by `internal/source` (read-only) and by `internal/devstack` (to make its own test database). cgo; it compiles its own copy of SQLite.
- `github.com/aws/aws-sdk-go-v2` `config` v1.33.18, `credentials` v1.21.0, `service/dynamodb` v1.74.0, `feature/dynamodb/attributevalue` v1.21.21.
- `golang.org/x/sync` **v0.19.0** (`singleflight`, `semaphore`, `errgroup`); newer releases need Go 1.25+.
- `internal/deps` (a blank-import file that pinned these while the packages were written) has been removed;
  `go mod tidy` has been run. `golang.org/x/image` is a direct requirement, for tests only.
- Everything else is the standard library.

Ownership of directories:

| Directory | Owner |
|---|---|
| `internal/config`, `internal/styles`, `internal/source`, `testdata/dynamodb`, `testdata/sqlite`, `CONTRACTS.md`, `go.mod`, `go.sum`, `internal/deps` | Foundation (done) |
| `internal/telegram` | Telegram |
| `internal/imaging`, `testdata/images` (sample JPEGs) | Imaging |
| `internal/cache` | Cache |
| `internal/server`, `cmd/gallery` | Server |
| `web/` | Frontend |
| `deploy/`, `README.md`, `.env.example`, `LICENSE` | Docs/deploy |

## 1. `internal/config` (exists)

```go
type Config struct {
	Listen          string        // GALLERY_LISTEN, "host:port"; default 127.0.0.1:8080
	Public          bool          // GALLERY_PUBLIC=1: viewing needs no login
	Token           string        // GALLERY_TOKEN (secret): viewing needs it; "" exactly when Public
	AdminToken      string        // GALLERY_ADMIN_TOKEN (secret); "" means no admin sessions, features or routes
	Source          string        // GALLERY_SOURCE: "dynamodb" (default, config.SourceDynamoDB) or "sqlite" (config.SourceSQLite)
	Table           string        // DALIBOT_TABLE; default dalibot; "" (not read) when Source is sqlite
	Region          string        // AWS_REGION, else AWS_DEFAULT_REGION, else ap-southeast-1; "" (not read) when Source is sqlite
	SQLitePath      string        // GALLERY_SQLITE_PATH made absolute; required when Source is sqlite, an error otherwise
	TelegramToken   string        // TELEGRAM_BOT_TOKEN (secret); required
	TelegramAPIBase string        // TELEGRAM_API_BASE without a trailing slash; default https://api.telegram.org
	StoreDays       int           // GALLERY_STORE_DAYS; default 90; 1..3650
	DefaultCommand  string        // GALLERY_DEFAULT_COMMAND without a slash; default dream
	Chats           []string      // GALLERY_CHATS; nil means all chats
	Refresh         time.Duration // GALLERY_REFRESH; default 60s; 5s..24h; a bare number means seconds
	CacheDir        string        // GALLERY_CACHE_DIR, else first entry of $CACHE_DIRECTORY, else ./cache; absolute
	CacheMaxMB      int           // GALLERY_CACHE_MAX_MB; default 2048; >= 16
	StateDir        string        // GALLERY_STATE_DIR, else first entry of $STATE_DIRECTORY, else CacheDir; absolute; not "/"
	StateInCache    bool          // neither GALLERY_STATE_DIR nor $STATE_DIRECTORY was set: StateDir is CacheDir (main warns)
	WebPQuality     int           // GALLERY_WEBP_QUALITY; default 76; 1..100
	ImageWorkers    int           // GALLERY_IMAGE_WORKERS; default runtime.GOMAXPROCS(0), which follows a container's CPU limit; 1..64
	Warm            int           // GALLERY_WARM; default 120; 0..10000
	TrustedProxies  []netip.Prefix // GALLERY_TRUSTED_PROXIES: comma list of IPs or CIDRs; default 127.0.0.0/8 and ::1; "none" is empty
}

func Load() (*Config, error)                                   // from os.Getenv
func FromEnv(getenv func(string) string) (*Config, error)      // for tests
func (c *Config) CacheMaxBytes() int64
func (c *Config) AdminEnabled() bool                           // AdminToken != ""
func (c *Config) LoginEnabled() bool                           // !Public || AdminEnabled(): /login and /logout exist
func (c *Config) Secrets() []string                            // the non-empty secrets, for log redaction
func (c *Config) LogValue() slog.Value                         // secrets shown as "[redacted]"
func (c *Config) String() string                               // same, as text
```

Rules enforced by `FromEnv` (the error lists every problem, names the variable and never contains a secret's value):

- Who may view: exactly one of `GALLERY_TOKEN` and `GALLERY_PUBLIC=1`. Neither is an error (the gallery is never open by accident); both is an error (contradictory). `GALLERY_PUBLIC` accepts only `1` (and `0` or empty, meaning unset).
- Who may administer: `GALLERY_ADMIN_TOKEN`, optional, in either mode. It must differ from `GALLERY_TOKEN`. An admin token alone (no token, not public) is the "neither" error.
- `GALLERY_TOKEN` and `GALLERY_ADMIN_TOKEN` must each be at least **16** characters (`config.MinTokenLength`), with no surrounding spaces or control characters. Tests and examples must use tokens that long.
- `GALLERY_AUTH` was removed. Set to anything non-blank, whatever else is set, it is an error whose message names `GALLERY_PUBLIC=1` as the replacement; its value is never interpreted.
- `TELEGRAM_BOT_TOKEN` must match `^[0-9]+:[A-Za-z0-9_-]+$` (so it is safe inside a URL path). Tests can use `123456:TEST-token_abc`.
- `TELEGRAM_API_BASE` must be an `http` or `https` URL without credentials, query or fragment.
- `Config` can be passed to `fmt` or `slog` in any form (`%v`, `%+v`, `%#v`, value or pointer, text or JSON handler, `json.Marshal`) without printing a secret. Log it once at start-up as `slog.Info("configuration", "config", cfg)`.

`cmd/gallery` must print a `Load` error to stderr as is and exit with status 2.

## 2. `internal/styles` (exists)

```go
const Base = "_"                 // key of the unstyled image
type Style struct{ Key, Label string }
func All() []Style               // the 24 entries in display order, "_" (Original) first
func Label(key string) string    // table label; unknown keys are title-cased ("neon_noir" -> "Neon Noir")
func Known(key string) bool
func Rank(key string) int        // index in the table; len(All()) for unknown keys
func Less(a, b string) bool      // table order, then unknown keys alphabetically
```

## 3. `internal/source` (exists)

### Types

```go
type Record struct {                  // one stored item, raw
	ID      string                    // "<chat_id>:<message_id>", or "<chat_id>:f<file_unique_id>" for an imported picture
	Prompt, Command, Current string
	TTL     int64                     // epoch seconds; 0 if missing
	Created int64                     // epoch seconds for an imported picture (the "created" attribute); else 0
	Images  map[string]RawImage       // style key -> image
}
type RawImage struct{ FileID, Caption string }

type Source interface {               // read-only backend: Dynamo or SQLite
	Records(ctx context.Context) ([]Record, error)
}
type Static []Record                  // a Source over a fixed list, for tests

type Image struct {                   // JSON: {"style","label","key","caption"}
	Style, Label, Key, Caption string
}
type Tile struct {                    // JSON field names are the lower-case names below
	Key     string  `json:"key"`      // this image's key; unique per tile
	ID      string  `json:"id,omitempty"` // record id (holds the chat id); absent from the JSON of a WithoutIDs copy
	Style   string  `json:"style"`    // this image's style key
	Label   string  `json:"label"`    // this image's style label
	Caption string  `json:"caption"`  // this image's caption (may be "")
	Updated int64   `json:"updated"`  // epoch seconds = ttl - StoreDays*86400, or the record's `created` attribute when it has one
	Prompt  string  `json:"prompt"`   // the record's original prompt
	Command string  `json:"command"`  // the record's command, no slash
	Current string  `json:"current"`  // STYLE KEY (a string, not a bool) of the record's current image after the fallback rule; always one of Styles
	Styles  []Image `json:"styles"`   // every image of the record, this tile's included, in display order; never empty
}
func (t Tile) Alt() string            // caption, or the prompt when the caption is blank
func (t Tile) Cursor() string         // cursor naming this tile's sort position; holds no record id
func WithoutIDs(tiles []Tile) []Tile  // copies without the record id, for encoding only (a copy's Cursor is wrong)

type Options struct {
	StoreDays      int                // cfg.StoreDays
	Chats          []string           // cfg.Chats
	DefaultCommand string             // cfg.DefaultCommand
	Now            func() time.Time   // nil = time.Now (tests set it)
	Hidden         func(key string) bool // nil = nothing hidden; else hidden.List.Has. Asked on every Build; must be safe for concurrent use
}
```

`json.Marshal` of a `Tile` is exactly the tile object of the HTTP API. Neither `Tile` nor `Image` contains a file_id.

### Functions

```go
const KeyLen = 22
const ProbeID = "check:check"
var ErrBadCursor error                             // errors.Is(err, source.ErrBadCursor) -> HTTP 400

func Key(fileID string) string                     // base64url(sha256(file_id))[:22]
func ValidKey(s string) bool                       // exactly 22 of [A-Za-z0-9_-]; says nothing about the snapshot
func Build(records []Record, opt Options) *Snapshot   // all listing rules; use in tests to make snapshots

// DynamoDB reader (Scan, DescribeTable and the three read operations of DynamoDB Streams; never writes)
type DynamoOptions struct {
	Table, Region string
	Endpoint      string        // "" = SDK default (AWS_ENDPOINT_URL_DYNAMODB is honoured); set for moto
	Logger        *slog.Logger  // may be nil
}
func NewDynamo(ctx context.Context, opt DynamoOptions) (*Dynamo, error)   // standard credential chain; no request made
type ScanAPI = dynamodb.ScanAPIClient
func NewDynamoFromClient(api ScanAPI, table string, log *slog.Logger) *Dynamo
func (d *Dynamo) WithStreams(table TableAPI, streams StreamsAPI) *Dynamo   // NewDynamo does this; a reader without it scans every time
func (d *Dynamo) Records(ctx context.Context) ([]Record, error)
// Records: the first call, and every `resyncEvery` (6 h) after, scans the whole table (ConsistentRead only
// when a stream is followed). If the table has a stream (DescribeTable: enabled, view NEW_IMAGE or
// NEW_AND_OLD_IMAGES, and DescribeStream allowed), every later call reads the stream and applies INSERT/MODIFY
// (the new image replaces the record) and REMOVE (by key) to the picture of the table. Every shard is read
// from TRIM_HORIZON (so changes the scan missed or saw are replayed: replacing a record whole and keeping
// each item's changes in order converge), parent first, resumed by AFTER_SEQUENCE_NUMBER, a new iterator at
// every call (nothing expires). A page with no records is read through (3 in a row), `callsPerShard` bounds
// one call. Failures that may pass keep the state and return an error (Store keeps the old snapshot); after 3
// in a row, or at once for the unrecoverable (trimmed records, a vanished unfinished shard, a stream that is
// off, a record that cannot be applied), the picture is dropped, the table scanned and the stream joined again;
// a stream dropped twice within 10 min is left alone for 1 h. A table with no usable stream is scanned at every
// call, with one warning per reason, and looked at again every `attachEvery` (1 h). The records returned are
// shared with the picture: do not modify them; from the stream they are sorted by id.

// SQLite reader for the bot's self-hosted store (lib/store.py, SqliteStore); never writes.
// messages(chat_id TEXT, message_id TEXT, data TEXT, updated INTEGER, PRIMARY KEY (chat_id, message_id)); data is the record as JSON.
type SQLiteOptions struct {
	Path        string         // the bot's database file; required
	StoreDays   int            // the bot's DALIBOT_STORE_DAYS; required
	BusyTimeout time.Duration  // how long to retry while the bot holds a lock; 0 = DefaultSQLiteBusyTimeout (5 s)
	Logger      *slog.Logger   // may be nil
}
func NewSQLite(opt SQLiteOptions) (*SQLite, error)                // does not touch the file
func (s *SQLite) Records(ctx context.Context) ([]Record, error)

// Snapshot: immutable, safe for concurrent use. Returned slices must not be modified.
func (s *Snapshot) Len() int                       // number of tiles
func (s *Snapshot) Records() int
func (s *Snapshot) Built() time.Time               // zero for the empty start-up snapshot
func (s *Snapshot) Page(cursor string, limit int) (tiles []Tile, next string, err error)
func (s *Snapshot) NewSince(cursor string) (count int, newest string, err error)
func (s *Snapshot) Newest(n int) []Tile            // first n tiles; for the warmer and for /api/tiles/new
func (s *Snapshot) Tile(key string) (Tile, bool)
func (s *Snapshot) Has(key string) bool            // is this key listed (may everyone with view access be served it)?
func (s *Snapshot) Hidden() []Tile                 // images Options.Hidden took out, in listing order; Tiles without Styles and Current
func (s *Snapshot) HasHidden(key string) bool      // a hidden image that is still in the bot's records (served to admins only)
func (s *Snapshot) Known(key string) bool          // Has || HasHidden: must the cache keep this key's files?
func (s *Snapshot) FileID(key string) (string, bool)   // listed or hidden. SERVER-SIDE SECRET: only ever pass to telegram.Client.Fetch
func (s *Snapshot) Keys() []string                 // every live key, newest first

// Store: the current snapshot plus the refresher.
func NewStore(src Source, opt Options, log *slog.Logger) *Store   // log may be nil
func (st *Store) Snapshot() *Snapshot              // never nil; empty until the first successful refresh
func (st *Store) Refresh(ctx context.Context) error    // on error the previous snapshot stays
func (st *Store) Run(ctx context.Context, interval time.Duration)  // blocks; refreshes now, then every interval; logs failures
func (st *Store) Rebuild()                         // Build again from the records read last (no read) and swap; after a change to Options.Hidden
func (st *Store) OnSwap(fn func(*Snapshot))        // called after every swap, by Refresh or Rebuild, on the goroutine that swapped
func (st *Store) LastRefresh() time.Time           // last success; zero if none
func (st *Store) Age() time.Duration               // since last success; math.MaxInt64 if none
func (st *Store) Healthy(maxAge time.Duration) bool    // a refresh succeeded less than maxAge ago
```

### Behaviour other packages rely on

- **Listing rules (`Build`)**: drops `check:check`, ids that are not `<chat>:<message>`, chats outside `Options.Chats` (when not empty), records with `ttl < now`, and records without images. Sorts records by (updated desc, id desc; ids compare as strings). Removes an image whose file_id already appeared in a newer record, and drops records left empty. Then takes out every image whose key `Options.Hidden` reports (after the de-duplication, so a hidden image does not come back through an older record), keeps it aside for `Snapshot.Hidden`, and drops records left empty; a hidden image is not among the `Styles` of its record's other tiles. `Current` = stored current if that image exists, else `_`, else the first image in display order. One tile per remaining image: the current style first, then style-table order, then unknown keys alphabetically.
- **`Page`**: `cursor == ""` starts at the newest tile. `next == ""` means the end. A page always holds **whole records**: it takes records while they fit in `limit` and always at least one, so a page can be a little shorter than `limit` (or longer, if one record has more images than `limit`). **A short page is not the end; only `next == ""` is.** This is what keeps paging free of duplicates and gaps when the table changes between requests, including when a user switches a record's style in Telegram (which reorders that record's tiles without changing its timestamp). `limit < 1` is treated as 1; the caller applies the default (60) and the cap (120).
- **`NewSince(cursor)`**: `count` = number of tiles that sort before the cursor's tile (i.e. are newer); `newest` = cursor of the snapshot's first tile, `""` if the snapshot is empty. `cursor == ""` counts every tile. `s.Newest(count)` are exactly those new tiles, newest first.
- **Cursors** are opaque base64url strings without padding (URL-safe as they are). Never build or parse one outside this package. A malformed one gives `ErrBadCursor`. A cursor stays valid across snapshot swaps, even if its tile is gone. A cursor of this package is base64 of text that includes the record id, so it must not reach a browser as it is: the server seals every cursor it sends and opens every cursor it receives (7.3).
- **Request handling**: call `store.Snapshot()` once per request and use that value for everything in the request.
- **`OnSwap` hooks** run synchronously on the goroutine that swapped (the refresher, or the caller of `Rebuild`), in registration order, only after a successful swap; a hook may be called from two goroutines at once. A hook must return quickly: the warmer and the cache pruning must start their own goroutine (and should cancel or skip a still-running previous run). Register hooks before `Run`.
- **Rebuild**: when `Rebuild` returns, the current snapshot reflects every change to `Options.Hidden` made before the call, whatever refresh runs at the same time (snapshots are built and swapped under one lock, from the records read last). It does nothing before the first successful refresh and does not count as a refresh for `LastRefresh`, `Age` or `Healthy`.
- **Liveness**: a key is served to everyone exactly when `store.Snapshot().Has(key)`, and to an admin also when `HasHidden(key)`. Pass `snap.Known` (from the snapshot given to the hook) as the `live` function of `cache.Prune`, so a hidden image keeps its files while its record lasts. Never prune from the empty start-up snapshot: only prune inside an `OnSwap` hook, which runs only after a successful scan.
- **Health**: `/healthz` is `store.Healthy(3 * cfg.Refresh)`.
- **File ids**: `Snapshot.FileID` is the only way to get one. Do not log it, put it in an error that reaches a response, or send it to a browser.
- **SQLite rows (`SQLite.Records`)**: each call opens the file with `file:<escaped path>?mode=ro&_query_only=1&_busy_timeout=0`, runs one `SELECT` (every column cast, so any stored type scans) and closes it; nothing stays open between refreshes. A row maps to `Record{ID: chat_id + ":" + message_id, TTL: updated + StoreDays*86400, Created: data.created (a whole number, else 0), …}`, so `Build` needs no SQLite case: its `ttl < now` rule hides a row older than the store days (the bot purges only at start-up) and `ttl - StoreDays*86400` dates a record from `updated`. `base` and `kind` are not decoded. A row whose `data` is not a JSON object in the bot's shape is skipped and counted in one warning. `SQLITE_BUSY` is retried every 20 ms until `BusyTimeout` or the context ends; every other failure (missing file, no table, hot journal, permissions) is returned at once, and `Store` keeps the previous snapshot. A file whose header says WAL (bytes 18/19 = 2) is refused before SQLite opens it, because a read-only WAL connection creates `-shm`/`-wal` files. Driver: `github.com/mattn/go-sqlite3` (cgo, SQLite compiled in).
- **Test fixtures**: `testdata/sqlite/dalibot.db` was written by the bot's own `SqliteStore` through `testdata/sqlite/make_fixture.py` with the clock fixed at `now = 1800000000` (store days 90): 12 rows, giving 12 tiles from 9 records. `testdata/dynamodb/items.json` (`{"now", "store_days", "items": [DynamoDB-JSON items]}`) holds 17 items covering every shape in the brief; with `now` from the file it yields 19 tiles from 11 records. Other packages' tests should normally build snapshots from `source.Static`/`source.Build` with their own records rather than parse this file.

## 4. `internal/telegram` (REQUIRED)

```go
package telegram

// MaxFileBytes is the download cap.
const MaxFileBytes = 20 << 20

// Client fetches original image bytes from Telegram. It only ever requests
//   POST {apiBase}/bot{token}/getFile          (JSON body {"file_id": "..."})
//   GET  {apiBase}/file/bot{token}/{file_path}
// and refuses, in code, to build any other URL. Safe for concurrent use.
type Client struct{ /* unexported */ }

// New returns a client. apiBase has no trailing slash (cfg.TelegramAPIBase). log may be nil.
func New(apiBase, token string, log *slog.Logger) *Client

// Fetch returns the file's bytes (at most MaxFileBytes; a larger file is an error).
// Inside Fetch: singleflight per file_id, a global limit of 4 concurrent Telegram
// fetches, timeouts of 10 s (getFile) and 30 s (download), one retry after
// parameters.retry_after on HTTP 429 (bounded by ctx), and the failure cache
// (permanent errors 24 h, others 5 min; a cached failure is returned without a request).
// Every error is a *Error and contains neither the token, a Telegram URL, the file_path nor the file_id.
func (c *Client) Fetch(ctx context.Context, fileID string) ([]byte, error)

// Error is the only error type Fetch returns (context errors are wrapped in it as temporary).
type Error struct {
	Permanent  bool          // true: Telegram answered 400 (dead or unknown file_id), or the file is over the cap; cached 24 h
	Status     int           // HTTP status from Telegram, 0 if there was none
	RetryAfter time.Duration // from a 429, else 0
	Cached     bool          // true if this is a replay from the failure cache
	// plus an unexported, already redacted message
}
func (e *Error) Error() string
func (e *Error) Temporary() bool            // !Permanent

// Sentinels: errors.Is(err, telegram.ErrPermanent) / errors.Is(err, telegram.ErrTemporary).
var ErrPermanent error
var ErrTemporary error
func (e *Error) Is(target error) bool       // matches the sentinel for e.Permanent

// Redact replaces every occurrence of each non-empty secret in s with "[redacted]".
// It also redacts the URL-escaped form of each secret.
func Redact(s string, secrets ...string) string

// Redact applies the package Redact with the client's own token.
func (c *Client) Redact(s string) string
```

- The HTTP client must not follow redirects to another host and must never log a `*url.Error` unredacted (it contains the full URL with the token).
- Tests use `httptest` only. They must fail on any request path other than the two above, prove the token is absent from captured `slog` output and from every error, and cover 429 `retry_after`, the failure cache and the 20 MiB cap. A test hook to shorten waits is fine as an unexported field or an extra exported option, as long as `New` keeps the signature above.

## 5. `internal/imaging` (REQUIRED)

```go
package imaging

// PipelineVersion changes whenever size, crop or encoder settings change.
const PipelineVersion = 1                  // untyped integer constant

// Limits enforced by Validate.
const (
	MaxPixels = 50_000_000
	MaxSide   = 10_000
)

// Sizes are the only thumbnail sizes.
var Sizes = [...]int{240, 480}
func ValidSize(size int) bool

// Background is the page, tile and placeholder colour. web/app.css must use the same value.
const Background = "#111111"

type Options struct {
	Workers int   // cfg.ImageWorkers; concurrent pipelines
	Quality int   // cfg.WebPQuality
}

// Startup sets VIPS_BLOCK_UNTRUSTED=1, starts libvips (ConcurrencyLevel 1, MaxCacheSize 0),
// installs the JPEG-only loader allowlist and builds the placeholder. Call once, before any other function.
func Startup(opt Options) error
// Shutdown stops libvips. No other function may be called afterwards.
func Shutdown()

// Version is the libvips version, e.g. "8.15.1", for the start-up log.
func Version() string

// Tag names the output of the running pipeline: strconv.Itoa(PipelineVersion) when the quality is 76,
// otherwise fmt.Sprintf("%dq%d", PipelineVersion, quality). It is the `v` of image URLs and the
// version in cache file names, so changing GALLERY_WEBP_QUALITY can never serve stale bytes.
// Matches ^[0-9a-z]{1,16}$. Valid after Startup.
func Tag() string

// Validate checks, in pure Go and before libvips sees the bytes, that data starts with FF D8 FF
// and that image/jpeg.DecodeConfig reports at most MaxSide per side and MaxPixels in total.
func Validate(data []byte) error
var ErrNotJPEG error     // wrapped by Validate errors for non-JPEG input
var ErrTooLarge error    // wrapped by Validate errors for oversized dimensions

// Verify validates jpeg and then decodes all of it with fail_on=error, producing nothing. It is an
// error for a body that is truncated or corrupt behind a valid header. The server calls it on every
// downloaded original before storing it. Same worker slots, pixel budget and ctx rules as Thumbnail.
func Verify(ctx context.Context, jpeg []byte) error

// Thumbnail validates jpeg, then returns a square centre-cropped WebP of size x size pixels
// (smaller, still square, if the source is smaller: never upscaled). size must satisfy ValidSize.
// The JPEG is decoded with fail_on=error. It waits for one of the Workers slots and for room in the
// pixel budget (the source pixels of all running pipelines together are at most MaxPixels, so the
// largest accepted image is decoded alone); ctx cancels the wait (returns ctx.Err()).
func Thumbnail(ctx context.Context, jpeg []byte, size int) ([]byte, error)

// Placeholder is a small solid WebP in Background colour, served when an image cannot be fetched.
// Valid after Startup; the returned slice must not be modified.
func Placeholder() []byte
```

- WebP export: `Quality` from Options, `StripMetadata: true`, `ReductionEffort: 4`.
- The cgo loader allowlist: `vips_operation_block_set("VipsForeignLoad", TRUE)` then `vips_operation_block_set("VipsForeignLoadJpeg", FALSE)`.
- Sample JPEGs for tests live in `testdata/images/` (1024×1024, progressive, about q88, real detail; a few small files). Imaging creates them; other packages may read them.
- Tests in other packages that need imaging call `imaging.Startup` once in `TestMain` and `Shutdown` at the end.

## 6. `internal/cache` (REQUIRED)

```go
package cache

// Cache is a disk cache under one directory. Safe for concurrent use.
//   originals:  <dir>/<key>.jpg
//   thumbnails: <dir>/<key>-<size>-v<version>.webp
// Every write goes to a temp file in the same directory and is renamed into place.
type Cache struct{ /* unexported */ }

// Open creates dir (0700) if needed, removes leftover temp files and indexes what is there.
// maxBytes is cfg.CacheMaxBytes(). log may be nil.
func Open(dir string, maxBytes int64, log *slog.Logger) (*Cache, error)

// key must satisfy source.ValidKey, size imaging.ValidSize, version ^[0-9a-z]{1,16}$ (imaging.Tag()).
// Anything else is rejected (Get: ok=false; Put: error) without touching the disk, so no input can escape dir.

func (c *Cache) GetOriginal(key string) (data []byte, ok bool)
func (c *Cache) PutOriginal(key string, data []byte) error
func (c *Cache) HasOriginal(key string) bool

func (c *Cache) GetThumb(key string, size int, version string) (data []byte, ok bool)   // marks the entry as recently used
func (c *Cache) PutThumb(key string, size int, version string, data []byte) error       // may evict older thumbnails
func (c *Cache) HasThumb(key string, size int, version string) bool                     // no read, does not touch recency

// Prune removes every original and every thumbnail whose key is not live, and every thumbnail whose
// version is not `version`. It returns the number of files removed. Call it from a source.Store OnSwap
// hook (in its own goroutine) with live = snapshot.Has.
func (c *Cache) Prune(live func(key string) bool, version string) (removed int, err error)

// Size is the total bytes of all cached files (originals and thumbnails).
func (c *Cache) Size() int64
```

- Size limit: when originals + thumbnails exceed `maxBytes`, thumbnails are evicted least recently used first until the total fits or the thumbnails are down to the reserve, `maxBytes/8`. The reserve keeps the most recently used thumbnails when the originals alone approach or pass the limit (otherwise each thumbnail would be evicted as it is written); the directory can then hold the originals plus the reserve, and one warning is logged when that state begins. **Originals are never evicted for size**; only `Prune` removes them. Recency survives a restart approximately (use file mtime at `Open`).
- A `Get` miss, a read error and a zero-length file are all `ok=false`.
- Files are written 0600.

## 6a. `internal/hidden` (exists)

The gallery's own list of images an admin has hidden. It is the only state the gallery writes besides its cache; it never touches the bot's data.

```go
package hidden

const FileName = "hidden.json"   // in cfg.StateDir: {"version":1,"hidden":[{"key":"<22>","at":<epoch s>}, …]}
const MaxEntries = 100_000
var ErrBadKey, ErrFull, ErrReadOnly error

type Entry struct{ Key string; At int64 }
type List struct{ /* unexported */ }   // safe for concurrent use

func Open(dir string, log *slog.Logger) (*List, error)
func (l *List) Path() string
func (l *List) Has(key string) bool       // what source.Options.Hidden takes
func (l *List) At(key string) (int64, bool)
func (l *List) Len() int
func (l *List) Hide(key string) error
func (l *List) Restore(key string) error
```

- `Open` creates `dir` (0700), removes leftover temporary files, and reads the list. A missing file is an empty list. A file it cannot understand (not JSON, another version) is renamed to `hidden.json.corrupt-<UTC time>` and logged, and the list starts empty; if the rename fails the list starts empty and every change returns `ErrReadOnly`, so the file is never overwritten. Entries whose key fails `source.ValidKey` are skipped with a warning. An error is returned only when the directory cannot be used or the file exists but cannot be read (or is not a regular file, or is over 16 MiB); `cmd/gallery` then exits with status 1.
- `Hide` and `Restore` take `source.ValidKey` keys only (`ErrBadKey`), write the whole list to a temporary file in the same directory (0600), `fsync` it, rename it over the list and `fsync` the directory. If the write fails, the change is undone in memory: `Has` always says what the file says. Nothing is written when the key was already hidden, or not hidden.
- The file is read at `Open` only.

## 7. HTTP API (REQUIRED; implemented by `internal/server`, consumed by `web/`)

### 7.1 Construction

```go
package server

type Fetcher interface {   // satisfied by *telegram.Client
	Fetch(ctx context.Context, fileID string) ([]byte, error)
}

type Deps struct {
	Config  *config.Config
	Store   *source.Store
	Fetcher Fetcher
	Cache   *cache.Cache
	Hidden  *hidden.List     // the list behind Store's Options.Hidden; nil: nothing can be hidden and the admin routes do not exist
	Web     fs.FS            // web.Files: index.html, login.html, hidden.html, app.js, app.css, admin.js, admin.css, favicon.png at its root
	Log     *slog.Logger
}

// New returns the handler with every route, auth, headers and gzip applied.
// It parses the templates and returns an error if they are invalid, or if the
// Config does not say who may view (neither or both of Public and Token) or
// has an admin token equal to the token.
func New(d Deps) (*Server, error)
func (s *Server) Handler() http.Handler
// Warm generates missing thumbnails for the newest Config.Warm tiles of snap. It is what main registers
// with Store.OnSwap (wrapped in a goroutine) together with Cache.Prune.
func (s *Server) Warm(ctx context.Context, snap *source.Snapshot)

// Added by the Server phase:
func (s *Server) OnSwap(snap *source.Snapshot)   // the Store.OnSwap hook: Warm and Cache.Prune in the background; returns at once
func (s *Server) Close()                         // stops and waits for background image work; call before imaging.Shutdown
func RedactingHandler(h slog.Handler, secrets ...string) slog.Handler   // strips secrets from every log record
```

`cmd/gallery` also accepts `-healthcheck` (GET /healthz on GALLERY_LISTEN; exit 0 when healthy) and `-version`,
and logs image, asset and health requests only when `GALLERY_DEBUG=1`. JSON bodies under 256 bytes are not gzip-encoded.
`internal/devstack` and `scripts/dev-stack.sh` run the whole stack locally (moto, a fake Telegram API, seeded data);
sample JPEGs are in `testdata/telegram/` (not `testdata/images/`).

`cmd/gallery/main.go` wires, in this order: `config.Load` → logger → `imaging.Startup` → `cache.Open` → `hidden.Open(cfg.StateDir)` (warning when `cfg.StateInCache` and there is an admin token or something hidden) → `telegram.New` → `source.NewSQLite` when `cfg.Source` is sqlite, else `source.NewDynamo` (only the chosen one is constructed) → `source.NewStore` (with `Options.Hidden = hid.Has`) → `server.New` (with `Hidden: hid`) → `store.OnSwap(...)` → `go store.Run(ctx, cfg.Refresh)` → `http.Server{ReadHeaderTimeout: 5s, ReadTimeout: 10s, WriteTimeout: 60s, IdleTimeout: 120s, MaxHeaderBytes: 16 << 10}`; on SIGINT/SIGTERM: shut the server down, cancel ctx, `imaging.Shutdown`.

How an image is produced (server): thumbnail request → `cache.GetThumb` → else original (`cache.GetOriginal` → else `snap.FileID` + `Fetcher.Fetch` + `imaging.Verify` (header check, then a full decode) + `cache.PutOriginal`) → `imaging.Thumbnail` → `cache.PutThumb`. Use singleflight per `key+size` so concurrent requests for one cold tile do the work once.

### 7.2 Routes

Only `GET` and `HEAD` are allowed, except `POST /login`, `/logout`, which takes only `POST`, and the admin's `POST` routes below; anything else is `405` with an `Allow` header. Unknown paths are `404`. `/login` and `/logout` exist only when `cfg.LoginEnabled()`; in a public gallery without an admin token they are unknown paths (`404`, and `405` for a `POST`). All routes except `/healthz`, `/login`, `/logout`, `/app.css` (the login page's stylesheet) and `/favicon.png` need view access (7.4), which everyone has in a public gallery.

| Route | Response |
|---|---|
| `GET /` , `GET /?cursor=<c>` | `200 text/html; charset=utf-8`: `index.html` rendered as in 7.5. `Cache-Control: no-store`. A cursor the server did not seal is `400`. |
| `GET /app.js`, `GET /app.css` | the embedded file, `Cache-Control: no-cache`, strong `ETag`, `304` on `If-None-Match`. `/app.js` needs view access (`401 text/plain` without); `/app.css` does not. |
| `GET /api/tiles?limit=<n>&cursor=<c>` | JSON, see 7.3. |
| `GET /api/tiles/new?since=<c>[&tiles=1]` | JSON, see 7.3. |
| `GET /img/<key>-<size>.webp?v=<tag>` | the WebP thumbnail, see below. |
| `GET /img/<key>.jpg` | the original JPEG, byte for byte, see below. |
| `GET /login`, `POST /login`, `POST /logout` | see 7.4. |
| `GET /healthz` | `200 text/plain` body `ok\n` when `store.Healthy(3*cfg.Refresh)`, else `503` body `stale\n`. No auth. |
| `GET /admin.js`, `GET /admin.css` | admin only (see below): the embedded file, `Cache-Control: private, no-cache`, `ETag`, `304`. |
| `GET /admin/hidden` | admin only: `hidden.html` (7.5), `no-store`. |
| `POST /api/admin/hide/<key>` | admin only: hide an image the snapshot knows (`snap.Known`) or that is hidden already; `200 {"key":"<key>","hidden":true}`. |
| `POST /api/admin/restore/<key>` | admin only: restore a hidden image (also one no longer in the records); `200 {"key":"<key>","hidden":false}`. |

Admin routes (`internal/server/admin.go`) exist only when `cfg.AdminEnabled()` and `Deps.Hidden != nil`, and are served only when `authorize` gives admin level with no wait. Everyone else gets exactly what an unknown path gives: a `GET` goes through the usual access handling (`303` to `/login`, or `401`, or `404`), and a `POST` is `404` (JSON `{"error":"not found"}`) whatever the key, never `405`. For an admin: a `POST` that fails `http.CrossOriginProtection` is `403` `{"error":"cross-origin request refused"}` and changes nothing; an invalid or unknown key is `404`; `MaxEntries` reached is `409`; a list that cannot be written is `500` and changes nothing; a wrong method is `405` with `Allow`. On success the server calls `store.Rebuild()` before answering, so the listing and the image routes already reflect the change, and logs the key and client address. `/admin.js` and `/admin.css` are unknown paths (`404`) to everyone but an admin.

Image routes:

- The path after `/img/` must match `^[A-Za-z0-9_-]{22}-(240|480)\.webp$` or `^[A-Za-z0-9_-]{22}\.jpg$`. A key may itself contain `-` and `_`: it is always the first 22 characters. Anything else is `404`.
- The key must be in the current snapshot (`snap.Has`), else `404`. A hidden image (`snap.HasHidden`) is `404` like any unknown key, except to an admin, who is served it with `Cache-Control: no-store` and no `ETag` (and no `304`). For `.webp`, `v` must equal `imaging.Tag()`, else `404` (an old page asking for an old pipeline version must not be given new bytes under an immutable URL).
- Success: `200`, `Content-Type: image/webp` or `image/jpeg`, `Cache-Control: private, max-age=31536000, immutable` (`public, …` when `cfg.Public`; the brief's `public` would let shared caches store images that are behind the login), `ETag: "<key>-<size>-v<tag>"` (thumbnail) or `"<key>"` (original), `Content-Length`. `If-None-Match` with that ETag gives `304` without doing any work.
- Failure to obtain the image (Telegram error, invalid bytes):
  - `.webp`: `200`, `Content-Type: image/webp`, body `imaging.Placeholder()`, `Cache-Control: no-store`, header `X-Gallery-Placeholder: 1`, no `ETag`.
  - `.jpg`: `502`, `text/plain`, `Cache-Control: no-store`, body `image unavailable\n`.
- Images are never gzip-encoded.

### 7.3 JSON

All JSON responses: `Content-Type: application/json; charset=utf-8`, `Cache-Control: no-store`, `Vary: Accept-Encoding`, gzip when the request allows it. Errors are `{"error":"<short message>"}` with status `400` (bad `limit`, `cursor` or `since`), `401`, `404`, `405` or `429`. No response ever contains a file_id, a token, a Telegram URL or a file_path, and no response to a non-admin visitor of a public gallery contains a record id or a chat id.

**Cursors hold no record id.** A cursor is base64url of the tile's date, its place within its record and the image key of the record's original picture.

**`GET /api/tiles`**

- `limit`: optional integer; default 60; values above 120 are treated as 120; less than 1 or not an integer is `400`.
- `cursor`: optional; the `next` of an earlier response. Absent or empty starts at the newest tile.

```json
{
  "tiles": [
    {
      "key": "RQ2AvWdtw2pN5Bhv0Y3CdX",
      "id": "-1001234567890:101",
      "style": "monet",
      "label": "Monet",
      "caption": "A red fox standing in deep snow, in the style of Claude Monet",
      "updated": 1799999000,
      "prompt": "a red fox in snow",
      "command": "dream",
      "current": "monet",
      "styles": [
        {"style": "monet", "label": "Monet", "key": "RQ2AvWdtw2pN5Bhv0Y3CdX", "caption": "A red fox standing in deep snow, in the style of Claude Monet"},
        {"style": "_", "label": "Original", "key": "p0Qm3J1c9s7TtYbV2xKe4A", "caption": "A red fox standing in deep snow"}
      ]
    }
  ],
  "next": "3q2-7w…(a cursor)",
  "newest": "9J8hZk…(a cursor)",
  "v": "1"
}
```

- `tiles`: always an array (`[]` when empty, never `null`), newest first; each element is `json.Marshal(source.Tile)`.
  - `id`: the record id. **Absent** for a visitor of a public gallery who is not an admin (`cfg.Public && level < accessAdmin`: the tiles go through `source.WithoutIDs`). Present when viewing needs the token, and for an admin. The page does not use it.
  - `key`: unique per tile; the DOM de-duplication id and the deep link `#/p/<key>`.
  - `current`: a **string**, the style key of the record's current image. The tile is the record's current image when `tile.style === tile.current`.
  - `styles`: every image of the tile's record, the tile's own included (find it by `key`), current style first. Each entry's 240 px thumbnail is `/img/<styles[i].key>-240.webp?v=<v>` and its full image `/img/<styles[i].key>.jpg`. The lightbox strip does not use `styles`: it holds a lazily loaded thumbnail of every loaded tile in gallery order, centred on the open one, and loads the next page when scrolled near its end.
  - `updated`: epoch seconds. `caption` may be `""`: alt text is `caption || prompt`.
- `next`: string cursor for the following page, or `null` at the end. **A page may hold fewer (or, rarely, more) tiles than `limit`**, because pages hold whole records; keep loading until `next` is `null`.
- `newest`: cursor of the newest tile in the listing at the time of the response, or `null` if the listing is empty. The page keeps the `newest` of its **first** (cursor-less) response as its `since`.
- `v`: `imaging.Tag()`. Image URLs are built by the client as:
  - `/img/<key>-240.webp?v=<v>` and `/img/<key>-480.webp?v=<v>`
  - `/img/<key>.jpg`

**`GET /api/tiles/new?since=<cursor>`** → `{"count": 3, "newest": "<cursor>"}`

- `since`: the `newest` the page holds. Absent or empty counts every tile.
- `count`: number of tiles newer than `since`. `newest`: as above (`null` when the listing is empty).
- With `&tiles=1` the response also carries the new tiles themselves:
  `{"count": 3, "newest": "<cursor>", "v": "1", "tiles": [ ...the min(count, 120) newest tiles, newest first... ]}`.
- The "N new" pill: poll without `tiles` every 60 s while the page is visible and show `count` when it is above 0. On click, request with `tiles=1`. If `count > tiles.length` (more than 120 new), reload the page. Otherwise, for each returned tile in order, remove any DOM node that already has that key (a record that gained a style moves to the top, and its older tiles come with it), insert the tiles at the top in the order given, set `since = newest`, and scroll to the top. Never insert without the click.

### 7.4 Access

A request has one of three levels (`internal/server/auth.go`, `authorize`): none, view, admin. Admin includes view.

- **View**: everyone, when `cfg.Public`. Otherwise a view or admin session cookie, or `Authorization: Basic …` whose **password** equals `GALLERY_TOKEN` or `GALLERY_ADMIN_TOKEN` (any username).
- **Admin** (only when `cfg.AdminEnabled()`): the admin session cookie, or Basic with `GALLERY_ADMIN_TOKEN` as the password. In either mode.
- Nothing else counts: not the peer address, not `GALLERY_TRUSTED_PROXIES`, not a header.

Session cookie: name `gallery_session`, one cookie for both kinds, compared in constant time against both values:

- view session: lower-case hex of `HMAC-SHA256(key = GALLERY_TOKEN, message = "dalibot-gallery session v1")`;
- admin session: lower-case hex of `HMAC-SHA256(key = GALLERY_ADMIN_TOKEN, message = "dalibot-gallery admin session v1")`.

A session whose token is not configured has no value and matches nothing (not even an empty cookie).

A Basic password is a guess at both tokens and goes through the limiter below (`loginLimiter.guess`): a wrong one is recorded once; the same wrong password from the same client again within the window is refused without being recorded or logged again (the limiter keeps an HMAC fingerprint of it under a random per-process key, never the password), so a browser or a Basic-auth proxy resending it on every request does not use up the client's allowance; and a client over the limit has its password ignored, right or wrong. The cookie is checked first and is never limited. The password is only evaluated when it could raise the level: never for an admin session, never for a view session or a public visitor when there is no admin token. When a wrong or blocked password comes with view access from elsewhere (public mode, or a view cookie), the request is served at view level; otherwise it is `401`, or `429` with `Retry-After` (JSON for `/api/`) when blocked. `authorize` returns the level and the wait; an admin-only route must refuse when the level is below admin or the wait is above zero, answering as for an unknown path.

Requests without view access (only when viewing needs the token):

- `GET /` (and any other non-API, non-image page): `303` to `/login`.
- `/api/…` and `/img/…`: `401` (`{"error":"unauthorized"}` for `/api/`, empty body for `/img/`). **No `WWW-Authenticate` header**, so `fetch()` never raises the browser's password dialog. The page reacts to a `401` from the API by navigating to `/login`.

Login and logout (both exist only when `cfg.LoginEnabled()`):

- `GET /login`: `200 text/html`, `login.html` rendered as in 7.5 with `Error` false. `303` to `/` instead when the cookie is an admin session, or a view session and there is no admin token (a view session still gets the form when there is one: signing in again is how it becomes an admin session). Only the cookie is looked at here.
- `POST /login`: body `application/x-www-form-urlencoded` (at most 4 KiB) with field `token`, compared with both tokens.
  - Correct: `303` to `/` with `Set-Cookie: gallery_session=<value>; Path=/; Max-Age=31536000; HttpOnly; Secure; SameSite=Strict`, the value being the view or the admin session according to which token it was.
  - Wrong: `401 text/html`, `login.html` with `Error` true.
  - Rate limit, shared with Basic passwords and between the two tokens: at most 5 wrong tokens per client per minute, and 300 per minute overall; beyond that `429` with `Retry-After`, whether or not the token is right. The body is read first; the limiter then checks and records in one locked step (`loginLimiter.attempt`), so requests in flight together cannot all pass the check.
  - The client is the peer address, or, when the peer is in `cfg.TrustedProxies`, the right-most `X-Forwarded-For` entry that is not itself a trusted proxy. IPv6 clients are counted per /64.
- `POST /logout`: `303` to `/` with `Set-Cookie: gallery_session=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict`. Needs no session. `GET` is `405` with `Allow: POST`.
- A `POST` to either from another origin (`http.CrossOriginProtection`) is `403` and changes nothing.
- The `Secure` attribute is always set: browsers accept it over plain HTTP on `localhost`/`127.0.0.1`, which is what tests use.

Headers on **every** response the handler writes (responses net/http writes itself before the handler runs, such as `400` for a missing Host header or `431`, cannot carry them):

```
Content-Security-Policy: default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy: accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=(), interest-cohort=()
```

So `web/` must contain no inline `<script>`, no inline `<style>`, no `style=""` attributes, no `on…=""` handlers, no data: or blob: images and no external resources.

### 7.5 `web/` (Frontend) and the templates

```go
// web/embed.go
package web

import "embed"

//go:embed index.html login.html hidden.html app.js app.css admin.js admin.css favicon.png
var Files embed.FS
```

`index.html`, `login.html` and `hidden.html` are parsed with `html/template` (so `{{`…`}}` is template syntax in those files; the scripts and stylesheets are served as they are). The scripts and styles are referenced as `/app.css` and `/app.js` (`<script src="/app.js" defer>`).

`index.html` is executed with:

```go
type indexData struct {
	V     string       // imaging.Tag()
	Tiles []indexTile  // one page (limit 60) starting at ?cursor=, or at the newest tile
	Next  string       // cursor of the next page, "" at the end
	Admin      bool    // the visitor has admin access: rendered as <html lang="en" data-admin="1">
	AdminLogin bool    // an admin token is configured and the visitor is not an admin: a link to /login in the footer
	SignOut    bool    // the visitor has a session cookie: a POST /logout form in the footer
	Hide       bool    // admin level and the admin routes exist: /admin.css and /admin.js (after /app.js) in <head>, and a link to /admin/hidden in the footer
}
type indexTile struct {
	Key    string  // tile key
	Alt    string  // caption, or prompt
	Src240 string  // "/img/<key>-240.webp?v=<V>"
	Src480 string  // "/img/<key>-480.webp?v=<V>"
	Full   string  // "/img/<key>.jpg"
}
```

The first page is server-rendered **inside `<noscript>`** only; with JavaScript the page starts empty and `app.js` loads `/api/tiles`. `index.html` must contain this block (class names and attributes may be extended, the template actions may not change):

```html
<noscript>
  <ul class="grid">
    {{range .Tiles}}<li class="tile"><a href="{{.Full}}"><img src="{{.Src240}}" srcset="{{.Src240}} 240w, {{.Src480}} 480w" sizes="(min-width:1600px) 17vw, (min-width:1200px) 20vw, (min-width:768px) 25vw, (min-width:480px) 34vw, 50vw" width="240" height="240" loading="lazy" decoding="async" alt="{{.Alt}}"></a></li>{{end}}
  </ul>
  {{if .Next}}<p class="more"><a href="/?cursor={{.Next}}">Load more</a></p>{{end}}
</noscript>
```

With JavaScript, `?cursor=` in the page URL is ignored.

**The admin flag.** For an admin the server renders `<html lang="en" data-admin="1">`; for everyone else `<html lang="en">`, with no attribute. Script reads it as `document.documentElement.dataset.admin`. It is a hint for the page: the server checks the level on every request. Admin-only script must be a separate file served only at admin level, so that `app.js` + `app.css` stay within their budget and other visitors download nothing extra.

**Admin script.** `admin.js` and `admin.css` hold everything for hiding and restoring; nothing in `app.js`, `app.css` or the page of a visitor without `Hide` refers to them (tested in `web/web_test.go`). `admin.js` works on the page `app.js` builds without changing it: it adds `<button id="trash" class="nav">` (a trash-can icon, top left, styled by `.nav` so it matches the close button and fades with the overlays) to the viewer stage. One press posts the hide for the current image, with no confirmation and no Undo; presses within 500 ms of a hide are ignored. It then removes the tile and its strip thumbnail, refills the grid's 60-tile batches in order, and moves the viewer to the next image (the previous at the end, closing if none) through `location.replace('#/p/<key>')`, and announces "Image hidden" (or "Could not hide the image") in the live region only. On `hidden.html` it handles the `data-restore` buttons.

`hidden.html` is executed with `struct{ Tiles []struct{ Key, Alt, Text, Src240, Src480, Full string }; Gone int; SignOut bool }` and is always rendered with `<html lang="en" data-admin="1">`; it lists each tile in `<ul id="hidden">` with a `<button type="button" data-restore="<key>">Restore</button>`, has `<p id="none">` (hidden unless the list is empty) and a `#live` region, and loads `/app.css`, `/admin.css` and `/admin.js` only.

**The footer.** When `AdminLogin`, `SignOut` or `Hide`, the page ends with `<footer class="foot">` holding `<a href="/admin/hidden">Hidden images</a>` (with `Hide`), `<a href="/login">Admin</a>` and/or `<form method="post" action="/logout"><button type="submit">Sign out</button></form>`. An anonymous visitor of a public gallery without an admin token gets no footer.

`login.html` is executed with `struct{ Error, AdminOnly bool }` (`AdminOnly` is `cfg.Public`: the label reads "Admin token" and a link leads back to the gallery) and must contain a `<form method="post" action="/login">` with `<input type="password" name="token" autocomplete="current-password" required autofocus>` and a submit button, and show a message when `{{if .Error}}`.

Fixed values shared by `web/` and the server:

- Page, grid, tile and dialog background colour: `#111111` (`imaging.Background`).
- Deep link: `#/p/<key>`; a tile's link is `<a href="#/p/<key>">`.
- The grid the script fills is `<ul class="grid" aria-label="Gallery">`; each tile `<li class="tile" data-key="<key>">`.

## 8. Things no package may do

- Call any Telegram method other than `getFile` and the file download, or any DynamoDB operation other than `Scan`, `DescribeTable`, `DescribeStream`, `GetShardIterator` and `GetRecords`. Tests may create and fill tables in moto only.
- Open the bot's SQLite file in any way but read-only, or run any statement on it but the one `SELECT`. Tests and the dev stack write only to database files they created themselves.
- Write anywhere but `cfg.CacheDir` (the cache) and `cfg.StateDir` (`hidden.json`).
- Send or log a file_id, the bot token, the gallery token, a cookie value, a Telegram URL or a file_path.
- Import or run anything from the bot's repository.
