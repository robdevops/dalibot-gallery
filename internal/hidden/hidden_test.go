package hidden

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/source"
)

func key(i int) string { return source.Key(fmt.Sprintf("file-%d", i)) }

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func open(t *testing.T, dir string) (*List, *logBuf) {
	t.Helper()
	logs := &logBuf{}
	l, err := Open(dir, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return l, logs
}

// names lists the directory, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range list {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out
}

func TestHideRestorePersist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "deep") // created on Open
	l, logs := open(t, dir)
	if l.Len() != 0 || l.Has(key(1)) || len(entries(l)) != 0 {
		t.Fatal("a new list is not empty")
	}
	if got := names(t, dir); len(got) != 0 {
		t.Fatalf("opening wrote something: %v", got)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", info, err)
	}
	clock := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return clock }

	for i := 1; i <= 3; i++ {
		if err := l.Hide(key(i)); err != nil {
			t.Fatalf("Hide %d: %v", i, err)
		}
		clock = clock.Add(time.Minute)
	}
	// Again is not a change, and keeps the first time.
	if err := l.Hide(key(1)); err != nil || l.Len() != 3 {
		t.Fatalf("hiding twice: %v, %d entries", err, l.Len())
	}
	if at, ok := l.At(key(1)); !ok || at != 1_700_000_000 {
		t.Errorf("At = %d %v", at, ok)
	}
	if _, ok := l.At(key(9)); ok {
		t.Error("At reports a key that is not hidden")
	}
	// Most recently hidden first.
	es := entries(l)
	if len(es) != 3 || es[0].Key != key(3) || es[2].Key != key(1) || es[0].At != 1_700_000_120 {
		t.Errorf("Entries = %+v", es)
	}
	// The file: mode 0600, the documented shape, nothing else in the directory.
	if got := names(t, dir); len(got) != 1 || got[0] != FileName {
		t.Fatalf("directory holds %v", got)
	}
	info, err := os.Stat(l.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", info, err)
	}
	raw, _ := os.ReadFile(l.Path())
	var f struct {
		Version int
		Hidden  []struct {
			Key string
			At  int64
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != 1 || len(f.Hidden) != 3 || f.Hidden[0].Key != key(3) || !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatalf("file content: %s (%v)", raw, err)
	}

	// Restore, and restore again.
	if err := l.Restore(key(2)); err != nil || l.Has(key(2)) {
		t.Fatalf("Restore: %v", err)
	}
	if err := l.Restore(key(2)); err != nil {
		t.Fatalf("restoring twice: %v", err)
	}
	if l.Has(key(2)) || !l.Has(key(1)) || !l.Has(key(3)) || l.Len() != 2 {
		t.Error("wrong list after Restore")
	}

	// Keys that are not keys are refused and never reach the file.
	for _, bad := range []string{"", "short", key(1) + "x", "../../../../etc/passwd!!", strings.Repeat("é", 11)} {
		if err := l.Hide(bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("Hide(%q): %v", bad, err)
		}
		if err := l.Restore(bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("Restore(%q): %v", bad, err)
		}
	}

	// A new process sees the same list, times included.
	again, _ := open(t, dir)
	if again.Len() != 2 || !again.Has(key(1)) || !again.Has(key(3)) || again.Has(key(2)) {
		t.Errorf("after reopening: %+v", entries(again))
	}
	if at, _ := again.At(key(3)); at != 1_700_000_120 {
		t.Errorf("time after reopening: %d", at)
	}
	if s := logs.String(); s != "" {
		t.Errorf("unexpected log: %s", s)
	}
}

// A write is a new file renamed over the old one: the old list is whole
// until the new one is, a failed write changes nothing, and nothing is left behind.
func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	l, _ := open(t, dir)
	if err := l.Hide(key(1)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(l.Path())
	inode := func() os.FileInfo {
		info, err := os.Stat(l.Path())
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	old := inode()
	if err := l.Hide(key(2)); err != nil {
		t.Fatal(err)
	}
	if os.SameFile(old, inode()) {
		t.Error("the list was rewritten in place, not replaced by a rename")
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("temporary files left behind: %v", got)
	}

	// A directory that cannot be written to (as on a read-only volume).
	// Root ignores directory modes, so the test makes the write fail another
	// way too: the list's own name is taken by a directory.
	good, _ := os.ReadFile(l.Path())
	if os.Geteuid() != 0 {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := l.Hide(key(3)); err == nil {
			t.Error("Hide succeeded in a read-only directory")
		}
		if err := l.Restore(key(1)); err == nil {
			t.Error("Restore succeeded in a read-only directory")
		}
		_ = os.Chmod(dir, 0o700)
	}
	if err := os.Remove(l.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(l.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Path(), "x"), nil, 0o600); err != nil { // so that the rename cannot replace it
		t.Fatal(err)
	}
	if err := l.Hide(key(3)); err == nil {
		t.Errorf("Hide with the rename failing: %v", err)
	}
	if err := l.Restore(key(1)); err == nil {
		t.Errorf("Restore with the rename failing: %v", err)
	}
	// Memory still says what the last good file said.
	if !l.Has(key(1)) || !l.Has(key(2)) || l.Has(key(3)) || l.Len() != 2 {
		t.Errorf("a failed write changed the list: %+v", entries(l))
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("a failed write left files behind: %v", got)
	}
	if err := os.RemoveAll(l.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Path(), good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Hide(key(3)); err != nil {
		t.Fatalf("Hide after the directory was repaired: %v", err)
	}
	if bytes.Equal(before, good) {
		t.Fatal("the test's two versions of the file are the same")
	}

	// A crash between writing the temporary file and renaming it leaves the
	// old list and a temporary file, which the next start removes.
	tmp := filepath.Join(dir, FileName+".tmp-123456")
	if err := os.WriteFile(tmp, []byte(`{"version":1,"hidden":[{"key":"`+key(9)+`","at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	again, logs := open(t, dir)
	if again.Len() != 3 || again.Has(key(9)) {
		t.Errorf("after a crash: %+v", entries(again))
	}
	if got := names(t, dir); len(got) != 1 || got[0] != FileName {
		t.Errorf("after a crash the directory holds %v", got)
	}
	if s := logs.String(); s != "" {
		t.Errorf("unexpected log: %s", s)
	}
}

// A file that cannot be understood: the gallery starts with an empty list,
// says so, and the file is kept under another name, byte for byte, before
// anything is written.
func TestCorruptFile(t *testing.T) {
	for name, content := range map[string]string{
		"empty":           "",
		"not JSON":        "\x00\x01 not json",
		"truncated":       `{"version":1,"hidden":[{"key":"` + key(1) + `","at":17`,
		"another shape":   `["` + key(1) + `"]`,
		"a later version": `{"version":2,"hidden":[{"key":"` + key(1) + `","at":1}]}`,
		"no version":      `{"hidden":[{"key":"` + key(1) + `","at":1}]}`,
		"wrong types":     `{"version":1,"hidden":[{"key":17,"at":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			l, logs := open(t, dir)
			if l.Len() != 0 || l.Has(key(1)) {
				t.Errorf("the list is not empty: %+v", entries(l))
			}
			if s := logs.String(); !strings.Contains(s, "level=ERROR") || !strings.Contains(s, "corrupt") || !strings.Contains(s, "kept_as=") {
				t.Errorf("log: %s", s)
			}
			got := names(t, dir)
			if len(got) != 1 || !strings.HasPrefix(got[0], FileName+".corrupt-") {
				t.Fatalf("directory after opening: %v", got)
			}
			kept, _ := os.ReadFile(filepath.Join(dir, got[0]))
			if string(kept) != content {
				t.Errorf("the copy differs from the corrupt file: %q", kept)
			}
			// The list works, and writing it leaves the copy alone.
			if err := l.Hide(key(5)); err != nil {
				t.Fatal(err)
			}
			kept, _ = os.ReadFile(filepath.Join(dir, got[0]))
			if string(kept) != content || len(names(t, dir)) != 2 {
				t.Errorf("after a write: copy %q, directory %v", kept, names(t, dir))
			}
			// The next start is an ordinary one.
			again, logs := open(t, dir)
			if again.Len() != 1 || !again.Has(key(5)) || logs.String() != "" {
				t.Errorf("after reopening: %+v, log %q", entries(again), logs.String())
			}
		})
	}
}

// If the corrupt file cannot even be set aside, it is not overwritten: the
// list is empty and refuses every change.
func TestCorruptFileThatCannotBeMoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	const content = "{ this is not a list"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// As when the directory is read-only (which the tests cannot rely on: root ignores it).
	rename = func(string, string) error { return errors.New("read-only file system") }
	l, logs := open(t, dir)
	rename = os.Rename // even now that it could, it must not write
	if l.Len() != 0 {
		t.Error("the list is not empty")
	}
	if s := logs.String(); !strings.Contains(s, "level=ERROR") || !strings.Contains(s, "could not be set aside") {
		t.Errorf("log: %s", s)
	}
	if err := l.Hide(key(1)); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Hide: %v", err)
	}
	if err := l.Restore(key(1)); err != nil {
		t.Errorf("restoring what is not hidden: %v", err)
	}
	if l.Has(key(1)) {
		t.Error("a refused Hide changed the list")
	}
	if raw, _ := os.ReadFile(path); string(raw) != content {
		t.Errorf("the corrupt file was overwritten: %q", raw)
	}
	if got := names(t, dir); len(got) != 1 || got[0] != FileName {
		t.Errorf("the directory was changed: %v", got)
	}
}

// Entries that are not image keys are ignored; the rest of the list counts.
func TestOddEntries(t *testing.T) {
	dir := t.TempDir()
	content := `{"version":1,"hidden":[{"key":"` + key(1) + `","at":5},{"key":"../x","at":6},{"key":"","at":7},{"key":"` + key(2) + `"}],"later":"field"}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	l, logs := open(t, dir)
	if l.Len() != 2 || !l.Has(key(1)) || !l.Has(key(2)) {
		t.Errorf("list: %+v", entries(l))
	}
	if s := logs.String(); !strings.Contains(s, "level=WARN") || !strings.Contains(s, "entries=2") {
		t.Errorf("log: %s", s)
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("a list with odd entries was treated as corrupt: %v", got)
	}
}

// What cannot be read at all is an error, not an empty list: the gallery
// must not guess that nothing is hidden.
func TestUnreadable(t *testing.T) {
	if _, err := Open("", nil); err == nil {
		t.Error("an empty directory name was accepted")
	}
	// The directory's name is taken by a file.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file, nil); err == nil {
		t.Error("a file was accepted as the state directory")
	}
	// The list's name is taken by a directory.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, nil); err == nil {
		t.Error("a directory was accepted as the list")
	}
	if got := names(t, dir); len(got) != 1 || got[0] != FileName {
		t.Errorf("the directory was changed: %v", got)
	}
	// A list this process may not read.
	if os.Geteuid() != 0 {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(`{"version":1,"hidden":[]}`), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, nil); err == nil {
			t.Error("an unreadable list was accepted")
		}
		if got := names(t, dir); len(got) != 1 || got[0] != FileName {
			t.Errorf("the directory was changed: %v", got)
		}
	}
}

func TestFull(t *testing.T) {
	dir := t.TempDir()
	f := file{Version: version}
	for i := range MaxEntries {
		f.Hidden = append(f.Hidden, Entry{Key: key(i), At: int64(i)})
	}
	raw, _ := json.Marshal(f)
	if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := open(t, dir)
	if l.Len() != MaxEntries {
		t.Fatalf("Len = %d", l.Len())
	}
	if err := l.Hide(key(MaxEntries)); !errors.Is(err, ErrFull) {
		t.Errorf("Hide on a full list: %v", err)
	}
	if err := l.Hide(key(0)); err != nil {
		t.Errorf("hiding a hidden key on a full list: %v", err)
	}
	if err := l.Restore(key(0)); err != nil {
		t.Fatal(err)
	}
	if err := l.Hide(key(MaxEntries)); err != nil {
		t.Errorf("Hide after making room: %v", err)
	}
}

// Many goroutines hiding, restoring and reading at once: no race (the suite
// runs with -race), every acknowledged change is in the list and in the
// file, and a reader of the file never sees part of a list.
func TestConcurrent(t *testing.T) {
	dir := t.TempDir()
	l, _ := open(t, dir)
	const workers, each = 8, 25
	stop := make(chan struct{})
	var readers sync.WaitGroup
	torn := make(chan string, 1)
	for range 2 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				raw, err := os.ReadFile(l.Path())
				if err != nil {
					continue // not written yet
				}
				if _, _, err := parse(raw); err != nil {
					select {
					case torn <- fmt.Sprintf("%v in %q", err, raw):
					default:
					}
					return
				}
				_ = l.Has(key(1))
				_ = entries(l)
			}
		})
	}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				k := key(w*1000 + i)
				if err := l.Hide(k); err != nil {
					t.Error(err)
				}
				if i%5 == 4 { // every fifth is restored again
					if err := l.Restore(k); err != nil {
						t.Error(err)
					}
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	select {
	case msg := <-torn:
		t.Fatalf("a reader saw a torn file: %s", msg)
	default:
	}
	want := workers * each * 4 / 5
	if l.Len() != want {
		t.Errorf("Len = %d, want %d", l.Len(), want)
	}
	again, _ := open(t, dir)
	if again.Len() != want {
		t.Errorf("after reopening: %d, want %d", again.Len(), want)
	}
	for w := range workers {
		for i := range each {
			if got := again.Has(key(w*1000 + i)); got != (i%5 != 4) {
				t.Errorf("key %d/%d hidden = %v", w, i, got)
			}
		}
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("temporary files left behind: %v", got)
	}
}

// entries is the whole list, most recently hidden first.
func entries(l *List) []Entry {
	l.mu.RLock()
	out := make([]Entry, 0, len(l.at))
	for k, at := range l.at {
		out = append(out, Entry{Key: k, At: at})
	}
	l.mu.RUnlock()
	sortEntries(out)
	return out
}
