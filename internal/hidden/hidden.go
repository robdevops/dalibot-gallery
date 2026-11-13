// Package hidden is the gallery's list of hidden images: the images an admin
// has removed from the gallery.
//
// The gallery never writes to the bot's data, so hiding is the gallery's own
// state, kept in one file:
//
//	<state dir>/hidden.json
//	{"version":1,"hidden":[{"key":"<22 characters>","at":<epoch seconds>}, …]}
//
// An entry names an image by its key (source.Key), the name the image has in
// URLs and in the cache. The list holds nothing else about the image.
//
// The file is state, not cache: nothing can make it again. Every change is
// written to a temporary file in the same directory, synced and renamed over
// the list, so a crash leaves either the old list or the new one. A file
// that cannot be understood is never overwritten: it is renamed to
// hidden.json.corrupt-<time> first, and the gallery goes on with an empty
// list.
package hidden

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/source"
)

const (
	// FileName is the list's name inside the state directory.
	FileName = "hidden.json"
	// MaxEntries bounds the list (and with it the file and the memory it takes).
	MaxEntries = 100_000

	version       = 1
	tmpPattern    = FileName + ".tmp-*"
	corruptPrefix = FileName + ".corrupt-"
	maxFileBytes  = 16 << 20 // far above MaxEntries entries
)

// Errors returned by Hide and Restore.
var (
	ErrBadKey = errors.New("hidden: invalid key")
	ErrFull   = errors.New("hidden: the list is full")
	// ErrReadOnly: the list on disk could not be understood and no copy of it
	// could be kept, so nothing is written over it.
	ErrReadOnly = errors.New("hidden: the list on disk is corrupt and could not be set aside; changes are refused")
)

// rename is os.Rename; a test replaces it to make setting a file aside fail.
var rename = os.Rename

// Entry is one hidden image.
type Entry struct {
	Key string `json:"key"`
	At  int64  `json:"at"` // epoch seconds it was hidden
}

type file struct {
	Version int     `json:"version"`
	Hidden  []Entry `json:"hidden"`
}

// List is the hidden list. Safe for concurrent use. What Has reports is
// always what the file on disk says: a change that cannot be written is not
// made.
type List struct {
	dir string
	log *slog.Logger
	now func() time.Time

	mu       sync.RWMutex
	at       map[string]int64 // key -> when hidden
	readOnly bool
}

// Open creates dir (0700) if needed and reads the list in it.
//
// A missing file is an empty list. A file that is not a list this version
// understands is renamed to hidden.json.corrupt-<time> and logged, and the
// list starts empty; if it cannot even be renamed, the list starts empty and
// refuses every change (ErrReadOnly), so the file is never overwritten.
//
// An error is returned only when the directory cannot be used or the file
// exists but cannot be read: then the gallery cannot know what is hidden,
// and showing everything would be the wrong guess. log may be nil.
func Open(dir string, log *slog.Logger) (*List, error) {
	if dir == "" {
		return nil, errors.New("hidden: empty directory name")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("hidden: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("hidden: %w", err)
	}
	l := &List{dir: abs, log: log, now: time.Now, at: map[string]int64{}}

	// Temporary files left by a crash in the middle of a write.
	if old, err := filepath.Glob(filepath.Join(abs, tmpPattern)); err == nil {
		for _, name := range old {
			_ = os.Remove(name)
		}
	}

	path := l.Path()
	data, err := readLimited(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hidden: reading the list of hidden images: %w", err)
	}
	entries, skipped, perr := parse(data)
	if perr != nil {
		aside := filepath.Join(abs, corruptPrefix+l.now().UTC().Format("20060102T150405.000000000Z"))
		if rerr := rename(path, aside); rerr != nil {
			l.readOnly = true
			log.Error("the list of hidden images is corrupt and could not be set aside; starting with nothing hidden, and hiding and restoring are refused until the file is repaired or removed",
				"file", path, "problem", perr, "error", rerr)
			return l, nil
		}
		log.Error("the list of hidden images is corrupt; it was kept under another name and the gallery starts with nothing hidden",
			"file", path, "kept_as", aside, "problem", perr)
		return l, nil
	}
	for _, e := range entries {
		l.at[e.Key] = e.At
	}
	if skipped > 0 {
		log.Warn("the list of hidden images has entries that are not image keys; they are ignored", "file", path, "entries", skipped)
	}
	return l, nil
}

func readLimited(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s is %d bytes, more than a list can be", path, info.Size())
	}
	return os.ReadFile(path)
}

// parse reads the file's content. Entries whose key is not an image key are
// counted in skipped and left out; anything else that is wrong is an error.
func parse(data []byte) (entries []Entry, skipped int, err error) {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, 0, err
	}
	if f.Version != version {
		return nil, 0, fmt.Errorf("version %d is not understood (this gallery writes version %d)", f.Version, version)
	}
	for _, e := range f.Hidden {
		if !source.ValidKey(e.Key) {
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	return entries, skipped, nil
}

// Path is the list's file.
func (l *List) Path() string { return filepath.Join(l.dir, FileName) }

// Has reports whether key is hidden. It is the function source.Options.Hidden wants.
func (l *List) Has(key string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.at[key]
	return ok
}

// At is when key was hidden, in epoch seconds; ok is false if it is not hidden.
func (l *List) At(key string) (at int64, ok bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	at, ok = l.at[key]
	return at, ok
}

// Len is the number of hidden images.
func (l *List) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.at)
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].At != es[j].At {
			return es[i].At > es[j].At
		}
		return es[i].Key < es[j].Key
	})
}

// Hide adds key to the list and writes the list. Hiding a hidden key changes nothing. On an error nothing has changed, in memory or on disk.
func (l *List) Hide(key string) error {
	if !source.ValidKey(key) {
		return ErrBadKey
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.at[key]; ok {
		return nil
	}
	if l.readOnly {
		return ErrReadOnly
	}
	if len(l.at) >= MaxEntries {
		return ErrFull
	}
	l.at[key] = l.now().Unix()
	if err := l.writeLocked(); err != nil {
		delete(l.at, key)
		return err
	}
	return nil
}

// Restore takes key off the list and writes the list. Restoring a key that is not hidden changes nothing. On an error nothing has changed, in memory or on disk.
func (l *List) Restore(key string) error {
	if !source.ValidKey(key) {
		return ErrBadKey
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.at[key]
	if !ok {
		return nil
	}
	if l.readOnly {
		return ErrReadOnly
	}
	delete(l.at, key)
	if err := l.writeLocked(); err != nil {
		l.at[key] = at
		return err
	}
	return nil
}

// writeLocked writes the list to a temporary file next to it, syncs it and
// renames it into place, then syncs the directory, so that neither a reader
// nor a crash ever sees part of a list.
func (l *List) writeLocked() error {
	f := file{Version: version, Hidden: make([]Entry, 0, len(l.at))}
	for k, at := range l.at {
		f.Hidden = append(f.Hidden, Entry{Key: k, At: at})
	}
	sortEntries(f.Hidden) // a stable file for the same list
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("hidden: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(l.dir, tmpPattern) // created 0600
	if err != nil {
		return fmt.Errorf("hidden: %w", err)
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, l.Path())
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("hidden: writing the list: %w", err)
	}
	// The rename itself must survive a power cut. A file system that cannot
	// sync a directory still has the list in place.
	if d, err := os.Open(l.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
