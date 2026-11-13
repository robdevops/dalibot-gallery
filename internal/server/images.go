package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/source"
	"github.com/robdevops/dalibot-gallery/internal/telegram"
)

const (
	// Images never change under their URL. When viewing needs the token they
	// are "private": only the browser may keep them, never a shared cache,
	// which could hand them to someone who is not signed in. In public mode
	// (GALLERY_PUBLIC=1) anyone may see them and they are sent as "public".
	immutablePrivate = "private, max-age=31536000, immutable"
	immutablePublic  = "public, max-age=31536000, immutable"
	// jobTimeout bounds one shared image job (Telegram fetch plus thumbnail); it is under WriteTimeout.
	jobTimeout = 50 * time.Second
	// invalidTTL is how long an original that is not a usable JPEG is remembered.
	invalidTTL  = 24 * time.Hour
	maxInvalid  = 20000
	warmWorkers = 4
)

var (
	reThumb    = regexp.MustCompile(`^([A-Za-z0-9_-]{22})-(240|480)\.webp$`)
	reOriginal = regexp.MustCompile(`^([A-Za-z0-9_-]{22})\.jpg$`)

	errUnavailable = errors.New("image unavailable")
	errClosed      = errors.New("server is shutting down")
)

// failures remembers keys whose original cannot be used, so that a file
// Telegram serves but that is not a valid JPEG is not downloaded on every request.
type failures struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func (f *failures) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.until[key]
	if ok && time.Now().After(t) {
		delete(f.until, key)
		return false
	}
	return ok
}

func (f *failures) add(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.until == nil || len(f.until) >= maxInvalid {
		f.until = map[string]time.Time{}
	}
	f.until[key] = time.Now().Add(invalidTTL)
}

// handleImage serves /img/<key>-<size>.webp and /img/<key>.jpg. Only keys in
// the current snapshot are served, so the route cannot be used to fetch
// arbitrary files.
//
// A hidden image is not in the listing, and its routes answer exactly as for
// a key that never existed. The one exception is an admin, who has to see
// what there is to restore: an admin is served a hidden image, uncacheable
// (no-store, no ETag), so that no cache, shared or the admin's own, keeps a
// copy under the image's public URL.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request, name string, admin bool) {
	snap := s.store.Snapshot()
	// served: listed, or hidden and asked for by an admin.
	served := func(key string) (ok, hidden bool) {
		if snap.Has(key) {
			return true, false
		}
		if admin && snap.HasHidden(key) {
			return true, true
		}
		return false, false
	}
	if m := reThumb.FindStringSubmatch(name); m != nil {
		key := m[1]
		size, _ := strconv.Atoi(m[2])
		ok, hid := served(key)
		if !ok || r.URL.Query().Get("v") != s.tag {
			plain(w, http.StatusNotFound, "not found\n")
			return
		}
		etag := `"` + key + "-" + m[2] + "-v" + s.tag + `"`
		if !hid && s.notModified(w, r, etag) {
			return
		}
		data, err := s.thumbnail(r.Context(), snap, key, size)
		if err != nil {
			// Not cacheable: the image may be back on the next request.
			h := w.Header()
			h.Set("X-Gallery-Placeholder", "1")
			h.Set("Cache-Control", "no-store")
			writeImage(w, "image/webp", imaging.Placeholder())
			return
		}
		if hid {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", s.immutable())
		}
		writeImage(w, "image/webp", data)
		return
	}
	if m := reOriginal.FindStringSubmatch(name); m != nil {
		key := m[1]
		ok, hid := served(key)
		if !ok {
			plain(w, http.StatusNotFound, "not found\n")
			return
		}
		etag := `"` + key + `"`
		if !hid && s.notModified(w, r, etag) {
			return
		}
		data, err := s.original(r.Context(), snap, key)
		if err != nil {
			plain(w, http.StatusBadGateway, "image unavailable\n")
			return
		}
		if hid {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", s.immutable())
		}
		writeImage(w, "image/jpeg", data)
		return
	}
	plain(w, http.StatusNotFound, "not found\n")
}

// immutable is the Cache-Control of an image response.
func (s *Server) immutable() string {
	if s.cfg.Public {
		return immutablePublic
	}
	return immutablePrivate
}

// notModified answers 304 when If-None-Match names etag. Nothing is read or generated.
func (s *Server) notModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	if !etagMatch(r.Header.Get("If-None-Match"), etag) {
		return false
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", s.immutable())
	w.WriteHeader(http.StatusNotModified)
	return true
}

func writeImage(w http.ResponseWriter, ctype string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// shared runs fn once for all concurrent callers with the same name. The job
// runs under the server's context, not the caller's: one caller going away
// does not fail the others. A caller whose ctx ends stops waiting.
func (s *Server) shared(ctx context.Context, g *singleflight.Group, name string, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	ch := g.DoChan(name, func() (any, error) {
		if !s.begin() {
			return nil, errClosed
		}
		defer s.wg.Done()
		jctx, cancel := context.WithTimeout(s.ctx, jobTimeout)
		defer cancel()
		return fn(jctx)
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.([]byte), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// original returns the original JPEG of a key in snap: from the disk cache,
// or else fetched from Telegram once, checked and stored. The check is the
// header validation and then a full decode with fail_on=error, so a file
// whose body is truncated or corrupt is never stored or served. The stored
// copy outlives a file_id that later stops working.
func (s *Server) original(ctx context.Context, snap *source.Snapshot, key string) ([]byte, error) {
	if data, ok := s.cache.GetOriginal(key); ok {
		return data, nil
	}
	if s.invalid.has(key) {
		return nil, errUnavailable
	}
	return s.shared(ctx, &s.origins, key, func(ctx context.Context) ([]byte, error) {
		if data, ok := s.cache.GetOriginal(key); ok {
			return data, nil
		}
		fileID, ok := snap.FileID(key)
		if !ok {
			return nil, errUnavailable
		}
		data, err := s.fetcher.Fetch(ctx, fileID)
		if err != nil {
			// The fetcher's errors are already free of the token, URLs and ids; log the key only.
			// A replay from the fetcher's failure cache is not news: the first failure was logged.
			level := slog.LevelWarn
			if te, ok := errors.AsType[*telegram.Error](err); ok && te.Cached {
				level = slog.LevelDebug
			}
			s.log.Log(ctx, level, "fetching an original failed", "key", key, "error", safeError(err))
			return nil, errUnavailable
		}
		if err := imaging.Verify(ctx, data); err != nil {
			if ctx.Err() != nil {
				return nil, errUnavailable // interrupted, not judged
			}
			s.invalid.add(key)
			s.log.Warn("an original is not a usable JPEG", "key", key, "bytes", len(data), "error", err)
			return nil, errUnavailable
		}
		if err := s.cache.PutOriginal(key, data); err != nil {
			s.log.Error("storing an original failed", "key", key, "error", err)
		}
		return data, nil
	})
}

// thumbnail returns the WebP thumbnail of a key in snap: from the disk cache,
// or else generated once from the original and stored.
func (s *Server) thumbnail(ctx context.Context, snap *source.Snapshot, key string, size int) ([]byte, error) {
	if data, ok := s.cache.GetThumb(key, size, s.tag); ok {
		return data, nil
	}
	return s.shared(ctx, &s.thumbs, key+"-"+strconv.Itoa(size), func(ctx context.Context) ([]byte, error) {
		if data, ok := s.cache.GetThumb(key, size, s.tag); ok {
			return data, nil
		}
		orig, err := s.original(ctx, snap, key)
		if err != nil {
			return nil, err
		}
		data, err := imaging.Thumbnail(ctx, orig, size)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("making a thumbnail failed", "key", key, "size", size, "error", err)
			}
			return nil, errUnavailable
		}
		// The cache may evict at once when it is full, so the bytes just made are what is served.
		if err := s.cache.PutThumb(key, size, s.tag, data); err != nil {
			s.log.Error("storing a thumbnail failed", "key", key, "size", size, "error", err)
		}
		return data, nil
	})
}

// Warm generates the missing thumbnails of the newest Config.Warm tiles of
// snap, so the first page is served from the cache. It uses the same paths as
// a request, and therefore the same Telegram and libvips limits. It returns
// when done or when ctx or the server is closed.
func (s *Server) Warm(ctx context.Context, snap *source.Snapshot) {
	if s.cfg.Warm <= 0 || snap == nil {
		return
	}
	start := time.Now()
	var mu sync.Mutex
	made, failed := 0, 0
	var g errgroup.Group
	g.SetLimit(min(warmWorkers, max(1, s.cfg.ImageWorkers)))
	for _, t := range snap.Newest(s.cfg.Warm) {
		for _, size := range imaging.Sizes {
			if s.cache.HasThumb(t.Key, size, s.tag) {
				continue
			}
			if ctx.Err() != nil || s.ctx.Err() != nil {
				break
			}
			g.Go(func() error {
				if ctx.Err() != nil || s.ctx.Err() != nil {
					return nil
				}
				_, err := s.thumbnail(ctx, snap, t.Key, size)
				mu.Lock()
				if err != nil {
					failed++
				} else {
					made++
				}
				mu.Unlock()
				return nil
			})
		}
	}
	_ = g.Wait()
	if made+failed > 0 {
		// A cycle that made nothing only repeats failures that were logged when they first happened.
		level := slog.LevelInfo
		if made == 0 {
			level = slog.LevelDebug
		}
		s.log.Log(ctx, level, "thumbnails warmed", "made", made, "failed", failed, "duration", time.Since(start).Round(time.Millisecond))
	}
}

// OnSwap is the hook for source.Store.OnSwap. It returns at once; in the
// background it warms the newest tiles of the new snapshot (cancelling a
// warm-up still running for an older one) and prunes cache files whose key is
// no longer in the bot's records or whose pipeline version is old. The files
// of a hidden image are kept while its record lasts: restoring it then needs
// no second download, which Telegram may no longer allow.
func (s *Server) OnSwap(snap *source.Snapshot) {
	s.swapMu.Lock()
	defer s.swapMu.Unlock()
	if s.warmCancel != nil {
		s.warmCancel()
		s.warmCancel = nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	if s.spawn(func() { defer cancel(); s.Warm(ctx, snap) }) {
		s.warmCancel = cancel
	} else {
		cancel() // closed: nothing was started
	}
	if !s.pruning {
		s.pruning = s.spawn(func() {
			removed, err := s.cache.Prune(snap.Known, s.tag)
			if err != nil {
				s.log.Error("pruning the cache failed", "error", err)
			} else if removed > 0 {
				s.log.Info("cache pruned", "removed", removed, "bytes", s.cache.Size())
			}
			s.swapMu.Lock()
			s.pruning = false
			s.swapMu.Unlock()
		})
	}
}
