//go:build sqlite

package source

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sqliteFixture was written by the bot's own SqliteStore: see testdata/sqlite/make_fixture.py.
const (
	sqliteFixture     = "../../testdata/sqlite/dalibot.db"
	sqliteFixtureNow  = 1800000000 // NOW in make_fixture.py
	sqliteFixtureDays = 90
	fixtureGroup      = "-1001234567890"
	fixturePrivate    = "424242"
	fixtureOther      = "-1009876543210"
	// The bot's own statement, for the tables the tests make themselves.
	botSchema = `CREATE TABLE IF NOT EXISTS messages (chat_id TEXT, message_id TEXT, data TEXT, updated INTEGER, PRIMARY KEY (chat_id, message_id))`
)

func fixtureFileID(name string) string {
	return "AgACAgUAAxkDAAIsql-FIXTURE-" + name + "_AAnK6MRtYq2lV"
}

func sqliteFixtureOptions(chats ...string) Options {
	return Options{StoreDays: sqliteFixtureDays, Chats: chats, DefaultCommand: "dream",
		Now: func() time.Time { return time.Unix(sqliteFixtureNow, 0) }}
}

func newSQLite(t testing.TB, path string, busy time.Duration, log *slog.Logger) *SQLite {
	t.Helper()
	s, err := NewSQLite(SQLiteOptions{Path: path, StoreDays: sqliteFixtureDays, BusyTimeout: busy, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// copyFixture puts a writable copy of the fixture in a new directory.
func copyFixture(t testing.TB) string {
	t.Helper()
	raw, err := os.ReadFile(sqliteFixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dalibot.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writer opens path the way the bot does: read-write, waiting up to 10 s for a lock.
func writer(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=10000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func insertRow(t testing.TB, db *sql.DB, chat, message, data, updated any) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO messages VALUES (?, ?, ?, ?)`, chat, message, data, updated); err != nil {
		t.Fatal(err)
	}
}

func recordJSON(fileID, caption string) string {
	raw, _ := json.Marshal(map[string]any{"prompt": caption, "command": "dream", "current": "_",
		"images": map[string]any{"_": map[string]any{"file_id": fileID, "caption": caption, "base": nil}}})
	return string(raw)
}

// dirState describes a directory exactly: every name, size, mode, modification time and content hash.
func dirState(t testing.TB, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		sum := [32]byte{}
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			sum = sha256.Sum256(raw)
		}
		fmt.Fprintf(&b, "%s %d %v %d %x\n", e.Name(), info.Size(), info.Mode(), info.ModTime().UnixNano(), sum)
	}
	return b.String()
}

// The fixture the bot's code wrote, through every listing rule.
func TestSQLiteFixture(t *testing.T) {
	src := newSQLite(t, sqliteFixture, 0, nil)
	records, err := src.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 12 {
		t.Fatalf("read %d rows, want 12", len(records))
	}
	byID := map[string]Record{}
	for _, r := range records {
		byID[r.ID] = r
	}

	// Rows are mapped as they are stored; Build decides what is shown.
	fox := byID[fixtureGroup+":101"]
	if fox.Prompt != "a red fox in snow" || fox.Command != "dream" || fox.Current != "_" || len(fox.Images) != 3 ||
		fox.Images["monet"].FileID != fixtureFileID("fox1") ||
		fox.Images["dali"].Caption != "A red fox standing in deep snow, in the style of Salvador Dali" ||
		fox.TTL != sqliteFixtureNow-3800+sqliteFixtureDays*86400 || fox.Created != 0 {
		t.Errorf("ordinary record: %+v", fox)
	}
	if boat := byID[fixtureGroup+":fAQADimport1"]; boat.Created != sqliteFixtureNow-40*86400 || boat.Command != "storeonly" ||
		boat.TTL != sqliteFixtureNow-2000+sqliteFixtureDays*86400 {
		t.Errorf("imported record: %+v", boat)
	}
	if note, ok := byID[fixtureGroup+":g77"]; !ok || note.Images != nil || note.Prompt != "holiday pictures" {
		t.Errorf("album note: %+v (present %v)", note, ok)
	}
	if _, ok := byID[fixtureGroup+":103"]; ok {
		t.Error("the record the bot deleted is still there")
	}

	snap := Build(records, sqliteFixtureOptions())
	type row struct {
		id, style, current, command, prompt, fileID string
		updated                                     int64
	}
	const now = int64(sqliteFixtureNow)
	want := []row{
		{fixtureOther + ":401", "_", "_", "dream", "a city made of books", "books", now - 800},
		// Carried over from message 301, which lost every image to this newer copy.
		{fixturePrivate + ":302", "bw", "bw", "dream", "tea party on the moon", "tea1", now - 900},
		{fixturePrivate + ":302", "_", "bw", "dream", "tea party on the moon", "tea0", now - 900},
		// Variations: one record for each photo of an album; the prompt may be empty.
		{fixtureGroup + ":203", "_", "_", "dream", "", "plain", now - 1450},
		{fixtureGroup + ":202", "_", "_", "dream", "make it cosy", "cosy2", now - 1500},
		{fixtureGroup + ":201", "_", "_", "dream", "make it cosy", "cosy1", now - 1500},
		{fixtureGroup + ":102", "_", "_", "flux", "lighthouse in a storm", "lighthouse", now - 3000},
		// Three styles, dated from the last write (the button press that chose "_").
		{fixtureGroup + ":101", "_", "_", "dream", "a red fox in snow", "fox0", now - 3800},
		{fixtureGroup + ":101", "monet", "_", "dream", "a red fox in snow", "fox1", now - 3800},
		{fixtureGroup + ":101", "dali", "_", "dream", "a red fox in snow", "fox2", now - 3800},
		// Imported 2000 s ago, but shown where it was first sent: 40 days ago.
		{fixtureGroup + ":fAQADimport1", "_", "_", "storeonly", "a paper boat on a wide river", "boat", now - 40*86400},
		// 89 days old: still kept. The 91-day-old row is expired although the bot has not purged it.
		{fixtureGroup + ":91", "_", "_", "dream", "an old oak tree", "oak", now - 89*86400},
	}
	tiles, next, err := snap.Page("", 100)
	if err != nil || next != "" {
		t.Fatalf("page: next=%q err=%v", next, err)
	}
	var got []row
	for _, tile := range tiles {
		fileID, _ := snap.FileID(tile.Key)
		name := strings.TrimSuffix(strings.TrimPrefix(fileID, "AgACAgUAAxkDAAIsql-FIXTURE-"), "_AAnK6MRtYq2lV")
		got = append(got, row{tile.ID, tile.Style, tile.Current, tile.Command, tile.Prompt, name, tile.Updated})
	}
	if !slices.Equal(got, want) {
		t.Errorf("listing:\n got %v\nwant %v", got, want)
	}
	if snap.Records() != 9 {
		t.Errorf("records = %d, want 9", snap.Records())
	}
	if tile, _ := snap.Tile(Key(fixtureFileID("cosy1"))); tile.Caption != "watercolour, seen from above" || tile.Alt() != tile.Caption {
		t.Errorf("variation caption: %+v", tile)
	}
	for _, gone := range []string{"expired", "deleted"} {
		if snap.Has(Key(fixtureFileID(gone))) {
			t.Errorf("%s image is listed", gone)
		}
	}

	// The chat allow-list applies to the chat_id column.
	only := Build(records, sqliteFixtureOptions(fixturePrivate, fixtureOther))
	if only.Len() != 3 || only.Records() != 2 {
		t.Errorf("allow-list: %d tiles of %d records, want 3 of 2", only.Len(), only.Records())
	}

	// One second later the 89-day row would still be there; at 90 days and a second it is not.
	later := sqliteFixtureOptions()
	later.Now = func() time.Time { return time.Unix(now+86400+1, 0) }
	if Build(records, later).Has(Key(fixtureFileID("oak"))) {
		t.Error("a row older than the store days is listed")
	}
	later.Now = func() time.Time { return time.Unix(now+86400, 0) }
	if !Build(records, later).Has(Key(fixtureFileID("oak"))) {
		t.Error("a row exactly as old as the store days must still be listed, as the bot keeps it")
	}
}

// Reading leaves the bot's file and its directory exactly as they were, and
// the connection cannot write even if asked to.
func TestSQLiteReadOnly(t *testing.T) {
	path := copyFixture(t)
	dir := filepath.Dir(path)
	before := dirState(t, dir)
	src := newSQLite(t, path, 0, nil)
	for range 3 {
		if records, err := src.Records(context.Background()); err != nil || len(records) != 12 {
			t.Fatalf("records: %d %v", len(records), err)
		}
	}
	if after := dirState(t, dir); after != before {
		t.Errorf("reading changed the directory:\nbefore %s\nafter  %s", before, after)
	}

	db, err := sql.Open("sqlite3", src.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`INSERT INTO messages VALUES ('1', '2', '{}', 3)`,
		`DELETE FROM messages`,
		`UPDATE messages SET updated = 0`,
		`CREATE TABLE other (x)`,
		`PRAGMA journal_mode=WAL`,
		`VACUUM`,
		`PRAGMA query_only=0; DELETE FROM messages`, // even with the second guard taken off
	} {
		if _, err := db.Exec(stmt); err == nil && !strings.HasPrefix(stmt, "PRAGMA journal_mode") {
			t.Errorf("%s: succeeded on the read-only connection", stmt)
		}
	}
	if after := dirState(t, dir); after != before {
		t.Errorf("write attempts changed the directory:\nbefore %s\nafter  %s", before, after)
	}
}

// A missing file is an error, is never created, and is read again once it is back.
func TestSQLiteMissing(t *testing.T) {
	path := copyFixture(t)
	dir := filepath.Dir(path)
	st := NewStore(newSQLite(t, path, 0, nil), sqliteFixtureOptions(), nil)
	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := st.Snapshot()
	if first.Len() != 12 {
		t.Fatalf("tiles = %d", first.Len())
	}

	away := filepath.Join(t.TempDir(), "moved.db")
	if err := os.Rename(path, away); err != nil {
		t.Fatal(err)
	}
	err := st.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("refresh with the file missing: %v", err)
	}
	if st.Snapshot() != first {
		t.Error("the previous snapshot was not kept")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("something was created where the database should be: %v", entries)
	}

	// A directory in its place, then an empty file (no table yet): errors, not an empty gallery.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.Refresh(context.Background()); err == nil {
		t.Error("a directory was read as a database")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("an empty file: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Errorf("the empty file was written to: %v %v", info, err)
	}
	if err := os.WriteFile(path, []byte("this is not a database, it is a text file of some length"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Refresh(context.Background()); err == nil {
		t.Error("a text file was read as a database")
	}
	if st.Snapshot() != first {
		t.Error("the previous snapshot was not kept")
	}

	if err := os.Rename(away, path); err != nil {
		t.Fatal(err)
	}
	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.Snapshot() == first || st.Snapshot().Len() != 12 {
		t.Error("the file was not read again once it was back")
	}
}

// While the bot holds its write lock: a short hold is waited out, a long one
// is an error and the previous snapshot stays.
func TestSQLiteLocked(t *testing.T) {
	path := copyFixture(t)
	st := NewStore(newSQLite(t, path, 150*time.Millisecond, nil), sqliteFixtureOptions(), nil)
	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := st.Snapshot()

	db := writer(t, path)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// What the bot does to write, taken as far as the exclusive lock.
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO messages VALUES (?, ?, ?, ?)`, fixtureGroup, "999",
		recordJSON(fixtureFileID("locked"), "written under the lock"), sqliteFixtureNow-10); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = st.Refresh(ctx)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("refresh while locked: %v", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond || waited > 5*time.Second {
		t.Errorf("waited %v for the lock, want about 150ms", waited)
	}
	if st.Snapshot() != first {
		t.Error("the previous snapshot was not kept")
	}

	// A hold shorter than the busy timeout only delays the read.
	patient := NewStore(newSQLite(t, path, 10*time.Second, nil), sqliteFixtureOptions(), nil)
	done := make(chan error, 1)
	go func() { done <- patient.Refresh(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("the read did not wait for the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("refresh after the lock was released: %v", err)
	}
	if !patient.Snapshot().Has(Key(fixtureFileID("locked"))) || patient.Snapshot().Len() != 13 {
		t.Error("the row written under the lock is not listed")
	}
	if err := st.Refresh(ctx); err != nil || st.Snapshot().Len() != 13 {
		t.Errorf("refresh once unlocked: %v", err)
	}

	// A cancelled context ends the wait.
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start = time.Now()
	if err := patient.Refresh(cctx); err == nil {
		t.Error("refresh succeeded under an exclusive lock")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("a cancelled read still waited %v", waited)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

// Rows that are not the bot's are skipped and counted; they cannot spoil the rest.
func TestSQLiteOddRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "odd.db")
	db := writer(t, path)
	if _, err := db.Exec(botSchema); err != nil {
		t.Fatal(err)
	}
	const now = int64(sqliteFixtureNow)
	good := func(name string) string { return recordJSON(fixtureFileID(name), name) }
	insertRow(t, db, "1", "1", good("good"), now-10)
	// Skipped: not JSON, not an object, no data at all, fields of the wrong type.
	insertRow(t, db, "1", "2", "{not json", now-10)
	insertRow(t, db, "1", "3", `["a list"]`, now-10)
	insertRow(t, db, "1", "4", nil, now-10)
	insertRow(t, db, "1", "5", `{"prompt": 5, "images": {}}`, now-10)
	insertRow(t, db, "1", "6", `{"prompt": "x", "images": "none"}`, now-10)
	// Kept as rows, but never listed: no date, a date that is text, no chat, no message, no file_id.
	insertRow(t, db, "1", "7", good("nodate"), nil)
	insertRow(t, db, "1", "8", good("textdate"), "yesterday")
	insertRow(t, db, "", "9", good("nochat"), now-10)
	insertRow(t, db, "1", "", good("nomessage"), now-10)
	insertRow(t, db, "1", "10", `{"prompt": "x", "images": {"_": {"caption": "no file"}, "monet": null}}`, now-10)
	// Listed: values the bot may leave null or out, a blob and numbers where text is expected,
	// a created that is not a whole number (ignored), and unknown fields.
	insertRow(t, db, "1", "11", `{"prompt": null, "command": null, "current": null, "kind": "variation", "extra": [1, 2],
		"images": {"_": {"file_id": "`+fixtureFileID("nulls")+`", "caption": null, "base": null}}}`, now-20)
	insertRow(t, db, 2, 12, []byte(good("blob")), fmt.Sprint(now-30))
	insertRow(t, db, "1", "13", strings.Replace(good("fraction"), "{", `{"created": 1799990000.5, `, 1), now-40)
	insertRow(t, db, "1", "14", strings.Replace(good("created"), "{", `{"created": 1799000000, `, 1), now-50)
	insertRow(t, db, "1", "15", strings.Replace(good("future"), "{", `{"created": 1900000000, `, 1), now-60)

	var logs lockedBuffer
	src := newSQLite(t, path, 0, slog.New(slog.NewTextHandler(&logs, nil)))
	records, err := src.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 11 {
		t.Errorf("rows read = %d, want 11", len(records))
	}
	if !strings.Contains(logs.String(), "count=5") {
		t.Errorf("no warning for the 5 skipped rows: %q", logs.String())
	}
	snap := Build(records, sqliteFixtureOptions())
	var got []string
	for _, tile := range snap.Newest(100) {
		got = append(got, fmt.Sprintf("%s@%d", tile.ID, now-tile.Updated))
	}
	want := []string{"1:1@10", "1:11@20", "2:12@30", "1:13@40", "1:15@60", "1:14@1000000"}
	if !slices.Equal(got, want) {
		t.Errorf("listed %v, want %v", got, want)
	}
	if tile, ok := snap.Tile(Key(fixtureFileID("nulls"))); !ok || tile.Prompt != "" || tile.Command != "dream" || tile.Current != "_" || tile.Caption != "" {
		t.Errorf("a record with nulls: %+v", tile)
	}
}

// A path with characters that mean something in a URI is still just a path.
func TestSQLiteOddPath(t *testing.T) {
	raw, err := os.ReadFile(sqliteFixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "a dir?mode=rwc&x=1#frag %41 ü")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bot's data?immutable=1.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirState(t, dir)
	records, err := newSQLite(t, path, 0, nil).Records(context.Background())
	if err != nil || len(records) != 12 {
		t.Fatalf("records: %d %v", len(records), err)
	}
	if after := dirState(t, dir); after != before {
		t.Errorf("the directory changed:\nbefore %s\nafter  %s", before, after)
	}
	if _, err := newSQLite(t, filepath.Join(dir, "missing?mode=rwc"), 0, nil).Records(context.Background()); err == nil {
		t.Error("a missing file was read")
	}
	if after := dirState(t, dir); after != before {
		t.Error("a file was created from a path that looks like a URI")
	}
}

// A database in WAL mode is refused before SQLite can create its -shm and -wal files.
func TestSQLiteWALRefused(t *testing.T) {
	path := copyFixture(t)
	dir := filepath.Dir(path)
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("switching to WAL: %q %v", mode, err)
	}
	if err := db.Close(); err != nil { // the last connection closes: -wal and -shm are removed
		t.Fatal(err)
	}
	before := dirState(t, dir)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("expected only the database file, found %v", entries)
	}
	_, err = newSQLite(t, path, 0, nil).Records(context.Background())
	if err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Errorf("a WAL database: %v", err)
	}
	if after := dirState(t, dir); after != before {
		t.Errorf("the directory changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestNewSQLiteRequires(t *testing.T) {
	if _, err := NewSQLite(SQLiteOptions{StoreDays: 90}); err == nil {
		t.Error("no path accepted")
	}
	if _, err := NewSQLite(SQLiteOptions{Path: "/x"}); err == nil {
		t.Error("no store days accepted")
	}
}

// The bot writes while the gallery reads: every read succeeds, sees whole
// transactions only, and no write is refused.
func TestSQLiteConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db := writer(t, path)
	if _, err := db.Exec(botSchema); err != nil {
		t.Fatal(err)
	}
	const writes = 150
	const now = int64(sqliteFixtureNow)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		defer close(stop)
		for i := range writes {
			// The bot's _modify: read, change and write in one immediate transaction. Two rows
			// each time, so a reader that saw half a transaction would count an odd number.
			tx, err := db.Begin()
			if err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
			for _, half := range []string{"a", "b"} {
				if _, err := tx.Exec(`INSERT OR REPLACE INTO messages VALUES (?, ?, ?, ?)`, "7", fmt.Sprintf("%d%s", i, half),
					recordJSON(fixtureFileID(fmt.Sprintf("w%d%s", i, half)), "concurrent"), now-int64(i)); err != nil {
					t.Errorf("write %d: %v", i, err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		}
	})
	src := newSQLite(t, path, 0, nil)
	for range 3 {
		wg.Go(func() {
			last := 0
			for {
				records, err := src.Records(context.Background())
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				if len(records)%2 != 0 || len(records) < last {
					t.Errorf("read %d rows after %d: half a transaction, or rows lost", len(records), last)
				}
				last = len(records)
				select {
				case <-stop:
					return
				default:
				}
			}
		})
	}
	wg.Wait()
	records, err := src.Records(context.Background())
	if err != nil || len(records) != 2*writes {
		t.Fatalf("final read: %d rows, %v", len(records), err)
	}
	if n := Build(records, sqliteFixtureOptions()).Len(); n != 2*writes {
		t.Errorf("tiles = %d, want %d", n, 2*writes)
	}
}
