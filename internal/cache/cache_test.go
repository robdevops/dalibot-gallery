package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/source"
)

const v1 = "1"

func key(i int) string { return source.Key(fmt.Sprintf("file-%d", i)) }

func open(t *testing.T, dir string, max int64) *Cache {
	t.Helper()
	c, err := Open(dir, max, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// clock gives every operation a distinct, increasing time.
func clock(c *Cache) {
	var mu sync.Mutex
	now := time.Now()
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}
}

func blob(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }

func files(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range list {
		out = append(out, d.Name())
	}
	return out
}

func diskBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	for _, name := range files(t, dir) {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		n += info.Size()
	}
	return n
}

func TestRoundTripAndNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	c := open(t, dir, 1<<20)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", info, err)
	}
	k := key(1)
	if _, ok := c.GetOriginal(k); ok {
		t.Fatal("hit in an empty cache")
	}
	if c.HasOriginal(k) || c.HasThumb(k, 480, v1) {
		t.Fatal("Has in an empty cache")
	}
	orig, t480, t240 := blob(1000, 'o'), blob(300, 'a'), blob(100, 'b')
	if err := c.PutOriginal(k, orig); err != nil {
		t.Fatal(err)
	}
	if err := c.PutThumb(k, 480, v1, t480); err != nil {
		t.Fatal(err)
	}
	if err := c.PutThumb(k, 240, "1q80", t240); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.GetOriginal(k); !ok || !bytes.Equal(got, orig) {
		t.Fatal("original differs")
	}
	if got, ok := c.GetThumb(k, 480, v1); !ok || !bytes.Equal(got, t480) {
		t.Fatal("480 differs")
	}
	if got, ok := c.GetThumb(k, 240, "1q80"); !ok || !bytes.Equal(got, t240) {
		t.Fatal("240 differs")
	}
	if _, ok := c.GetThumb(k, 240, v1); ok {
		t.Fatal("hit for another version")
	}
	if !c.HasOriginal(k) || !c.HasThumb(k, 480, v1) || c.HasThumb(k, 240, v1) {
		t.Fatal("Has is wrong")
	}
	want := []string{k + "-240-v1q80.webp", k + "-480-v1.webp", k + ".jpg"}
	got := files(t, dir)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, name := range want {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", name, info.Mode().Perm())
		}
	}
	if c.Size() != 1400 || diskBytes(t, dir) != 1400 {
		t.Fatalf("Size = %d, disk = %d, want 1400", c.Size(), diskBytes(t, dir))
	}
	// Replacing a file adjusts the total.
	if err := c.PutThumb(k, 480, v1, blob(50, 'c')); err != nil {
		t.Fatal(err)
	}
	if c.Size() != 1150 {
		t.Fatalf("Size after replace = %d", c.Size())
	}
}

// The file name scheme is the documented one: key = base64url(sha256(file_id))[:22].
func TestKeyNaming(t *testing.T) {
	sum := sha256.Sum256([]byte("AgACAgUAAxkBAAIB"))
	k := base64.RawURLEncoding.EncodeToString(sum[:])[:22]
	if k != source.Key("AgACAgUAAxkBAAIB") {
		t.Fatal("key derivation differs from source.Key")
	}
	dir := t.TempDir()
	c := open(t, dir, 1<<20)
	if err := c.PutOriginal(k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, k+".jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsBadArguments(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "cache")
	c := open(t, dir, 1<<20)
	secret := filepath.Join(parent, "secret.jpg")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := key(1)
	badKeys := []string{
		"", "short", good + "x", good[:21],
		"../secret", "../../../../etc/passwd", "..%2Fsecret",
		strings.Repeat(".", 22), "../" + good[:19], good[:21] + "/", good[:21] + "\\",
		good[:21] + ".", good[:21] + "\x00", good[:21] + " ", good[:20] + "é",
		"/" + good[:21], "a/b/c/d/e/f/g/h/i/j/k/", filepath.Join("..", "secret.jpg"),
	}
	for _, k := range badKeys {
		if err := c.PutOriginal(k, []byte("evil")); !errors.Is(err, ErrBadKey) {
			t.Errorf("PutOriginal(%q) = %v", k, err)
		}
		if err := c.PutThumb(k, 480, v1, []byte("evil")); !errors.Is(err, ErrBadKey) {
			t.Errorf("PutThumb(%q) = %v", k, err)
		}
		if _, ok := c.GetOriginal(k); ok {
			t.Errorf("GetOriginal(%q) hit", k)
		}
		if _, ok := c.GetThumb(k, 480, v1); ok {
			t.Errorf("GetThumb(%q) hit", k)
		}
		if c.HasOriginal(k) || c.HasThumb(k, 480, v1) {
			t.Errorf("Has(%q)", k)
		}
	}
	for _, size := range []int{0, -480, 1, 241, 479, 960, 4800} {
		if err := c.PutThumb(good, size, v1, []byte("x")); !errors.Is(err, ErrBadSize) {
			t.Errorf("PutThumb size %d = %v", size, err)
		}
		if _, ok := c.GetThumb(good, size, v1); ok || c.HasThumb(good, size, v1) {
			t.Errorf("size %d hit", size)
		}
	}
	for _, ver := range []string{"", "V1", "1.0", "../x", "1/2", "1 ", "a-b", "é", strings.Repeat("1", 17), "1\x00"} {
		if err := c.PutThumb(good, 480, ver, []byte("x")); !errors.Is(err, ErrBadVersion) {
			t.Errorf("PutThumb version %q = %v", ver, err)
		}
		if _, ok := c.GetThumb(good, 480, ver); ok || c.HasThumb(good, 480, ver) {
			t.Errorf("version %q hit", ver)
		}
	}
	if err := c.PutOriginal(good, nil); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty Put = %v", err)
	}
	// Nothing was written anywhere, inside or outside the directory.
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("cache directory holds %v", got)
	}
	if got := files(t, parent); len(got) != 2 {
		t.Errorf("parent directory holds %v", got)
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "outside" {
		t.Errorf("file outside the cache changed: %q %v", data, err)
	}
	if c.Size() != 0 {
		t.Errorf("Size = %d", c.Size())
	}
}

func TestOpenIndexesAndCleans(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 1<<20)
	k1, k2 := key(1), key(2)
	if err := c.PutOriginal(k1, blob(500, 'o')); err != nil {
		t.Fatal(err)
	}
	if err := c.PutThumb(k1, 480, v1, blob(200, 't')); err != nil {
		t.Fatal(err)
	}
	// Left behind by a crash or by something else:
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".tmp-123456", blob(999, 'x'))        // interrupted write
	write(k2+".jpg", nil)                       // torn, empty
	write("notes.txt", []byte("keep me"))       // unrelated
	write(k2+"-999-v1.webp", []byte("unknown")) // not a size we make
	write(k2+"-480-vBAD.webp", []byte("bad"))   // not a version we make
	if err := os.Mkdir(filepath.Join(dir, k2+"-240-v1.webp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "notes.txt"), filepath.Join(dir, key(3)+".jpg")); err != nil {
		t.Fatal(err)
	}

	c = open(t, dir, 1<<20)
	if c.Size() != 700 {
		t.Fatalf("Size after reopen = %d, want 700", c.Size())
	}
	if got, ok := c.GetOriginal(k1); !ok || len(got) != 500 {
		t.Fatal("original lost over reopen")
	}
	if !c.HasThumb(k1, 480, v1) {
		t.Fatal("thumbnail lost over reopen")
	}
	if c.HasOriginal(k2) || c.HasThumb(k2, 240, v1) || c.HasOriginal(key(3)) {
		t.Fatal("indexed something that is not a cache file")
	}
	if _, ok := c.GetOriginal(key(3)); ok {
		t.Fatal("followed a symlink")
	}
	left := strings.Join(files(t, dir), " ")
	if strings.Contains(left, ".tmp-") {
		t.Errorf("temp file not removed: %s", left)
	}
	if strings.Contains(left, k2+".jpg") {
		t.Errorf("empty file not removed: %s", left)
	}
	if !strings.Contains(left, "notes.txt") {
		t.Errorf("unrelated file removed: %s", left)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open("", 1, nil); err == nil {
		t.Error("empty dir accepted")
	}
	if _, err := Open(t.TempDir(), -1, nil); err == nil {
		t.Error("negative limit accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file, 1, nil); err == nil {
		t.Error("a file accepted as directory")
	}
}

func TestMisses(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 1<<20)
	k := key(1)
	if err := c.PutOriginal(k, blob(10, 'o')); err != nil {
		t.Fatal(err)
	}
	// Truncated behind the cache's back: a zero-length file is a miss and is forgotten.
	if err := os.Truncate(filepath.Join(dir, k+".jpg"), 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.GetOriginal(k); ok {
		t.Fatal("zero-length file was a hit")
	}
	if c.HasOriginal(k) || c.Size() != 0 {
		t.Fatalf("empty file still indexed, Size = %d", c.Size())
	}
	// Deleted behind the cache's back: a miss, and the size is corrected.
	if err := c.PutThumb(k, 240, v1, blob(10, 't')); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, k+"-240-v1.webp")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.GetThumb(k, 240, v1); ok {
		t.Fatal("deleted file was a hit")
	}
	if c.Size() != 0 {
		t.Fatalf("Size = %d", c.Size())
	}
	// And it can be stored again.
	if err := c.PutThumb(k, 240, v1, blob(10, 't')); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.GetThumb(k, 240, v1); !ok {
		t.Fatal("miss after storing again")
	}
}

func TestLRUEviction(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 1000)
	clock(c)
	for i := 1; i <= 4; i++ { // 4 x 200 = 800 bytes
		if err := c.PutThumb(key(i), 480, v1, blob(200, byte('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	// Use 1, so 2 is now the least recently used. HasThumb must not count as use.
	if _, ok := c.GetThumb(key(1), 480, v1); !ok {
		t.Fatal("miss")
	}
	if !c.HasThumb(key(2), 480, v1) {
		t.Fatal("miss")
	}
	// 800 + 300 = 1100 > 1000: exactly one eviction is needed, and it must be 2.
	if err := c.PutThumb(key(5), 480, v1, blob(300, '5')); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]bool{1: true, 2: false, 3: true, 4: true, 5: true} {
		if c.HasThumb(key(i), 480, v1) != want {
			t.Errorf("thumb %d present = %v, want %v", i, !want, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, key(2)+"-480-v1.webp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("evicted file still on disk: %v", err)
	}
	if c.Size() != 900 || diskBytes(t, dir) != 900 {
		t.Fatalf("Size = %d, disk = %d, want 900", c.Size(), diskBytes(t, dir))
	}
	// A large write evicts several, oldest first: 3, 4, then 1 (used later than those).
	if err := c.PutThumb(key(6), 240, v1, blob(600, '6')); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]bool{1: false, 3: false, 4: false, 5: true} {
		if c.HasThumb(key(i), 480, v1) != want {
			t.Errorf("after the large write: thumb %d present = %v, want %v", i, !want, want)
		}
	}
	if !c.HasThumb(key(6), 240, v1) || c.Size() != 900 {
		t.Fatalf("Size = %d", c.Size())
	}
}

func TestOriginalsAreNeverEvictedForSize(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 1000)
	clock(c)
	if err := c.PutThumb(key(1), 480, v1, blob(300, 't')); err != nil {
		t.Fatal(err)
	}
	if err := c.PutThumb(key(2), 480, v1, blob(300, 't')); err != nil {
		t.Fatal(err)
	}
	// Originals push the total over the limit: thumbnails go, originals stay.
	for i := 1; i <= 3; i++ {
		if err := c.PutOriginal(key(i), blob(400, 'o')); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		if got, ok := c.GetOriginal(key(i)); !ok || len(got) != 400 {
			t.Fatalf("original %d was evicted", i)
		}
	}
	if c.HasThumb(key(1), 480, v1) || c.HasThumb(key(2), 480, v1) {
		t.Fatal("thumbnails kept although the cache is over its limit")
	}
	if c.Size() != 1200 {
		t.Fatalf("Size = %d, want 1200 (originals only)", c.Size())
	}
	// The originals alone are over the limit. A new thumbnail still stays, inside the
	// reserve (an eighth of the limit, 125 bytes): it is not evicted as soon as it is written.
	if err := c.PutThumb(key(1), 240, v1, blob(10, 't')); err != nil {
		t.Fatal(err)
	}
	if !c.HasThumb(key(1), 240, v1) || c.Size() != 1210 {
		t.Fatalf("a thumbnail inside the reserve was evicted: Size = %d", c.Size())
	}
	// Only Prune removes originals.
	n, err := c.Prune(func(k string) bool { return k == key(2) }, v1)
	if err != nil || n != 3 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if c.HasThumb(key(1), 240, v1) || c.HasOriginal(key(1)) || !c.HasOriginal(key(2)) || c.HasOriginal(key(3)) || c.Size() != 400 {
		t.Fatalf("after Prune: Size = %d", c.Size())
	}
}

// When the originals fill the limit, thumbnails keep a reserve of limit/8:
// the most recently used ones stay, and the warning is logged once, not on
// every write.
func TestThumbnailReserve(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	c, err := Open(dir, 8000, slog.New(slog.NewTextHandler(&logs, nil))) // reserve: 1000 bytes
	if err != nil {
		t.Fatal(err)
	}
	clock(c)
	for i := 1; i <= 9; i++ { // 9000 bytes of originals: over the limit on their own
		if err := c.PutOriginal(key(i), blob(1000, 'o')); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 9; i++ { // 9 x 300 bytes of thumbnails, oldest first
		if err := c.PutThumb(key(i), 480, v1, blob(300, 't')); err != nil {
			t.Fatal(err)
		}
		if !c.HasThumb(key(i), 480, v1) {
			t.Fatalf("thumbnail %d was evicted as soon as it was written", i)
		}
	}
	// The newest three (900 bytes) fit the reserve; a fourth would not.
	for i := 1; i <= 9; i++ {
		if got, want := c.HasThumb(key(i), 480, v1), i >= 7; got != want {
			t.Errorf("thumbnail %d present = %v, want %v", i, got, want)
		}
		if !c.HasOriginal(key(i)) {
			t.Errorf("original %d was evicted", i)
		}
	}
	if c.Size() != 9900 || diskBytes(t, dir) != 9900 {
		t.Fatalf("Size = %d, disk = %d, want 9900", c.Size(), diskBytes(t, dir))
	}
	// A recently used thumbnail outlives a newer unused one.
	if _, ok := c.GetThumb(key(7), 480, v1); !ok {
		t.Fatal("miss")
	}
	if err := c.PutThumb(key(1), 240, v1, blob(300, 't')); err != nil {
		t.Fatal(err)
	}
	if !c.HasThumb(key(7), 480, v1) || c.HasThumb(key(8), 480, v1) || !c.HasThumb(key(1), 240, v1) {
		t.Error("eviction inside the reserve is not least recently used first")
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("%d warnings logged, want 1:\n%s", n, logs.String())
	}
	// Reopening gives the same state.
	again := open(t, dir, 8000)
	if again.Size() != 9900 || !again.HasThumb(key(7), 480, v1) {
		t.Fatalf("reopened: Size = %d", again.Size())
	}
	// Back under the limit, and over it again: the warning is logged once more.
	if _, err := c.Prune(func(k string) bool { return k == key(1) }, v1); err != nil {
		t.Fatal(err)
	}
	if err := c.PutThumb(key(1), 480, v1, blob(300, 't')); err != nil || c.Size() > 8000 {
		t.Fatalf("after Prune: Size = %d, err = %v", c.Size(), err)
	}
	for i := 2; i <= 9; i++ {
		if err := c.PutOriginal(key(i), blob(1000, 'o')); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 2 {
		t.Errorf("%d warnings logged after the second overflow, want 2", n)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 1<<20)
	live, dead := key(1), key(2)
	for _, k := range []string{live, dead} {
		if err := c.PutOriginal(k, blob(100, 'o')); err != nil {
			t.Fatal(err)
		}
		for _, ver := range []string{"1", "1q80", "0"} {
			for _, size := range []int{240, 480} {
				if err := c.PutThumb(k, size, ver, blob(10, 't')); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := map[string]int{}
	n, err := c.Prune(func(k string) bool { asked[k]++; return k == live }, "1")
	if err != nil {
		t.Fatal(err)
	}
	// dead: 1 original + 6 thumbnails; live: 4 thumbnails of other versions.
	if n != 11 {
		t.Fatalf("removed %d files, want 11", n)
	}
	if asked[live] != 1 || asked[dead] != 1 || len(asked) != 2 {
		t.Errorf("live was asked %v", asked)
	}
	if !c.HasOriginal(live) || !c.HasThumb(live, 240, "1") || !c.HasThumb(live, 480, "1") {
		t.Error("live files removed")
	}
	if c.HasThumb(live, 480, "1q80") || c.HasThumb(live, 240, "0") {
		t.Error("old versions kept")
	}
	if c.HasOriginal(dead) || c.HasThumb(dead, 480, "1") {
		t.Error("dead files kept")
	}
	want := []string{live + "-240-v1.webp", live + "-480-v1.webp", live + ".jpg", "notes.txt"}
	got := files(t, dir)
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	if c.Size() != 120 {
		t.Fatalf("Size = %d, want 120", c.Size())
	}
	if n, err := c.Prune(func(string) bool { return true }, "1"); n != 0 || err != nil {
		t.Fatalf("second Prune = %d, %v", n, err)
	}
	if _, err := c.Prune(nil, "1"); err == nil {
		t.Error("nil live accepted")
	}
	if _, err := c.Prune(func(string) bool { return true }, "../1"); !errors.Is(err, ErrBadVersion) {
		t.Errorf("bad version = %v", err)
	}
}

// Recency survives a restart through file mtimes.
func TestRecencySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 1<<20)
	base := time.Now().Add(-time.Hour)
	for i := 1; i <= 3; i++ {
		if err := c.PutThumb(key(i), 480, v1, blob(300, 't')); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute) // 1 oldest, 3 newest
		if err := os.Chtimes(filepath.Join(dir, key(i)+"-480-v1.webp"), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// A read of the oldest (its mtime is over a minute old) writes a fresh mtime.
	c = open(t, dir, 1<<20)
	if _, ok := c.GetThumb(key(1), 480, v1); !ok {
		t.Fatal("miss")
	}
	info, err := os.Stat(filepath.Join(dir, key(1)+"-480-v1.webp"))
	if err != nil || time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("mtime not refreshed by a read: %v %v", info.ModTime(), err)
	}
	// Reopen with room for two: the least recently used, now 2, is evicted at Open.
	c = open(t, dir, 700)
	if !c.HasThumb(key(1), 480, v1) || c.HasThumb(key(2), 480, v1) || !c.HasThumb(key(3), 480, v1) {
		t.Fatalf("wrong thumbnail evicted at Open: 1=%v 2=%v 3=%v",
			c.HasThumb(key(1), 480, v1), c.HasThumb(key(2), 480, v1), c.HasThumb(key(3), 480, v1))
	}
	if c.Size() != 600 {
		t.Fatalf("Size = %d", c.Size())
	}
}

// Readers never see a partial or mixed file while writers replace it, and no temp files are left.
func TestAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 64<<20)
	k := key(1)
	const n = 256 << 10
	variants := [][]byte{blob(n, 'A'), blob(n, 'B'), blob(n/2, 'C')}
	if err := c.PutOriginal(k, variants[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.PutThumb(k, 480, v1, variants[0]); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 3 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				v := variants[(i+w)%len(variants)]
				if err := c.PutOriginal(k, v); err != nil {
					t.Error(err)
					return
				}
				if err := c.PutThumb(k, 480, v1, v); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	check := func(data []byte, ok bool) {
		if !ok {
			t.Error("miss during replace")
			return
		}
		if len(data) != n && len(data) != n/2 {
			t.Errorf("partial file: %d bytes", len(data))
			return
		}
		if c := bytes.Count(data, data[:1]); c != len(data) {
			t.Errorf("mixed file: %d of %d bytes equal the first", c, len(data))
		}
	}
	for range 4 {
		wg.Go(func() {
			for range 300 {
				check(c.GetOriginal(k))
				check(c.GetThumb(k, 480, v1))
				// What is on disk under the final name is always complete too.
				data, err := os.ReadFile(filepath.Join(dir, k+".jpg"))
				check(data, err == nil)
			}
		})
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	for _, name := range files(t, dir) {
		if strings.HasPrefix(name, tmpPrefix) {
			t.Errorf("temp file left behind: %s", name)
		}
	}
	if got := c.Size(); got != diskBytes(t, dir) {
		t.Errorf("Size = %d, disk = %d", got, diskBytes(t, dir))
	}
}

// Everything at once under the race detector; the index must match the disk afterwards.
func TestConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 40_000)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				k := key((g*7 + i) % 40)
				switch i % 5 {
				case 0:
					_ = c.PutOriginal(k, blob(500, 'o'))
				case 1, 2:
					_ = c.PutThumb(k, []int{240, 480}[i%2], v1, blob(700+i, 't'))
				case 3:
					if data, ok := c.GetThumb(k, 480, v1); ok && len(data) < 700 {
						t.Errorf("short read: %d", len(data))
					}
					c.HasOriginal(k)
				case 4:
					if g == 0 && i%50 == 4 {
						_, _ = c.Prune(func(k string) bool { return k[0]&1 == 0 }, v1)
					}
					c.Size()
				}
			}
		})
	}
	wg.Wait()
	if got := c.Size(); got != diskBytes(t, dir) {
		t.Fatalf("Size = %d, disk = %d", got, diskBytes(t, dir))
	}
	// Within the limit unless originals alone exceed it.
	var originals int64
	for _, name := range files(t, dir) {
		if strings.HasSuffix(name, ".jpg") {
			originals += 500
		}
	}
	// Over the limit only when the originals leave less than the reserve (limit/8) for thumbnails.
	if thumbs := c.Size() - originals; c.Size() > 40_000 && thumbs > 40_000/reserveDivisor {
		t.Fatalf("Size = %d over the limit with %d bytes of thumbnails left (originals %d)", c.Size(), thumbs, originals)
	}
	// A fresh index of the directory agrees.
	if again := open(t, dir, 1<<30); again.Size() != c.Size() {
		t.Fatalf("reopened Size = %d, want %d", again.Size(), c.Size())
	}
}

func TestParseName(t *testing.T) {
	k := key(1)
	for name, want := range map[string]bool{
		k + ".jpg":                 true,
		k + "-480-v1.webp":         true,
		k + "-240-v1q80.webp":      true,
		k + "-0480-v1.webp":        false,
		k + "-+480-v1.webp":        false,
		k + "-480-v.webp":          false,
		k + "-480-v1.webp.tmp":     false,
		k + "-480-v1-v2.webp":      false,
		k + ".jpeg":                false,
		k[:21] + ".jpg":            false,
		k + "x.jpg":                false,
		k:                          false,
		"":                         false,
		k + "-480-v" + k + ".webp": false,
	} {
		if _, ok := parseName(name); ok != want {
			t.Errorf("parseName(%q) = %v, want %v", name, ok, want)
		}
	}
}
