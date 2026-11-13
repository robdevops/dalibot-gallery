package source

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureSnapshot(t testing.TB) (fixture, *Snapshot) {
	f, records := fixtureRecords(t)
	return f, Build(records, f.options())
}

func describe(tiles []Tile) []string {
	out := make([]string, len(tiles))
	for i, tl := range tiles {
		out[i] = tl.ID + " " + tl.Style
	}
	return out
}

func TestBuildFixture(t *testing.T) {
	f, s := fixtureSnapshot(t)
	want := []string{
		group + ":101 monet", group + ":101 _", // current first, then table order
		"42:7 _", "42:7 sketch", // current "dali" is missing: falls back to "_"
		"42:8 picasso", "42:8 anime", // current missing and no "_": first key present
		group + ":103 vaporwave", group + ":103 _", group + ":103 bw", group + ":103 neon_noir", // unknown keys last, alphabetical
		group + ":201 dali", group + ":201 _", group + ":201 monet", // carry_over: the newer copy wins; :200 is gone
		"42:10 _",
		"42:9 bw", // its "_" moved to 42:10; current falls back to what is left
		"-200:5 _",
		"42:12 _", "42:11 _", // same updated: id descending
		"42:13 ega", // the entry without a file_id is ignored
	}
	tiles, next, err := s.Page("", 1000)
	if err != nil || next != "" {
		t.Fatalf("Page: next=%q err=%v", next, err)
	}
	if got := describe(tiles); !reflect.DeepEqual(got, want) {
		t.Fatalf("tiles:\n got %q\nwant %q", got, want)
	}
	if s.Len() != 19 || s.Records() != 11 || !s.Built().Equal(time.Unix(f.Now, 0)) {
		t.Errorf("Len=%d Records=%d Built=%v", s.Len(), s.Records(), s.Built())
	}
	for _, gone := range []string{group + ":102", group + ":200", "42:1", "42:2", "42:3", ProbeID} {
		for _, tl := range tiles {
			if tl.ID == gone {
				t.Errorf("record %s should not be listed", gone)
			}
		}
	}

	fox := tiles[0]
	if fox.Label != "Monet" || fox.Current != "monet" || fox.Prompt != "a red fox in snow" || fox.Command != "dream" ||
		fox.Updated != f.Now-1000 || !strings.HasSuffix(fox.Caption, "Claude Monet") {
		t.Errorf("fox tile = %+v", fox)
	}
	if len(fox.Styles) != 2 || fox.Styles[0].Key != fox.Key || fox.Styles[1].Key != tiles[1].Key ||
		fox.Styles[1].Style != "_" || fox.Styles[1].Label != "Original" {
		t.Errorf("fox styles = %+v", fox.Styles)
	}
	if !reflect.DeepEqual(tiles[1].Styles, fox.Styles) || tiles[1].Current != "monet" {
		t.Error("tiles of one record must share styles and current")
	}
	if tiles[6].Label != "Vaporwave" || tiles[9].Label != "Neon Noir" || tiles[4].Current != "picasso" || tiles[14].Current != "bw" || tiles[18].Current != "ega" {
		t.Errorf("labels/current wrong: %q %q %q %q %q", tiles[6].Label, tiles[9].Label, tiles[4].Current, tiles[14].Current, tiles[18].Current)
	}
	if tiles[15].Command != "flux" || tiles[15].Alt() != "a tram in the rain" || fox.Alt() != fox.Caption {
		t.Errorf("command/alt wrong: %+v", tiles[15])
	}
	for _, tl := range tiles {
		found := false
		for _, img := range tl.Styles {
			found = found || img.Style == tl.Current
		}
		if !found {
			t.Errorf("%s: current %q is not one of its styles", tl.ID, tl.Current)
		}
	}
}

func TestKeysAndFileIDs(t *testing.T) {
	_, s := fixtureSnapshot(t)
	if got := Key("AgACAgUAAxkDAAIBfox"); got != "RQ2AvWdtw2pN5Bhv0Y3CdX" {
		t.Errorf("Key = %q", got)
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	for i, key := range s.Keys() {
		if !ValidKey(key) || seen[key] {
			t.Errorf("key %q invalid or repeated", key)
		}
		seen[key] = true
		fileID, ok := s.FileID(key)
		if !ok || !strings.Contains(fileID, "FIXTURE") || Key(fileID) != key || files[fileID] {
			t.Errorf("FileID(%q) = %q, %v", key, fileID, ok)
		}
		files[fileID] = true
		tl, ok := s.Tile(key)
		if !ok || tl.Key != key || !s.Has(key) || tl.pos != indexOf(tl.Styles, key) {
			t.Errorf("Tile(%q) = %+v, %v", key, tl, ok)
		}
		if page, _, _ := s.Page("", 1000); page[i].Key != key {
			t.Errorf("Keys()[%d] is out of order", i)
		}
	}
	if len(seen) != 19 {
		t.Errorf("%d keys", len(seen))
	}
	// Keys outside the snapshot are unknown: expired, duplicate-only and arbitrary ones.
	for _, k := range []string{Key("AgACAgUAAxkDAAIold0-FIXTURE-old0old0old0_AAnK6MRtYq2lV"), Key("anything"), "", "../../etc/passwd"} {
		if _, ok := s.FileID(k); ok || s.Has(k) {
			t.Errorf("key %q should be unknown", k)
		}
		if _, ok := s.Tile(k); ok {
			t.Errorf("Tile(%q) should not exist", k)
		}
	}
	for _, bad := range []string{"", "short", "RQ2AvWdtw2pN5Bhv0Y3Cd/", "RQ2AvWdtw2pN5Bhv0Y3CdXX", "RQ2AvWdtw2pN5Bhv0Y3Cd.", "RQ2AvWdtw2pN5Bhv0Y3C=="} {
		if ValidKey(bad) {
			t.Errorf("ValidKey(%q) = true", bad)
		}
	}
}

func indexOf(images []Image, key string) int {
	for i, img := range images {
		if img.Key == key {
			return i
		}
	}
	return -1
}

// What the browser gets must never contain a file_id.
func TestJSONHasNoFileID(t *testing.T) {
	_, s := fixtureSnapshot(t)
	tiles, _, _ := s.Page("", 1000)
	raw, err := json.Marshal(map[string]any{"tiles": tiles})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"FIXTURE", "AgACAg", "file_id", "fileID", "FileID"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("JSON contains %q", secret)
		}
	}
	var decoded struct {
		Tiles []map[string]any `json:"tiles"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"caption", "command", "current", "id", "key", "label", "prompt", "style", "styles", "updated"}
	for _, tl := range decoded.Tiles {
		if got := mapKeys(tl); !reflect.DeepEqual(got, wantFields) {
			t.Fatalf("tile fields = %v, want %v", got, wantFields)
		}
		for _, img := range tl["styles"].([]any) {
			if got := mapKeys(img.(map[string]any)); !reflect.DeepEqual(got, []string{"caption", "key", "label", "style"}) {
				t.Fatalf("style fields = %v", got)
			}
		}
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestChatAllowlist(t *testing.T) {
	f, records := fixtureRecords(t)
	opt := f.options()
	opt.Chats = []string{"-200", group}
	tiles, _, _ := Build(records, opt).Page("", 1000)
	if len(tiles) != 10 {
		t.Errorf("%d tiles, want 10", len(tiles))
	}
	for _, tl := range tiles {
		if !strings.HasPrefix(tl.ID, "-200:") && !strings.HasPrefix(tl.ID, group+":") {
			t.Errorf("tile of chat outside the allowlist: %s", tl.ID)
		}
	}
	// "42" must not match "-200" or a chat that merely starts with 42.
	opt.Chats = []string{"4"}
	if n := Build(records, opt).Len(); n != 0 {
		t.Errorf("prefix matched: %d tiles", n)
	}
	opt.Chats = []string{"42"}
	if n := Build(records, opt).Len(); n != 9 {
		t.Errorf("chat 42: %d tiles, want 9", n)
	}
}

func TestBuildRules(t *testing.T) {
	now := int64(1_800_000_000)
	opt := Options{StoreDays: 90, DefaultCommand: "dream", Now: func() time.Time { return time.Unix(now, 0) }}
	img := func(id string) map[string]RawImage { return map[string]RawImage{"_": {FileID: id, Caption: id}} }
	recs := []Record{
		{ID: "1:1", TTL: now, Images: img("a")},                                                            // ttl == now is still alive
		{ID: "1:2", TTL: now - 1, Images: img("b")},                                                        // expired
		{ID: "nocolon", TTL: now + 5, Images: img("c")},                                                    // malformed id
		{ID: ":5", TTL: now + 5, Images: img("d")},                                                         // no chat
		{ID: "5:", TTL: now + 5, Images: img("e")},                                                         // no message
		{ID: "1:3", TTL: now + 9, Images: nil},                                                             // empty
		{ID: "1:4", TTL: now + 9, Command: "/flux", Images: img("f")},                                      // slash stripped
		{ID: "1:4", TTL: now + 99, Images: img("g")},                                                       // repeated id: first wins
		{ID: "1:5", TTL: now + 8, Images: map[string]RawImage{"_": {FileID: "h"}, "monet": {FileID: "h"}}}, // same file twice in one record
		{ID: "2:1", TTL: now + 7, Command: "", Images: img("a")},                                           // newer copy of "a"
	}
	s := Build(recs, opt)
	got := describe(s.Newest(100))
	want := []string{"1:4 _", "1:5 _", "2:1 _"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tiles = %q, want %q", got, want)
	}
	if tl := s.Newest(1)[0]; tl.Command != "flux" || tl.Updated != now+9-90*86400 {
		t.Errorf("tile = %+v", tl)
	}
	if tl := s.Newest(3)[2]; tl.Command != "dream" {
		t.Errorf("default command not applied: %+v", tl)
	}
	if len(s.Newest(2)[1].Styles) != 1 {
		t.Error("a file repeated inside one record must give one tile")
	}
	if len(s.Newest(0)) != 0 || len(s.Newest(-3)) != 0 {
		t.Error("Newest(0) should be empty")
	}

	// StoreDays shifts updated only; order is unchanged.
	opt.StoreDays = 30
	if tl := Build(recs, opt).Newest(1)[0]; tl.Updated != now+9-30*86400 {
		t.Errorf("updated = %d", tl.Updated)
	}
	// Empty input.
	e := Build(nil, opt)
	if tiles, next, err := e.Page("", 10); len(tiles) != 0 || next != "" || err != nil || e.Len() != 0 {
		t.Error("empty snapshot should page to nothing")
	}
	if n, newest, err := e.NewSince(""); n != 0 || newest != "" || err != nil {
		t.Error("empty snapshot: NewSince")
	}
}

// A picture imported from a forwarded message is dated by its created time, not by
// when it was imported, and still expires by its ttl.
func TestImportedRecordsKeepTheirDate(t *testing.T) {
	now := int64(1_800_000_000)
	span := int64(90 * 86400)
	opt := Options{StoreDays: 90, DefaultCommand: "dream", Now: func() time.Time { return time.Unix(now, 0) }}
	img := func(id string) map[string]RawImage { return map[string]RawImage{"_": {FileID: id, Caption: id}} }
	recs := []Record{
		{ID: "111:fOLD", TTL: now + span, Created: now - 400*86400, Images: img("old")}, // imported just now, made 400 days ago
		{ID: "-100:7", TTL: now + span - 3600, Images: img("hour")},                     // drawn an hour ago
		{ID: "111:fMID", TTL: now + span, Created: now - 60, Images: img("minute")},     // imported, made a minute ago
		{ID: "111:fGONE", TTL: now - 1, Created: now - 10, Images: img("gone")},         // imported, but expired
	}
	tiles := Build(recs, opt).Newest(10)
	var got []string
	for _, tile := range tiles {
		got = append(got, tile.ID)
	}
	if want := []string{"111:fMID", "-100:7", "111:fOLD"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if tiles[2].Updated != now-400*86400 || tiles[1].Updated != now-3600 {
		t.Errorf("updated = %d and %d", tiles[2].Updated, tiles[1].Updated)
	}
}

func TestBuildDoesNotDependOnInputOrder(t *testing.T) {
	f, records := fixtureRecords(t)
	want := describe(Build(records, f.options()).Newest(1000))
	rng := rand.New(rand.NewSource(1))
	for range 50 {
		rng.Shuffle(len(records), func(a, b int) { records[a], records[b] = records[b], records[a] })
		if got := describe(Build(records, f.options()).Newest(1000)); !reflect.DeepEqual(got, want) {
			t.Fatalf("order depends on input order:\n got %q\nwant %q", got, want)
		}
	}
}

func pageAll(t testing.TB, s *Snapshot, cursor string, limit int) []Tile {
	t.Helper()
	var all []Tile
	for i := 0; ; i++ {
		tiles, next, err := s.Page(cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		checkPage(t, s, tiles, limit)
		all = append(all, tiles...)
		if next == "" {
			return all
		}
		if len(tiles) == 0 {
			t.Fatal("empty page with a next cursor")
		}
		cursor = next
		if i > 10000 {
			t.Fatal("paging does not end")
		}
	}
}

// checkPage verifies the page rules: whole records only, and no more than limit
// tiles unless the page is a single record.
func checkPage(t testing.TB, s *Snapshot, tiles []Tile, limit int) {
	t.Helper()
	if len(tiles) == 0 {
		return
	}
	if first := tiles[0]; first.pos != 0 {
		t.Fatalf("page starts inside record %s", first.ID)
	}
	if last := tiles[len(tiles)-1]; last.pos != len(last.Styles)-1 {
		t.Fatalf("page ends inside record %s", last.ID)
	}
	if len(tiles) > max(limit, 1) && tiles[0].ID != tiles[len(tiles)-1].ID {
		t.Fatalf("page of %d tiles over several records, limit %d", len(tiles), limit)
	}
}

func TestPagingRoundTrip(t *testing.T) {
	_, s := fixtureSnapshot(t)
	want := describe(s.Newest(1000))
	for limit := 1; limit <= 25; limit++ {
		if got := describe(pageAll(t, s, "", limit)); !reflect.DeepEqual(got, want) {
			t.Errorf("limit %d:\n got %q\nwant %q", limit, got, want)
		}
	}
	// limit < 1 is 1; the last page has no next cursor; a cursor past the end is empty.
	if tiles, next, _ := s.Page("", 0); len(tiles) != 2 || tiles[1].ID != tiles[0].ID || next == "" { // one whole record
		t.Errorf("limit 0: %d tiles, next %q", len(tiles), next)
	}
	// Pages hold whole records: 2+2+2 fit in 7, the 4-image record does not.
	if tiles, _, _ := s.Page("", 7); len(tiles) != 6 {
		t.Errorf("limit 7: %d tiles, want 6", len(tiles))
	}
	// A cursor that points into the middle of a record continues after that record.
	if tiles, _, _ := s.Page(s.Newest(1)[0].Cursor(), 2); len(tiles) != 2 || tiles[0].ID != "42:7" {
		t.Errorf("mid-record cursor: %q", describe(tiles))
	}
	if tiles, next, _ := s.Page("", 1); len(tiles) != 2 || next == "" {
		t.Errorf("limit 0: %d tiles, next %q", len(tiles), next)
	}
	if tiles, next, _ := s.Page("", 19); len(tiles) != 19 || next != "" {
		t.Errorf("exact page: %d tiles, next %q", len(tiles), next)
	}
	last := s.Newest(1000)[18].Cursor()
	if tiles, next, err := s.Page(last, 10); len(tiles) != 0 || next != "" || err != nil {
		t.Errorf("past the end: %d tiles, next %q, err %v", len(tiles), next, err)
	}
	// A cursor is opaque base64url and round-trips through a URL unchanged.
	for _, tl := range s.Newest(1000) {
		c := tl.Cursor()
		if strings.ContainsAny(c, "+/=&?# ") {
			t.Errorf("cursor %q is not URL-safe", c)
		}
		p, err := decodeCursor(c)
		if err != nil || p != tl.position() {
			t.Errorf("cursor round trip: %+v != %+v (%v)", p, tl.position(), err)
		}
	}
}

func TestBadCursors(t *testing.T) {
	_, s := fixtureSnapshot(t)
	enc := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	bad := []string{
		"!!!", "====", enc("1|2|3"), enc("2|100|0|1:1"), enc("1|x|0|1:1"), enc("1|100|x|1:1"),
		enc("1|100|-1|1:1"), enc("1|100|0|"), enc("1|99999999999999999999|0|1:1"), strings.Repeat("A", 600),
		base64.StdEncoding.EncodeToString([]byte("1|100|0|1:1??>>")),
	}
	for _, c := range bad {
		if _, _, err := s.Page(c, 10); !errors.Is(err, ErrBadCursor) {
			t.Errorf("Page(%q): err = %v", c, err)
		}
		if _, _, err := s.NewSince(c); !errors.Is(err, ErrBadCursor) {
			t.Errorf("NewSince(%q): err = %v", c, err)
		}
	}
	// A well-formed cursor for a tile that no longer exists still works: it is a position.
	if tiles, _, err := s.Page(enc("1|1799997500|7|42:7"), 2); err != nil || len(tiles) != 2 || tiles[0].ID != "42:8" {
		t.Errorf("position cursor: %v %v", describe(tiles), err)
	}
	// An id containing '|' survives.
	p := position{5, "a|b:c|d", 3}
	if q, err := decodeCursor(p.encode()); err != nil || q != p {
		t.Errorf("round trip with '|': %+v %v", q, err)
	}
}

func TestNewSince(t *testing.T) {
	_, s := fixtureSnapshot(t)
	tiles := s.Newest(1000)
	top := tiles[0].Cursor()
	for i, tl := range tiles {
		n, newest, err := s.NewSince(tl.Cursor())
		if err != nil || n != i || newest != top {
			t.Errorf("NewSince(tile %d) = %d, %q, %v", i, n, newest, err)
		}
	}
	if n, newest, _ := s.NewSince(""); n != 19 || newest != top {
		t.Errorf("NewSince(\"\") = %d, %q", n, newest)
	}
}

// The scenario the cursor exists for: the table changes between two page requests.
func TestNoDuplicatesOrGapsWhenItemsAreAddedBetweenPages(t *testing.T) {
	f, records := fixtureRecords(t)
	opt := f.options()
	before := Build(records, opt)
	ttl := func(updated int64) int64 { return updated + 90*86400 }
	one := func(id string, updated int64, file string) Record {
		return Record{ID: id, Prompt: id, Command: "dream", Current: "_", TTL: ttl(updated),
			Images: map[string]RawImage{"_": {FileID: file, Caption: id}, "monet": {FileID: file + "-monet", Caption: id}}}
	}
	added := []Record{
		one("42:500", f.Now-5, "new-1"),        // newer than everything
		one("42:501", f.Now-5, "new-2"),        // same second, greater id
		one(group+":900", f.Now-1000, "new-3"), // same second as the newest fixture record, greater id
		one("-200:6", f.Now-1000, "new-4"),     // same second, smaller id
		one("7:1", f.Now-4500, "new-5"),        // lands in the middle of the listing
		one("7:2", f.Now-9500, "new-6"),        // ties with the twins near the end
		one("7:3", f.Now-20000, "new-7"),       // older than everything
	}
	after := Build(append(append([]Record(nil), records...), added...), opt)
	if after.Len() != before.Len()+2*len(added) {
		t.Fatalf("after has %d tiles, want %d", after.Len(), before.Len()+2*len(added))
	}

	for limit := 1; limit <= before.Len(); limit++ {
	pages:
		for firstPages := 1; ; firstPages++ {
			// The client reads some pages from the old snapshot...
			var got []Tile
			cursor := ""
			for i := 0; i < firstPages; i++ {
				tiles, next, err := before.Page(cursor, limit)
				if err != nil {
					t.Fatal(err)
				}
				if next == "" {
					break pages // the old listing ended: nothing left to continue
				}
				got = append(got, tiles...)
				cursor = next
			}
			seenFirst := len(got)
			// ...then the snapshot is replaced, and it carries on with its cursor.
			got = append(got, pageAll(t, after, cursor, limit)...)

			seen := map[string]int{}
			for i, tl := range got {
				if j, dup := seen[tl.Key]; dup {
					t.Fatalf("limit %d after %d pages: duplicate %s %s at %d and %d", limit, firstPages, tl.ID, tl.Style, j, i)
				}
				seen[tl.Key] = i
			}
			// No gaps: every tile of the old listing is delivered...
			for _, tl := range before.Newest(1000) {
				if _, ok := seen[tl.Key]; !ok {
					t.Fatalf("limit %d after %d pages: gap, %s %s was never delivered", limit, firstPages, tl.ID, tl.Style)
				}
			}
			// ...and so is every added tile that sorts after the cursor; those before it are
			// reported by NewSince instead, never shifted into a later page.
			last := got[seenFirst-1]
			for _, tl := range after.Newest(1000) {
				_, delivered := seen[tl.Key]
				isNew := !before.Has(tl.Key)
				sortsAfter := last.position().before(tl.position())
				if isNew && delivered != sortsAfter {
					t.Fatalf("limit %d after %d pages: added tile %s %s delivered=%v, sorts after cursor=%v", limit, firstPages, tl.ID, tl.Style, delivered, sortsAfter)
				}
			}
			// Order is still newest first throughout.
			for i := 1; i < len(got); i++ {
				if !got[i-1].position().before(got[i].position()) {
					t.Fatalf("limit %d: tiles %d and %d out of order", limit, i-1, i)
				}
			}
		}
	}

	// The "N new" count for a client that holds the old top tile.
	oldTop := before.Newest(1)[0]
	n, newest, err := after.NewSince(oldTop.Cursor())
	// 42:501 and 42:500, then the two that share the old top's second and have a greater id
	// (ids compare as strings: "-200:6" > "-1001234567890:900" > "-1001234567890:101"); two tiles each.
	if err != nil || n != 8 || newest != after.Newest(1)[0].Cursor() {
		t.Errorf("NewSince = %d, %q, %v", n, newest, err)
	}
	if got := describe(after.Newest(n)); !reflect.DeepEqual(got, []string{"42:501 _", "42:501 monet", "42:500 _", "42:500 monet", "-200:6 _", "-200:6 monet", group + ":900 _", group + ":900 monet"}) {
		t.Errorf("new tiles = %q", got)
	}
}

// Records also disappear (ttl) and move (a new style refreshes ttl) between pages.
func TestNoDuplicatesWhenItemsExpireOrMoveBetweenPages(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	now := int64(1_800_000_000)
	opt := Options{StoreDays: 90, DefaultCommand: "dream", Now: func() time.Time { return time.Unix(now, 0) }}
	styleKeys := []string{"_", "monet", "dali", "zz_custom", "bw"}
	file := 0
	newRecord := func(id string, updated int64) Record {
		r := Record{ID: id, Prompt: id, Current: styleKeys[rng.Intn(len(styleKeys))], TTL: updated + 90*86400, Images: map[string]RawImage{}}
		for _, k := range styleKeys[:1+rng.Intn(len(styleKeys))] {
			file++
			r.Images[k] = RawImage{FileID: fmt.Sprintf("file-%d", file), Caption: id}
		}
		return r
	}
	for round := range 200 {
		var records []Record
		for i := range 30 {
			records = append(records, newRecord(fmt.Sprintf("%d:%d", -100+rng.Intn(3), i), now-int64(rng.Intn(40))*100))
		}
		before := Build(records, opt)
		limit := 1 + rng.Intn(12)
		first, cursor, err := before.Page("", limit)
		if err != nil {
			t.Fatal(err)
		}
		if cursor == "" {
			continue
		}
		// Mutate: drop some records, add some, and give some a new style (which moves them to the top).
		var changed []Record
		for _, r := range records {
			switch rng.Intn(6) {
			case 0: // expired and deleted
			case 1: // a new style: ttl refreshed
				r2 := r
				r2.Images = map[string]RawImage{"extra": {FileID: "extra-" + r.ID + fmt.Sprint(round), Caption: "x"}}
				maps.Copy(r2.Images, r.Images)
				r2.TTL = now + 90*86400
				r2.Current = "extra"
				changed = append(changed, r2)
			case 2: // the user switched style in Telegram: current changes, ttl does not
				r2 := r
				r2.Current = styleKeys[rng.Intn(len(styleKeys))]
				changed = append(changed, r2)
			default:
				changed = append(changed, r)
			}
		}
		for i := range 5 {
			changed = append(changed, newRecord(fmt.Sprintf("9:%d", i), now-int64(rng.Intn(40))*100))
		}
		after := Build(changed, opt)
		got := append(append([]Tile(nil), first...), pageAll(t, after, cursor, limit)...)
		seen := map[string]bool{}
		for _, tl := range got {
			if seen[tl.Key] {
				t.Fatalf("round %d: duplicate tile %s %s", round, tl.ID, tl.Style)
			}
			seen[tl.Key] = true
		}
		// No gaps: every tile of a record that is still listed after the cursor's record was delivered.
		last := first[len(first)-1].position()
		last.pos = 1 << 30
		for _, tl := range after.Newest(100000) {
			if last.before(tl.position()) && !seen[tl.Key] {
				t.Fatalf("round %d: gap, %s %s was skipped", round, tl.ID, tl.Style)
			}
		}
	}
}
