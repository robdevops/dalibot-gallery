package source

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// hiding returns Options.Hidden for a set of keys.
func hiding(keys ...string) func(string) bool {
	return func(k string) bool { return slices.Contains(keys, k) }
}

// keyOf is the key of the tile described as "<id> <style>" (see describe).
func keyOf(t testing.TB, s *Snapshot, what string) string {
	t.Helper()
	tiles, _, _ := s.Page("", 10000)
	for _, tl := range tiles {
		if tl.ID+" "+tl.Style == what {
			return tl.Key
		}
	}
	t.Fatalf("no tile %q", what)
	return ""
}

// A hidden image is in no tile, no page and no record's styles, and Has
// says no, exactly as for an image that never existed. The snapshot keeps
// it aside for the admin.
func TestHiddenImages(t *testing.T) {
	f, records := fixtureRecords(t)
	plain := Build(records, f.options())
	all := describe(pageAll(t, plain, "", 1000))

	// Nothing hidden is the same as no rule at all.
	opt := f.options()
	opt.Hidden = hiding()
	if got := Build(records, opt); !reflect.DeepEqual(describe(pageAll(t, got, "", 1000)), all) || len(got.Hidden()) != 0 || got.Records() != plain.Records() {
		t.Fatal("an empty hidden list changed the listing")
	}
	if len(plain.Hidden()) != 0 || plain.HasHidden(keyOf(t, plain, "42:10 _")) {
		t.Fatal("a snapshot without the rule has hidden images")
	}

	monet := keyOf(t, plain, group+":101 monet") // the current style of a record with two images
	only := keyOf(t, plain, "-200:5 _")          // the only image of its record
	// 42:10's "_" is also in the older record 42:9, where de-duplication dropped it.
	moved := keyOf(t, plain, "42:10 _")
	dali := keyOf(t, plain, group+":201 dali") // the current style of a record with three
	opt.Hidden = hiding(monet, only, moved, dali, Key("a file the bot never recorded"))
	s := Build(records, opt)

	want := []string{
		group + ":101 _", // "monet" is hidden; the current style falls back to "_"
		"42:7 _", "42:7 sketch",
		"42:8 picasso", "42:8 anime",
		group + ":103 vaporwave", group + ":103 _", group + ":103 bw", group + ":103 neon_noir",
		group + ":201 _", group + ":201 monet", // "dali" is hidden
		// 42:10 is gone: its only image is hidden
		"42:9 bw", // and the hidden image does not come back through the older record that had it
		// -200:5 is gone
		"42:12 _", "42:11 _",
		"42:13 ega",
	}
	tiles := pageAll(t, s, "", 1000)
	if got := describe(tiles); !reflect.DeepEqual(got, want) {
		t.Fatalf("tiles:\n got %q\nwant %q", got, want)
	}
	if s.Len() != len(want) || s.Records() != plain.Records()-2 {
		t.Errorf("Len=%d Records=%d", s.Len(), s.Records())
	}
	// Small pages too: whole records, no gaps, the same tiles.
	for _, limit := range []int{1, 2, 3, 7} {
		if got := describe(pageAll(t, s, "", limit)); !reflect.DeepEqual(got, want) {
			t.Errorf("limit %d: %q", limit, got)
		}
	}

	fox := tiles[0]
	if fox.Current != "_" || fox.Style != "_" || len(fox.Styles) != 1 || fox.Styles[0].Key != fox.Key {
		t.Errorf("the record whose current style is hidden: %+v", fox)
	}
	raw, _ := json.Marshal(tiles)
	hidden := []string{monet, only, moved, dali}
	for _, k := range hidden {
		if s.Has(k) {
			t.Errorf("Has(%s) is true for a hidden image", k)
		}
		if _, ok := s.Tile(k); ok {
			t.Errorf("Tile(%s) found a hidden image", k)
		}
		if slices.Contains(s.Keys(), k) {
			t.Errorf("Keys has the hidden image %s", k)
		}
		if strings.Contains(string(raw), k) {
			t.Errorf("the listing's JSON names the hidden image %s", k)
		}
		if !s.HasHidden(k) || !s.Known(k) {
			t.Errorf("HasHidden(%s) or Known is false", k)
		}
		// The file_id stays available (to the server only), for the admin's view and the cache.
		if id, ok := s.FileID(k); !ok || Key(id) != k {
			t.Errorf("FileID(%s) = %q %v", k, id, ok)
		}
	}
	// Listed keys are known and not hidden; a key of nothing is neither.
	if k := fox.Key; !s.Known(k) || s.HasHidden(k) {
		t.Error("a listed image is hidden or unknown")
	}
	for _, k := range []string{Key("a file the bot never recorded"), "", "x"} {
		if s.Has(k) || s.HasHidden(k) || s.Known(k) {
			t.Errorf("%q is known", k)
		}
	}

	// What is kept aside: the hidden images that are in the records, in
	// listing order, each once, with what an admin needs to recognise it.
	var got []string
	for _, h := range s.Hidden() {
		got = append(got, h.ID+" "+h.Style)
		if h.Styles != nil || h.Current != "" || h.Key == "" || h.Label == "" || h.Command == "" || h.Updated == 0 {
			t.Errorf("hidden tile %+v", h)
		}
	}
	if want := []string{group + ":101 monet", group + ":201 dali", "42:10 _", "-200:5 _"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Hidden:\n got %q\nwant %q", got, want)
	}
	if h := s.Hidden()[0]; h.Key != monet || h.Label != "Monet" || h.Prompt != "a red fox in snow" || !strings.HasSuffix(h.Caption, "Claude Monet") || h.Updated != f.Now-1000 {
		t.Errorf("hidden fox = %+v", h)
	}

	// The same listing whatever order the records come in.
	reversed := slices.Clone(records)
	slices.Reverse(reversed)
	if got := Build(reversed, opt); !reflect.DeepEqual(describe(pageAll(t, got, "", 1000)), want) || len(got.Hidden()) != 4 {
		t.Error("the listing depends on the order of the records")
	}

	// Everything hidden: an empty listing, not a broken one.
	opt.Hidden = func(string) bool { return true }
	empty := Build(records, opt)
	if tiles, next, err := empty.Page("", 60); err != nil || len(tiles) != 0 || next != "" || empty.Len() != 0 || empty.Records() != 0 || len(empty.Hidden()) != plain.Len() {
		t.Errorf("everything hidden: %d tiles, %d hidden, next %q, %v", len(tiles), len(empty.Hidden()), next, err)
	}
}

// A cursor handed out before an image was hidden still names its place:
// paging on from it neither repeats nor skips a tile, also when the tile it
// names is the one that was hidden.
func TestHidingBetweenPages(t *testing.T) {
	f, records := fixtureRecords(t)
	before := Build(records, f.options())
	all := pageAll(t, before, "", 1000)
	for _, limit := range []int{1, 3, 5} {
		for at := range all {
			first, next, err := before.Page("", limit)
			if err != nil {
				t.Fatal(err)
			}
			opt := f.options()
			opt.Hidden = hiding(all[at].Key)
			after := Build(records, opt)
			seen := map[string]int{}
			for _, tl := range first {
				seen[tl.Key]++
			}
			if next != "" {
				for _, tl := range pageAll(t, after, next, limit) {
					seen[tl.Key]++
				}
			}
			for i, tl := range all {
				n := seen[tl.Key]
				// The hidden tile may have been on the first page already; nothing else may be missing or doubled.
				if tl.Key == all[at].Key {
					if n > 1 || (n == 1 && i >= len(first)) {
						t.Fatalf("limit %d, hiding tile %d: the hidden tile was delivered after it was hidden", limit, at)
					}
					continue
				}
				if n != 1 {
					t.Fatalf("limit %d, hiding tile %d: tile %d (%s %s) delivered %d times", limit, at, i, tl.ID, tl.Style, n)
				}
			}
			// "N new" counts by position, so a hidden newest tile leaves nothing new.
			count, _, err := after.NewSince(all[0].Cursor())
			if err != nil || count != 0 {
				t.Fatalf("hiding tile %d: %d new since the newest (%v)", at, count, err)
			}
		}
	}
}

// The rule is applied to records, after either source has read them: the
// SQLite reader's records are hidden exactly like the DynamoDB reader's.
func TestHiddenImagesFromSQLite(t *testing.T) {
	records, err := newSQLite(t, sqliteFixture, time.Second, nil).Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opt := sqliteFixtureOptions()
	plain := Build(records, opt)
	all := pageAll(t, plain, "", 1000)
	if len(all) < 6 {
		t.Fatalf("the fixture lists only %d tiles", len(all))
	}
	// One image of a record with several styles, and the only image of another record.
	var several, single Tile
	for _, tl := range all {
		if len(tl.Styles) > 1 && several.Key == "" {
			several = tl
		}
		if len(tl.Styles) == 1 && single.Key == "" {
			single = tl
		}
	}
	if several.Key == "" || single.Key == "" {
		t.Fatal("the fixture has no record with several styles, or none with one")
	}
	opt.Hidden = hiding(several.Key, single.Key)
	s := Build(records, opt)
	var want []string
	for _, tl := range all {
		if tl.Key != several.Key && tl.Key != single.Key {
			want = append(want, tl.Key)
		}
	}
	if got := s.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys:\n got %q\nwant %q", got, want)
	}
	if s.Records() != plain.Records()-1 || len(s.Hidden()) != 2 || !s.HasHidden(several.Key) || !s.HasHidden(single.Key) || s.Has(single.Key) {
		t.Errorf("Records=%d Hidden=%d", s.Records(), len(s.Hidden()))
	}
	for _, tl := range pageAll(t, s, "", 2) {
		if tl.ID == single.ID {
			t.Errorf("record %s is still listed", single.ID)
		}
		for _, img := range tl.Styles {
			if img.Key == several.Key {
				t.Errorf("record %s still has the hidden style", tl.ID)
			}
		}
	}
}

// Rebuild applies a change of the hidden list at once, from the records read
// last: the source is not read, and it is not a refresh.
func TestStoreRebuild(t *testing.T) {
	f, records := fixtureRecords(t)
	src := &flakySource{records: records}
	var mu sync.Mutex
	hidden := map[string]bool{}
	opt := f.options()
	opt.Hidden = func(k string) bool {
		mu.Lock()
		defer mu.Unlock()
		return hidden[k]
	}
	set := func(k string, v bool) {
		mu.Lock()
		hidden[k] = v
		mu.Unlock()
	}
	st := NewStore(src, opt, nil)
	var swaps []*Snapshot
	st.OnSwap(func(s *Snapshot) { swaps = append(swaps, s) })

	// Before anything was read there is nothing to build from.
	st.Rebuild()
	if st.Snapshot() != emptySnapshot || len(swaps) != 0 || src.callCount() != 0 || st.Healthy(time.Hour) {
		t.Fatal("Rebuild before the first refresh did something")
	}

	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := st.Snapshot()
	total := first.Len()
	key := keyOf(t, first, "42:10 _")
	read := st.LastRefresh()

	set(key, true)
	if !st.Snapshot().Has(key) {
		t.Fatal("the snapshot changed before Rebuild")
	}
	st.Rebuild()
	s := st.Snapshot()
	if s == first || s.Has(key) || !s.HasHidden(key) || s.Len() != total-1 {
		t.Fatalf("after hiding and Rebuild: Has=%v HasHidden=%v Len=%d", s.Has(key), s.HasHidden(key), s.Len())
	}
	if first.Len() != total || !first.Has(key) {
		t.Error("the earlier snapshot was modified")
	}
	if src.callCount() != 1 {
		t.Errorf("Rebuild read the source (%d reads)", src.callCount())
	}
	if !st.LastRefresh().Equal(read) {
		t.Error("Rebuild counted as a refresh")
	}
	if len(swaps) != 2 || swaps[1] != s {
		t.Errorf("OnSwap hooks: %d calls", len(swaps))
	}

	// Restoring is the same the other way round.
	set(key, false)
	st.Rebuild()
	if s := st.Snapshot(); !s.Has(key) || s.HasHidden(key) || s.Len() != total {
		t.Error("after restoring and Rebuild the image is not back")
	}

	// A failed refresh keeps the records to build from.
	src.set(nil, errors.New("unreachable"))
	if err := st.Refresh(context.Background()); err == nil {
		t.Fatal("the refresh did not fail")
	}
	set(key, true)
	st.Rebuild()
	if s := st.Snapshot(); s.Has(key) || s.Len() != total-1 {
		t.Error("Rebuild after a failed refresh lost the listing")
	}

	// The next refresh applies the list as well.
	src.set(records[:len(records)-1], nil)
	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.Snapshot().Has(key) {
		t.Error("a refresh showed a hidden image")
	}
}

// Refreshes and rebuilds at the same time: whatever the order, once both
// are done the snapshot reflects the list as it was when Rebuild was called.
func TestStoreRebuildDuringRefresh(t *testing.T) {
	f, records := fixtureRecords(t)
	var mu sync.Mutex
	hidden := map[string]bool{}
	opt := f.options()
	opt.Hidden = func(k string) bool {
		mu.Lock()
		defer mu.Unlock()
		return hidden[k]
	}
	st := NewStore(Static(records), opt, nil)
	st.OnSwap(func(*Snapshot) {})
	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := st.Snapshot().Keys()
	stop := make(chan struct{})
	var refreshers sync.WaitGroup
	for range 3 {
		refreshers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = st.Refresh(context.Background())
				}
			}
		})
	}
	for round := range 200 {
		k := keys[round%len(keys)]
		hide := round%2 == 0
		mu.Lock()
		hidden[k] = hide
		mu.Unlock()
		st.Rebuild()
		if s := st.Snapshot(); s.Has(k) == hide {
			t.Fatalf("round %d: after Rebuild, Has = %v for an image whose hidden state is %v", round, s.Has(k), hide)
		}
		if !hide {
			continue
		}
		mu.Lock()
		hidden[k] = false
		mu.Unlock()
		st.Rebuild()
	}
	close(stop)
	refreshers.Wait()
}
