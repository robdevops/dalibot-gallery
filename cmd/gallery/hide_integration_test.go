package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/robdevops/dalibot-gallery/internal/devstack"
)

// hideAcrossRestarts runs the admin's hiding against the real binary: st is
// running with devstack.AdminToken as its admin token, its cache in cacheDir
// and GALLERY_STATE_DIR=stateDir. viewAuth says whether viewing needs the
// token. Both integration tests call it, so the same steps run with the
// DynamoDB source and with the SQLite source.
//
// It hides two images, checks the listing and the image routes, stops the
// gallery, deletes the whole cache, starts it again and expects the images
// still hidden; restores them; then starts once more on a corrupt list. It
// leaves the gallery running with nothing hidden.
func hideAcrossRestarts(t *testing.T, st *stack, viewAuth bool, cacheDir, stateDir string) {
	t.Helper()
	listFile := filepath.Join(stateDir, "hidden.json")
	admin := func(method, target string, hdr ...string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, st.base+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("admin", devstack.AdminToken)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		return st.send(req)
	}
	viewer := func(method, target string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, st.base+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if viewAuth {
			req.SetBasicAuth("it", devstack.GalleryToken)
		}
		return st.send(req)
	}
	// The listing as a viewer gets it: keys in order, and for each key the keys of its record's styles.
	listing := func() (keys []string, styles map[string][]string, v string) {
		t.Helper()
		styles = map[string][]string{}
		target := "/api/tiles?limit=120"
		for {
			resp, body := viewer("GET", target)
			if resp.StatusCode != 200 {
				t.Fatalf("GET %s: %d %s", target, resp.StatusCode, body)
			}
			var p tilesPage
			if err := json.Unmarshal(body, &p); err != nil {
				t.Fatal(err)
			}
			v = p.V
			for _, tl := range p.Tiles {
				keys = append(keys, tl.Key)
				for _, img := range tl.Styles {
					styles[tl.Key] = append(styles[tl.Key], img.Key)
				}
			}
			if p.Next == nil {
				return keys, styles, v
			}
			target = "/api/tiles?limit=120&cursor=" + *p.Next
		}
	}
	hiddenKeys := func() []string {
		t.Helper()
		resp, body := admin("GET", "/api/admin/hidden")
		if resp.StatusCode != 200 {
			t.Fatalf("GET /api/admin/hidden: %d %s", resp.StatusCode, body)
		}
		var l struct {
			Hidden []struct{ Key string }
			Gone   int
		}
		if err := json.Unmarshal(body, &l); err != nil {
			t.Fatal(err)
		}
		if l.Gone != 0 {
			t.Errorf("%d hidden images are reported gone", l.Gone)
		}
		var keys []string
		for _, h := range l.Hidden {
			keys = append(keys, h.Key)
		}
		slices.Sort(keys)
		return keys
	}

	before, styles, v := listing()
	// One image of a record with several, and the only image of another record.
	var several, single string
	for _, k := range before {
		if len(styles[k]) > 1 && several == "" {
			several = k
		}
		if len(styles[k]) == 1 && single == "" {
			single = k
		}
	}
	if several == "" || single == "" {
		t.Fatal("the listing has no record with several images, or none with one")
	}
	both := []string{several, single}
	slices.Sort(both)
	urls := func(key string) []string {
		return []string{"/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + v}
	}
	for _, key := range both {
		for _, u := range urls(key) {
			if resp, _ := viewer("GET", u); resp.StatusCode != 200 {
				t.Fatalf("GET %s before hiding: %d", u, resp.StatusCode)
			}
		}
	}
	if _, err := os.Stat(listFile); !os.IsNotExist(err) {
		t.Fatalf("a list exists before anything was hidden: %v", err)
	}

	// Only an admin, and only from the gallery itself.
	if resp, body := viewer("POST", "/api/admin/hide/"+several); resp.StatusCode != 404 {
		t.Fatalf("a viewer's POST hide: %d %s", resp.StatusCode, body)
	}
	if resp, body := admin("POST", "/api/admin/hide/"+several, "Sec-Fetch-Site", "cross-site"); resp.StatusCode != 403 {
		t.Fatalf("a cross-site POST hide: %d %s", resp.StatusCode, body)
	}
	if resp, _ := viewer("GET", "/admin.js"); resp.StatusCode != 404 {
		t.Errorf("a viewer was served the admin's script: %d", resp.StatusCode)
	}
	if got, _, _ := listing(); !slices.Equal(got, before) {
		t.Fatal("a refused request changed the listing")
	}

	for _, key := range both {
		if resp, body := admin("POST", "/api/admin/hide/"+key, "Sec-Fetch-Site", "same-origin"); resp.StatusCode != 200 {
			t.Fatalf("POST hide: %d %s", resp.StatusCode, body)
		}
	}
	want := slices.DeleteFunc(slices.Clone(before), func(k string) bool { return slices.Contains(both, k) })
	check := func(when string) {
		t.Helper()
		got, styles, _ := listing()
		if !slices.Equal(got, want) {
			t.Fatalf("%s: the listing has %d tiles, want %d (the two hidden images left out)", when, len(got), len(want))
		}
		for k, ks := range styles {
			if slices.Contains(ks, several) || slices.Contains(ks, single) {
				t.Errorf("%s: tile %s still names a hidden image among its styles", when, k)
			}
		}
		for _, key := range both {
			for i, u := range urls(key) {
				resp, body := viewer("GET", u)
				unknown, unknownBody := viewer("GET", urls("AAAAAAAAAAAAAAAAAAAAAA")[i])
				if resp.StatusCode != 404 || unknown.StatusCode != 404 || string(body) != string(unknownBody) ||
					resp.Header.Get("Cache-Control") != unknown.Header.Get("Cache-Control") {
					t.Errorf("%s: GET %s as a viewer: %d %q", when, u, resp.StatusCode, body)
				}
				// The admin can still look at what there is to restore; nothing may cache that.
				if resp, _ := admin("GET", u); resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("ETag") != "" {
					t.Errorf("%s: GET %s as the admin: %d %v", when, u, resp.StatusCode, resp.Header)
				}
			}
		}
		if _, body := viewer("GET", "/"); strings.Contains(string(body), several) || strings.Contains(string(body), single) || strings.Contains(string(body), "admin.js") {
			t.Errorf("%s: the viewer's page names a hidden image or the admin's script", when)
		}
		if got := hiddenKeys(); !slices.Equal(got, both) {
			t.Errorf("%s: the admin's list is %q, want %q", when, got, both)
		}
		if resp, body := admin("GET", "/admin/hidden"); resp.StatusCode != 200 || !strings.Contains(string(body), `data-restore="`+several+`"`) || !strings.Contains(string(body), `data-restore="`+single+`"`) {
			t.Errorf("%s: the page of hidden images: %d", when, resp.StatusCode)
		}
	}
	check("right after hiding")

	// The list is in the state directory, not in the cache, and holds keys only.
	raw, err := os.ReadFile(listFile)
	if err != nil || !strings.Contains(string(raw), several) || !strings.Contains(string(raw), single) {
		t.Fatalf("the list on disk: %q (%v)", raw, err)
	}
	for _, secret := range st.secrets {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the list on disk contains %q", secret)
		}
	}
	if entries, _ := os.ReadDir(stateDir); len(entries) != 1 {
		t.Errorf("the state directory holds %d files", len(entries))
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "hidden.json")); err == nil {
		t.Error("the list was written to the cache directory")
	}

	// Stop, wipe the cache, start: the list is state and survives both.
	st.stop()
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatal(err)
	}
	mark := len(st.logs.String())
	st.start()
	check("after a restart and a cache wipe")
	if log := st.logs.String()[mark:]; !strings.Contains(log, `msg="hidden images" count=2`) || strings.Contains(log, "kept in the cache directory") {
		t.Errorf("the log after the restart:\n%s", tail(log, 2000))
	}

	// Restore both: everything as it was, and an empty list on disk.
	if resp, _ := viewer("POST", "/api/admin/restore/"+single); resp.StatusCode != 404 {
		t.Errorf("a viewer's POST restore: %d", resp.StatusCode)
	}
	for _, key := range both {
		if resp, body := admin("POST", "/api/admin/restore/"+key); resp.StatusCode != 200 {
			t.Fatalf("POST restore: %d %s", resp.StatusCode, body)
		}
	}
	if got, _, _ := listing(); !slices.Equal(got, before) {
		t.Fatalf("after restoring, the listing has %d tiles, want %d", len(got), len(before))
	}
	for _, key := range both {
		for _, u := range urls(key) {
			if resp, _ := viewer("GET", u); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
				t.Errorf("GET %s after restoring: %d", u, resp.StatusCode)
			}
		}
	}
	if got := hiddenKeys(); len(got) != 0 {
		t.Errorf("the admin's list after restoring: %q", got)
	}

	// A corrupt list: the gallery starts, says so, keeps the file under
	// another name and shows everything.
	if resp, _ := admin("POST", "/api/admin/hide/"+single); resp.StatusCode != 200 {
		t.Fatal("hiding again failed")
	}
	st.stop()
	good, _ := os.ReadFile(listFile)
	const broken = `{"version":1,"hidden":[{"key":"` // cut off, as a torn copy would be
	if err := os.WriteFile(listFile, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	mark = len(st.logs.String())
	st.start()
	if got, _, _ := listing(); !slices.Equal(got, before) {
		t.Errorf("with a corrupt list, the listing has %d tiles, want all %d", len(got), len(before))
	}
	if log := st.logs.String()[mark:]; !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "the list of hidden images is corrupt") {
		t.Errorf("the log does not report the corrupt list:\n%s", tail(log, 2000))
	}
	entries, _ := os.ReadDir(stateDir)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "hidden.json.corrupt-") {
		t.Fatalf("the state directory after starting on a corrupt list: %v", entries)
	}
	if kept, _ := os.ReadFile(filepath.Join(stateDir, entries[0].Name())); string(kept) != broken {
		t.Errorf("the corrupt list was not kept as it was: %q", kept)
	}
	// Hiding works again, and the copy stays. Then put the owner's list back, as the docs describe.
	if resp, _ := admin("POST", "/api/admin/hide/"+several); resp.StatusCode != 200 {
		t.Error("hiding after a corrupt list failed")
	}
	st.stop()
	if err := os.WriteFile(listFile, good, 0o600); err != nil {
		t.Fatal(err)
	}
	st.start()
	if got := hiddenKeys(); !slices.Equal(got, []string{single}) {
		t.Errorf("after the list was put back: %q", got)
	}
	if resp, _ := admin("POST", "/api/admin/restore/"+single); resp.StatusCode != 200 {
		t.Error("the last restore failed")
	}
	if got, _, _ := listing(); !slices.Equal(got, before) {
		t.Error("the listing was not left as it was found")
	}
}
