// Package cache is the gallery's disk cache: the original JPEGs fetched from
// Telegram and the WebP thumbnails made from them.
//
//	originals:  <dir>/<key>.jpg
//	thumbnails: <dir>/<key>-<size>-v<version>.webp
//
// File names are built only from validated parts (a 22-character base64url
// key, a known size, a short lower-case version), so no input can name a file
// outside the directory.
package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/source"
)

const (
	tmpPrefix = ".tmp-"
	// touchEvery limits how often a read updates a file's mtime (which is how
	// recency survives a restart).
	touchEvery = time.Minute
	// reserveDivisor sets the share of the size limit that thumbnails may
	// always use: limit/reserveDivisor. See evictLocked.
	reserveDivisor = 8
)

// Errors returned by the Put methods for arguments that fail validation.
var (
	ErrBadKey     = errors.New("cache: invalid key")
	ErrBadSize    = errors.New("cache: invalid thumbnail size")
	ErrBadVersion = errors.New("cache: invalid version")
	ErrEmpty      = errors.New("cache: refusing to store an empty file")
)

type entry struct {
	key     string
	version string // "" for an original
	thumb   bool
	bytes   int64
	used    time.Time // last use; orders thumbnail eviction
	touched time.Time // mtime last written to disk
	seq     uint64    // tie-break for equal times: lower is older
}

// Cache is a disk cache under one directory. Safe for concurrent use.
// Every write goes to a temp file in the same directory and is renamed into place.
type Cache struct {
	dir string
	max int64
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*entry // by file name
	total   int64             // bytes of all files
	thumbs  int64             // bytes of the thumbnails among them
	seq     uint64
	over    bool // the "over the limit" warning has been logged and still applies
}

// Open creates dir (0700) if needed, removes leftover temp files and indexes what is there.
// maxBytes is the size limit for originals plus thumbnails (see evictLocked for the one
// case in which the directory may be larger). log may be nil.
func Open(dir string, maxBytes int64, log *slog.Logger) (*Cache, error) {
	if dir == "" {
		return nil, errors.New("cache: empty directory name")
	}
	if maxBytes < 0 {
		return nil, fmt.Errorf("cache: negative size limit %d", maxBytes)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	c := &Cache{dir: abs, max: maxBytes, log: log, now: time.Now, entries: map[string]*entry{}}

	list, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	type found struct {
		name string
		e    *entry
	}
	var all []found
	for _, d := range list {
		name := d.Name()
		if !d.Type().IsRegular() {
			continue // never follow links or descend
		}
		if strings.HasPrefix(name, tmpPrefix) {
			if err := os.Remove(filepath.Join(abs, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Warn("cache: remove leftover temp file", "error", err)
			}
			continue
		}
		e, ok := parseName(name)
		if !ok {
			continue // not ours: leave it alone and do not count it
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		if info.Size() == 0 { // a torn write from a crash; a Get would miss anyway
			_ = os.Remove(filepath.Join(abs, name))
			continue
		}
		e.bytes = info.Size()
		e.used, e.touched = info.ModTime(), info.ModTime()
		all = append(all, found{name, e})
	}
	// Give older files lower sequence numbers so ties in mtime evict in a stable order.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].e.used.Equal(all[j].e.used) {
			return all[i].e.used.Before(all[j].e.used)
		}
		return all[i].name < all[j].name
	})
	for _, f := range all {
		c.seq++
		f.e.seq = c.seq
		c.entries[f.name] = f.e
		c.add(f.e)
	}
	c.mu.Lock()
	c.evictLocked()
	c.mu.Unlock()
	return c, nil
}

func validVersion(v string) bool {
	if len(v) < 1 || len(v) > 16 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if ch := v[i]; !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z') {
			return false
		}
	}
	return true
}

func originalName(key string) string { return key + ".jpg" }

func thumbName(key string, size int, version string) string {
	return key + "-" + strconv.Itoa(size) + "-v" + version + ".webp"
}

// parseName recognises the two file name forms. A key may contain '-' and
// '_', so it is always the first source.KeyLen bytes.
func parseName(name string) (*entry, bool) {
	if len(name) <= source.KeyLen {
		return nil, false
	}
	key, rest := name[:source.KeyLen], name[source.KeyLen:]
	if !source.ValidKey(key) {
		return nil, false
	}
	if rest == ".jpg" {
		return &entry{key: key}, true
	}
	rest, ok := strings.CutSuffix(rest, ".webp")
	if !ok || !strings.HasPrefix(rest, "-") {
		return nil, false
	}
	sizeText, version, ok := strings.Cut(rest[1:], "-v")
	if !ok || !validVersion(version) {
		return nil, false
	}
	size, err := strconv.Atoi(sizeText)
	if err != nil || !imaging.ValidSize(size) || thumbName(key, size, version) != name {
		return nil, false
	}
	return &entry{key: key, version: version, thumb: true}, true
}

func checkThumb(key string, size int, version string) error {
	switch {
	case !source.ValidKey(key):
		return ErrBadKey
	case !imaging.ValidSize(size):
		return ErrBadSize
	case !validVersion(version):
		return ErrBadVersion
	}
	return nil
}

// GetOriginal returns the stored original JPEG.
func (c *Cache) GetOriginal(key string) ([]byte, bool) {
	if !source.ValidKey(key) {
		return nil, false
	}
	return c.get(originalName(key))
}

// PutOriginal stores an original JPEG. It may evict thumbnails to stay under the size limit.
func (c *Cache) PutOriginal(key string, data []byte) error {
	if !source.ValidKey(key) {
		return ErrBadKey
	}
	return c.put(originalName(key), &entry{key: key}, data)
}

// HasOriginal reports whether an original is stored, without reading it.
func (c *Cache) HasOriginal(key string) bool {
	return source.ValidKey(key) && c.has(originalName(key))
}

// GetThumb returns a stored thumbnail and marks it as recently used.
func (c *Cache) GetThumb(key string, size int, version string) ([]byte, bool) {
	if checkThumb(key, size, version) != nil {
		return nil, false
	}
	return c.get(thumbName(key, size, version))
}

// PutThumb stores a thumbnail. It may evict older thumbnails to stay under the size limit.
func (c *Cache) PutThumb(key string, size int, version string, data []byte) error {
	if err := checkThumb(key, size, version); err != nil {
		return err
	}
	return c.put(thumbName(key, size, version), &entry{key: key, version: version, thumb: true}, data)
}

// HasThumb reports whether a thumbnail is stored. It does not read the file or change its recency.
func (c *Cache) HasThumb(key string, size int, version string) bool {
	return checkThumb(key, size, version) == nil && c.has(thumbName(key, size, version))
}

// Size is the total bytes of all cached files (originals and thumbnails).
func (c *Cache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func (c *Cache) has(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[name]
	return ok
}

// get reads an indexed file. name is built from validated parts only.
func (c *Cache) get(name string) ([]byte, bool) {
	c.mu.Lock()
	_, ok := c.entries[name]
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	path := filepath.Join(c.dir, name)
	// No lock while reading: a concurrent replace is a rename (this read sees
	// the old or the new file, never a mix) and a concurrent eviction is an
	// unlink (this read finishes or misses).
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.log.Warn("cache: read failed", "file", name, "error", err)
		}
		c.mu.Lock()
		if e, ok := c.entries[name]; ok {
			// Forget a file that is gone or empty, so it is fetched or rendered
			// again. Checked again under the lock: a Put may just have replaced it.
			info, statErr := os.Stat(path)
			if statErr != nil || info.Size() == 0 {
				_ = os.Remove(path)
				c.sub(e)
				delete(c.entries, name)
			}
		}
		c.mu.Unlock()
		return nil, false
	}

	now := c.now()
	touch := false
	c.mu.Lock()
	if e, ok := c.entries[name]; ok {
		e.used = now
		c.seq++
		e.seq = c.seq
		if now.Sub(e.touched) >= touchEvery {
			e.touched = now
			touch = true
		}
	}
	c.mu.Unlock()
	if touch {
		_ = os.Chtimes(path, now, now) // best effort; only orders eviction after a restart
	}
	return data, true
}

// put writes data to a temp file in the cache directory, syncs it and renames
// it over name, so readers and crashes never see a partial file.
func (c *Cache) put(name string, e *entry, data []byte) error {
	if len(data) == 0 {
		return ErrEmpty
	}
	f, err := os.CreateTemp(c.dir, tmpPrefix+"*") // created 0600
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: write %s: %w", name, err)
	}

	now := c.now()
	e.bytes = int64(len(data))
	e.used, e.touched = now, now

	c.mu.Lock()
	defer c.mu.Unlock()
	// Rename under the lock so the index always describes the file that won.
	if err := os.Rename(tmp, filepath.Join(c.dir, name)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: write %s: %w", name, err)
	}
	if old, ok := c.entries[name]; ok {
		c.sub(old)
	}
	c.seq++
	e.seq = c.seq
	c.entries[name] = e
	c.add(e)
	c.evictLocked()
	return nil
}

func (c *Cache) add(e *entry) {
	c.total += e.bytes
	if e.thumb {
		c.thumbs += e.bytes
	}
}

func (c *Cache) sub(e *entry) {
	c.total -= e.bytes
	if e.thumb {
		c.thumbs -= e.bytes
	}
}

// reserve is the space thumbnails may use even when the originals fill the limit.
func (c *Cache) reserve() int64 { return c.max / reserveDivisor }

// evictLocked removes thumbnails, least recently used first, until the total
// fits the limit. Originals are never removed here (only Prune removes them,
// once their record is gone).
//
// Thumbnails are not evicted below the reserve, an eighth of the limit. So
// when the originals alone come close to the limit or pass it, the most
// recently used thumbnails stay, and the directory can grow to the originals
// plus the reserve. Without the reserve every thumbnail would be evicted the
// moment it was written and every tile request would decode its JPEG again.
func (c *Cache) evictLocked() {
	if c.total <= c.max {
		c.over = false
		return
	}
	reserve := c.reserve()
	removed := 0
	if c.thumbs > reserve {
		type cand struct {
			name string
			e    *entry
		}
		var thumbs []cand
		for name, e := range c.entries {
			if e.thumb {
				thumbs = append(thumbs, cand{name, e})
			}
		}
		sort.Slice(thumbs, func(i, j int) bool {
			a, b := thumbs[i].e, thumbs[j].e
			if !a.used.Equal(b.used) {
				return a.used.Before(b.used)
			}
			return a.seq < b.seq
		})
		for _, t := range thumbs {
			if c.total <= c.max || c.thumbs <= reserve {
				break
			}
			if c.removeLocked(t.name, t.e) == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		c.log.Debug("cache: evicted thumbnails", "files", removed, "bytes", c.total, "limit", c.max)
	}
	if c.total <= c.max {
		c.over = false
		return
	}
	if !c.over {
		// Logged once, when the state begins, not on every write.
		c.over = true
		c.log.Warn("cache: the originals leave no room under the size limit; keeping only the reserve of thumbnails. Raise GALLERY_CACHE_MAX_MB",
			"bytes", c.total, "originals", c.total-c.thumbs, "thumbnails", c.thumbs, "reserve", reserve, "limit", c.max)
	}
}

func (c *Cache) removeLocked(name string, e *entry) error {
	err := os.Remove(filepath.Join(c.dir, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Warn("cache: remove failed", "file", name, "error", err)
		return err
	}
	c.sub(e)
	delete(c.entries, name)
	return nil
}

// Prune removes every original and every thumbnail whose key is not live, and every thumbnail whose
// version is not version. It returns the number of files removed. Call it from a source.Store OnSwap
// hook (in its own goroutine) with live = snapshot.Has.
func (c *Cache) Prune(live func(key string) bool, version string) (removed int, err error) {
	if live == nil {
		return 0, errors.New("cache: Prune needs a live function")
	}
	if !validVersion(version) {
		return 0, ErrBadVersion
	}
	// Ask about each key once, without holding the lock.
	c.mu.Lock()
	keys := make(map[string]bool, len(c.entries))
	for _, e := range c.entries {
		keys[e.key] = false
	}
	c.mu.Unlock()
	for k := range keys {
		keys[k] = live(k)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for name, e := range c.entries {
		alive, asked := keys[e.key]
		if !asked {
			continue // stored after the keys were collected: decide next time
		}
		if alive && (!e.thumb || e.version == version) {
			continue
		}
		if rerr := c.removeLocked(name, e); rerr != nil {
			err = errors.Join(err, rerr)
			continue
		}
		removed++
	}
	if removed > 0 {
		c.log.Debug("cache: pruned", "files", removed, "bytes", c.total)
	}
	return removed, err
}
