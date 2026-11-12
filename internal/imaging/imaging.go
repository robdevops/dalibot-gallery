// Package imaging turns Telegram's JPEGs into square WebP thumbnails with
// libvips (through govips).
//
// The input is untrusted, so three things stand between it and the decoders:
// libvips starts with VIPS_BLOCK_UNTRUSTED=1, every loader except JPEG is
// blocked, and Validate checks the JPEG header in pure Go before libvips sees
// a single byte.
package imaging

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"runtime"
	"strconv"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
	"golang.org/x/sync/semaphore"
)

// PipelineVersion changes whenever size, crop or encoder settings change.
const PipelineVersion = 1

// Limits enforced by Validate.
const (
	MaxPixels = 50_000_000
	MaxSide   = 10_000
)

// pixelBudget is the total number of source pixels (width x height from the
// JPEG header) that all running pipelines may hold together. A progressive
// JPEG is decoded with all of its coefficients in memory, about 3 to 6 bytes a
// pixel whatever the output size, so Workers pipelines on MaxPixels images
// would need gigabytes. With the budget equal to MaxPixels the largest image
// that Validate accepts runs alone, mid-sized ones run a few at a time, and
// Telegram's 1024 x 1024 images are not held back at all.
const pixelBudget = MaxPixels

// verifySide is the box Verify decodes to: small, so the decode is as cheap as libjpeg allows.
const verifySide = 16

// Sizes are the only thumbnail sizes.
var Sizes = [...]int{240, 480}

// ValidSize reports whether size is one of Sizes.
func ValidSize(size int) bool {
	for _, s := range Sizes {
		if s == size {
			return true
		}
	}
	return false
}

// Background is the page, tile and placeholder colour. web/app.css must use the same value.
const Background = "#111111"

// DefaultQuality is the WebP quality whose output is tagged with the bare pipeline version.
const DefaultQuality = 76

// reductionEffort is libwebp's effort setting (0 fastest .. 6 smallest); 4 is the libvips default.
const reductionEffort = 4

// Options configures Startup.
type Options struct {
	Workers int // concurrent pipelines; 0 means runtime.GOMAXPROCS(0)
	Quality int // WebP quality 1..100; 0 means DefaultQuality
}

// Errors wrapped by Validate (and so by Thumbnail).
var (
	ErrNotJPEG  = errors.New("not a JPEG image")
	ErrTooLarge = errors.New("image dimensions too large")
)

// ErrNotStarted is returned by Thumbnail before Startup or after Shutdown.
var ErrNotStarted = errors.New("imaging: not started")

//go:embed placeholder.webp
var placeholder []byte

type state struct {
	quality int
	tag     string
	slots   chan struct{}
	pixels  *semaphore.Weighted // pixelBudget
}

var (
	mu      sync.RWMutex // held for reading while a pipeline runs; Shutdown takes it for writing
	current *state
	stopped bool
)

// Startup sets VIPS_BLOCK_UNTRUSTED=1, starts libvips (ConcurrencyLevel 1, MaxCacheSize 0) and
// installs the JPEG-only loader allowlist. Call once, before any other function.
func Startup(opt Options) error {
	if opt.Workers == 0 {
		opt.Workers = runtime.GOMAXPROCS(0)
	}
	if opt.Quality == 0 {
		opt.Quality = DefaultQuality
	}
	if opt.Workers < 1 || opt.Workers > 1024 {
		return fmt.Errorf("imaging: workers must be between 1 and 1024, got %d", opt.Workers)
	}
	if opt.Quality < 1 || opt.Quality > 100 {
		return fmt.Errorf("imaging: quality must be between 1 and 100, got %d", opt.Quality)
	}

	mu.Lock()
	defer mu.Unlock()
	if stopped {
		return errors.New("imaging: libvips cannot be restarted after Shutdown")
	}
	if current != nil {
		return errors.New("imaging: already started")
	}

	// Read by vips_init: disables the loaders libvips itself marks as unsafe for untrusted input.
	if err := os.Setenv("VIPS_BLOCK_UNTRUSTED", "1"); err != nil {
		return fmt.Errorf("imaging: set VIPS_BLOCK_UNTRUSTED: %w", err)
	}
	// govips logs at info level by default; keep only errors and drop them
	// (every failure is also returned to the caller as an error).
	vips.LoggingSettings(func(string, vips.LogLevel, string) {}, vips.LogLevelError)
	// One libvips thread per pipeline, no operation cache: parallelism comes from Workers.
	if err := vips.Startup(&vips.Config{ConcurrencyLevel: 1, MaxCacheSize: 0, MaxCacheMem: 0, MaxCacheFiles: 0}); err != nil {
		return fmt.Errorf("imaging: start libvips: %w", err)
	}
	allowOnlyJPEG()

	tag := strconv.Itoa(PipelineVersion)
	if opt.Quality != DefaultQuality {
		tag = fmt.Sprintf("%dq%d", PipelineVersion, opt.Quality)
	}
	current = &state{quality: opt.Quality, tag: tag, slots: make(chan struct{}, opt.Workers), pixels: semaphore.NewWeighted(pixelBudget)}
	return nil
}

// Shutdown stops libvips after running pipelines have finished. No other function may be called afterwards.
func Shutdown() {
	mu.Lock()
	defer mu.Unlock()
	if current == nil {
		return
	}
	current = nil
	stopped = true
	vips.Shutdown()
}

// Version is the libvips version, e.g. "8.15.1", for the start-up log.
func Version() string { return vips.Version }

// Tag names the output of the running pipeline: "1" at quality 76, otherwise e.g. "1q80".
// It is the v of image URLs and the version in cache file names. Valid after Startup.
func Tag() string {
	mu.RLock()
	defer mu.RUnlock()
	if current == nil {
		return strconv.Itoa(PipelineVersion)
	}
	return current.tag
}

// Validate checks, in pure Go and before libvips sees the bytes, that data starts with FF D8 FF
// and that image/jpeg.DecodeConfig reports at most MaxSide per side and MaxPixels in total.
func Validate(data []byte) error {
	_, _, err := inspect(data)
	return err
}

// inspect is Validate that also returns the dimensions from the header.
func inspect(data []byte) (width, height int, err error) {
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF {
		return 0, 0, fmt.Errorf("%w: missing JPEG signature", ErrNotJPEG)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrNotJPEG, err)
	}
	w, h := cfg.Width, cfg.Height
	if w < 1 || h < 1 {
		return 0, 0, fmt.Errorf("%w: empty image (%dx%d)", ErrNotJPEG, w, h)
	}
	if w > MaxSide || h > MaxSide {
		return 0, 0, fmt.Errorf("%w: %dx%d, each side may be at most %d", ErrTooLarge, w, h, MaxSide)
	}
	if int64(w)*int64(h) > MaxPixels {
		return 0, 0, fmt.Errorf("%w: %dx%d is more than %d pixels", ErrTooLarge, w, h, MaxPixels)
	}
	return w, h, nil
}

// Thumbnail validates jpeg, then returns a square centre-cropped WebP of size x size pixels
// (smaller, still square, if the source is smaller: never upscaled). size must satisfy ValidSize.
// The JPEG is decoded with fail_on=error, so a truncated or corrupt body is an error, not a
// partly grey thumbnail. It waits for one of the Workers slots and for room in the pixel
// budget; ctx cancels the wait (returns ctx.Err()).
func Thumbnail(ctx context.Context, jpeg []byte, size int) ([]byte, error) {
	if !ValidSize(size) {
		return nil, fmt.Errorf("imaging: unsupported thumbnail size %d", size)
	}
	var out []byte
	err := pipeline(ctx, jpeg, func(w, h, quality int) (err error) {
		// vips_thumbnail enlarges small images to the requested box. Shrinking the
		// box to the shorter side keeps the output square without upscaling.
		out, err = render(jpeg, min(size, w, h), quality)
		return err
	})
	return out, err
}

// Verify validates jpeg and then decodes all of it with fail_on=error, without producing
// anything. It returns an error for a body that is truncated or corrupt even though its header
// is fine. Call it before an original is stored or served. It uses the same limits as Thumbnail.
func Verify(ctx context.Context, jpeg []byte) error {
	return pipeline(ctx, jpeg, func(w, h, _ int) error {
		ref, err := load(jpeg, min(verifySide, w, h))
		if err != nil {
			return err
		}
		defer ref.Close()
		// Reading the pixels runs the decoder over the whole file.
		if _, err := ref.ToBytes(); err != nil {
			return fmt.Errorf("imaging: decode: %w", err)
		}
		return nil
	})
}

// pipeline runs fn on a validated JPEG while holding a worker slot and the
// image's share of the pixel budget. fn gets the header's dimensions and the
// WebP quality.
func pipeline(ctx context.Context, jpeg []byte, fn func(w, h, quality int) error) error {
	mu.RLock()
	defer mu.RUnlock()
	st := current
	if st == nil {
		return ErrNotStarted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case st.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-st.slots }()

	// The header check runs inside the slot too, so a flood of requests cannot
	// run more than Workers header parses at once.
	w, h, err := inspect(jpeg)
	if err != nil {
		return err
	}
	// Validate has bounded w*h by MaxPixels, which is the whole budget, so this cannot wait for ever.
	weight := int64(w) * int64(h)
	if err := st.pixels.Acquire(ctx, weight); err != nil {
		return err
	}
	defer st.pixels.Release(weight)
	return fn(w, h, st.quality)
}

// load opens data as a sequential thumbnail pipeline: decode with
// shrink-on-load, resize, centre crop to side x side. The loader gets
// fail_on=error (govips spells it "fail"), so damage in the body is reported
// when the pixels are read rather than papered over.
func load(data []byte, side int) (*vips.ImageRef, error) {
	params := vips.NewImportParams()
	params.FailOnError.Set(true)
	ref, err := vips.LoadThumbnailFromBuffer(data, side, side, vips.InterestingCentre, vips.SizeBoth, params)
	if err != nil {
		return nil, fmt.Errorf("imaging: thumbnail: %w", err)
	}
	if ref.Width() != side || ref.Height() != side {
		defer ref.Close()
		return nil, fmt.Errorf("imaging: thumbnail is %dx%d, expected %dx%d", ref.Width(), ref.Height(), side, side)
	}
	return ref, nil
}

// render runs libvips: load (see load), then encode as WebP. The caller has
// validated data, holds a worker slot and holds mu for reading.
func render(data []byte, side, quality int) ([]byte, error) {
	// A thumbnail image is a sequential pipeline that can be rendered once,
	// so every output size starts again from the JPEG bytes.
	ref, err := load(data, side)
	if err != nil {
		return nil, err
	}
	defer ref.Close()
	out, _, err := ref.ExportWebp(&vips.WebpExportParams{
		Quality:         quality,
		StripMetadata:   true,
		ReductionEffort: reductionEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("imaging: encode WebP: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("imaging: encode WebP: empty output")
	}
	return out, nil
}

// Placeholder is a small solid WebP in Background colour, served when an image cannot be fetched.
// The returned slice must not be modified.
func Placeholder() []byte { return placeholder }
