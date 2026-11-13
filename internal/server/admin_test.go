package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/robdevops/dalibot-gallery/internal/hidden"
	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/source"
	"github.com/robdevops/dalibot-gallery/web"
)

// answer is everything about a response that could tell one case from another.
type answer struct {
	status                   int
	body                     string
	ctype, cache, loc, allow string
	etag, www, retry         string
}

func (e *env) answer(method, target string, opts ...reqOpt) answer {
	e.t.Helper()
	resp, body := e.do(method, target, nil, opts...)
	h := resp.Header
	return answer{resp.StatusCode, string(body), h.Get("Content-Type"), h.Get("Cache-Control"), h.Get("Location"),
		h.Get("Allow"), h.Get("ETag"), h.Get("WWW-Authenticate"), h.Get("Retry-After")}
}

type hiddenList struct {
	Hidden []hiddenItem `json:"hidden"`
	Gone   int          `json:"gone"`
	V      string       `json:"v"`
}

func (e *env) hiddenList(opts ...reqOpt) hiddenList {
	e.t.Helper()
	resp, body := e.get(adminListPath, opts...)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		e.t.Fatalf("GET %s: %d %s", adminListPath, resp.StatusCode, body)
	}
	var l hiddenList
	if err := json.Unmarshal(body, &l); err != nil {
		e.t.Fatal(err)
	}
	return l
}

// keys of the listing, as a viewer with these options gets them.
func (e *env) listed(opts ...reqOpt) []string {
	e.t.Helper()
	var keys []string
	target := "/api/tiles?limit=120"
	for {
		resp, body := e.get(target, opts...)
		if resp.StatusCode != 200 {
			e.t.Fatalf("GET %s: %d", target, resp.StatusCode)
		}
		var p page
		if err := json.Unmarshal(body, &p); err != nil {
			e.t.Fatal(err)
		}
		for _, tl := range p.Tiles {
			keys = append(keys, tl.Key)
			for _, img := range tl.Styles {
				if !slices.ContainsFunc(p.Tiles, func(o source.Tile) bool { return o.Key == img.Key }) {
					e.t.Errorf("tile %s names the style %s, which is not a tile of the page", tl.Key, img.Key)
				}
			}
		}
		if p.Next == nil {
			return keys
		}
		target = "/api/tiles?limit=120&cursor=" + *p.Next
	}
}

const sameOrigin = "http://example.com" // httptest's default Host

// Who may use the admin's routes: an admin, from the gallery's own pages.
// Everyone else gets what an unknown path gives (a POST: 404), the same
// whether the key exists, is hidden or was never there, and nothing changes.
func TestAdminAuthorization(t *testing.T) {
	view, admin := sessionValue(testToken), adminSessionValue(testAdminToken)
	for name, c := range map[string]struct {
		vars map[string]string
		// who is not an admin, and what an unknown page, API path and asset give them
		others              map[string][]reqOpt
		page, api, noAccess int
	}{
		"token and admin token": {tokenAndAdmin, map[string][]reqOpt{
			"view cookie":                        {cookie(view)},
			"view token as Basic":                {basic},
			"the admin message under the token":  {cookie(adminSessionValue(testToken)), basic},
			"view cookie and a wrong Basic":      {cookie(view), func(r *http.Request) { r.SetBasicAuth("u", "wrong-wrong-wrong-wrong") }},
			"view cookie from another site":      {cookie(view), header("Sec-Fetch-Site", "cross-site")},
			"view cookie, X-Forwarded-For local": {cookie(view), header("X-Forwarded-For", "127.0.0.1")},
		}, 404, 404, 401},
		"public and admin token": {publicAndAdmin, map[string][]reqOpt{
			"anonymous":                {},
			"a view cookie of nothing": {cookie(view)},
			"a wrong Basic password":   {func(r *http.Request) { r.SetBasicAuth("u", "wrong-wrong-wrong-wrong") }},
			"from a trusted proxy":     {from("127.0.0.1:9"), header("X-Forwarded-For", "127.0.0.1")},
		}, 404, 404, 0},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, records(6), c.vars)
			keys := e.store.Snapshot().Keys()
			listedKey, hiddenKey, ghost := keys[0], keys[1], source.Key("never recorded")
			if _, err := e.hidden.Hide(hiddenKey); err != nil {
				t.Fatal(err)
			}
			e.store.Rebuild()
			posts := []string{
				adminHidePath + listedKey, adminHidePath + hiddenKey, adminHidePath + ghost, adminHidePath + "x", adminHidePath,
				adminRestore + listedKey, adminRestore + hiddenKey, adminRestore + ghost,
				adminListPath, adminHiddenPage, adminAPI + "nothing",
			}
			unchanged := func(who string) {
				t.Helper()
				if e.hidden.Len() != 1 || !e.hidden.Has(hiddenKey) || e.store.Snapshot().Has(hiddenKey) || !e.store.Snapshot().Has(listedKey) {
					t.Fatalf("%s: the hidden list or the listing changed", who)
				}
			}
			n := 0
			for who, opts := range c.others {
				n++
				opts = append(opts, from(fmt.Sprintf("198.51.100.%d:1", n))) // each on its own allowance of wrong tokens
				// A POST: 404, whatever the path and the key.
				var first answer
				for i, target := range posts {
					got := e.answer("POST", target, opts...)
					if strings.HasPrefix(target, "/api/") {
						if got.status != 404 || got.body != `{"error":"not found"}`+"\n" || got.allow != "" {
							t.Errorf("%s: POST %s: %+v", who, target, got)
						}
						if i == 0 {
							first = got
						} else if got != first {
							t.Errorf("%s: POST %s is answered differently from POST %s:\n%+v\n%+v", who, target, posts[0], got, first)
						}
					} else if got.status != 404 || got.body != "not found\n" {
						t.Errorf("%s: POST %s: %+v", who, target, got)
					}
				}
				unchanged(who)
				// A GET or HEAD: exactly what a path that does not exist gives.
				for _, pair := range [][2]string{
					{adminHiddenPage, "/admin/nothing"}, {adminListPath, "/api/nothing"}, {adminAPI + "x", "/api/nothing"},
					{adminHidePath + listedKey, "/api/nothing"}, {adminRestore + hiddenKey, "/api/nothing"},
					{"/admin.js", "/nothing.js"}, {"/admin.css", "/nothing.css"},
				} {
					for _, method := range []string{"GET", "HEAD"} {
						got, want := e.answer(method, pair[0], opts...), e.answer(method, pair[1], opts...)
						if got != want || got.status != 404 {
							t.Errorf("%s: %s %s:\n got %+v\nlike %+v", who, method, pair[0], got, want)
						}
					}
				}
				// Other methods are refused as everywhere, without naming POST.
				for _, target := range []string{adminHidePath + listedKey, adminHiddenPage} {
					if got := e.answer("DELETE", target, opts...); got.status != 405 || got.allow != "GET, HEAD" {
						t.Errorf("%s: DELETE %s: %+v", who, target, got)
					}
				}
				// The hidden image is as gone as one that never existed, and the page has no admin parts.
				tag := imaging.Tag()
				for _, f := range []string{"%s.jpg", "%s-240.webp?v=" + tag} {
					got, want := e.answer("GET", "/img/"+fmt.Sprintf(f, hiddenKey), opts...), e.answer("GET", "/img/"+fmt.Sprintf(f, ghost), opts...)
					if got != want || got.status != 404 {
						t.Errorf("%s: the hidden image: %+v, an unknown one: %+v", who, got, want)
					}
				}
				_, body := e.get("/", opts...)
				for _, not := range []string{"admin.js", "admin.css", "/admin/", "Hidden", hiddenKey} {
					if strings.Contains(string(body), not) {
						t.Errorf("%s: the page contains %q", who, not)
					}
				}
			}
			unchanged("after all of them")

			// Someone with no access at all (when viewing needs the token).
			if c.noAccess != 0 {
				for _, target := range posts {
					if got := e.answer("POST", target); got.status != 404 {
						t.Errorf("no session: POST %s: %+v", target, got)
					}
				}
				for _, pair := range [][2]string{{adminHiddenPage, "/admin/nothing"}, {adminListPath, "/api/nothing"}, {"/admin.js", "/nothing.js"}, {"/admin.css", "/nothing.css"}} {
					got, want := e.answer("GET", pair[0]), e.answer("GET", pair[1])
					if got != want || (got.status != 401 && got.status != 303) {
						t.Errorf("no session: GET %s:\n got %+v\nlike %+v", pair[0], got, want)
					}
				}
				unchanged("no session")
			}

			// An admin whose request comes from another site's page: refused, nothing changes.
			for _, how := range [][]reqOpt{{cookie(admin)}, {basicAdmin}} {
				for what, opt := range map[string]reqOpt{
					"Sec-Fetch-Site: cross-site": header("Sec-Fetch-Site", "cross-site"),
					"Sec-Fetch-Site: same-site":  header("Sec-Fetch-Site", "same-site"),
					"a foreign Origin":           header("Origin", "https://evil.example"),
				} {
					for _, target := range []string{adminHidePath + listedKey, adminRestore + hiddenKey, adminHidePath + ghost} {
						got := e.answer("POST", target, append(how, opt)...)
						if got.status != 403 || got.body != `{"error":"cross-origin request refused"}`+"\n" {
							t.Errorf("admin, %s: POST %s: %+v", what, target, got)
						}
					}
				}
			}
			unchanged("cross-origin")

			// An admin over the limit of wrong tokens is not an admin until it has passed.
			blocked := from("198.51.100.200:1")
			for range loginPerClient {
				e.get("/", blocked, func(r *http.Request) { r.SetBasicAuth("u", "wrong-wrong-wrong-wrong") })
			}
			if got := e.answer("POST", adminHidePath+listedKey, blocked, basicAdmin); got.status != 404 {
				t.Errorf("blocked admin: %+v", got)
			}
			unchanged("blocked admin")

			// The admin, by cookie and by Basic password, from the gallery's own page or a script.
			for how, opts := range map[string][]reqOpt{
				"cookie":                       {cookie(admin)},
				"Basic":                        {basicAdmin},
				"cookie, Sec-Fetch-Site":       {cookie(admin), header("Sec-Fetch-Site", "same-origin")},
				"cookie, the gallery's Origin": {cookie(admin), header("Origin", sameOrigin)},
			} {
				if got := e.answer("GET", adminHiddenPage, opts...); got.status != 200 || !strings.HasPrefix(got.ctype, "text/html") || got.cache != "no-store" {
					t.Errorf("admin by %s: the page: %+v", how, got)
				}
				if l := e.hiddenList(opts...); len(l.Hidden) != 1 || l.Hidden[0].Key != hiddenKey {
					t.Errorf("admin by %s: the list: %+v", how, l)
				}
				for _, a := range []string{"/admin.js", "/admin.css"} {
					if got := e.answer("GET", a, opts...); got.status != 200 || got.cache != "private, no-cache" || got.etag == "" {
						t.Errorf("admin by %s: %s: %+v", how, a, got)
					}
				}
				if got := e.answer("POST", adminHidePath+listedKey, opts...); got.status != 200 {
					t.Errorf("admin by %s: hide: %+v", how, got)
				}
				if got := e.answer("POST", adminRestore+listedKey, opts...); got.status != 200 {
					t.Errorf("admin by %s: restore: %+v", how, got)
				}
			}
			unchanged("after the admin put things back")
			// Methods, for the admin: each route says what it takes.
			for _, m := range []struct{ method, target, allow string }{
				{"GET", adminHidePath + listedKey, "POST"}, {"HEAD", adminRestore + hiddenKey, "POST"},
				{"POST", adminHiddenPage, "GET, HEAD"}, {"POST", adminListPath, "GET, HEAD"}, {"POST", adminAPI + "x", "GET, HEAD"},
				{"DELETE", adminHidePath + listedKey, "GET, HEAD"},
			} {
				if got := e.answer(m.method, m.target, cookie(admin)); got.status != 405 || got.allow != m.allow {
					t.Errorf("admin: %s %s: %+v", m.method, m.target, got)
				}
			}
			if got := e.answer("GET", adminAPI+"x", cookie(admin)); got.status != 404 {
				t.Errorf("admin: an unknown admin path: %+v", got)
			}
		})
	}
}

// Without an admin token the routes do not exist, for anyone, whatever cookie is sent.
func TestNoAdminTokenNoAdminRoutes(t *testing.T) {
	for name, vars := range map[string]map[string]string{"token": nil, "public": publicOnly} {
		e := newEnv(t, records(4), vars)
		key := e.store.Snapshot().Keys()[0]
		for who, opt := range map[string]reqOpt{
			"viewer":                         basic,
			"an admin cookie of nothing":     cookie(adminSessionValue(testAdminToken)),
			"an empty admin session":         cookie(sessionMAC("", adminSessionMessage)),
			"the admin token nobody set":     basicAdmin,
			"the view token as admin cookie": cookie(adminSessionValue(testToken)),
		} {
			opts := []reqOpt{opt, from("198.51.100.7:1")}
			if name == "token" {
				// Let in as a viewer, by whichever way the case itself does not use.
				if strings.Contains(who, "cookie") || strings.Contains(who, "session") {
					opts = append(opts, basic)
				} else {
					opts = append([]reqOpt{cookie(sessionValue(testToken))}, opts...)
				}
			}
			for _, target := range []string{adminHidePath + key, adminRestore + key} {
				if got := e.answer("POST", target, opts...); got.status != 405 || got.allow != "GET, HEAD" {
					t.Errorf("%s, %s: POST %s: %+v", name, who, target, got)
				}
			}
			for _, target := range []string{adminHiddenPage, adminListPath, "/admin.js", "/admin.css"} {
				if got := e.answer("GET", target, opts...); got.status != 404 {
					t.Errorf("%s, %s: GET %s: %+v", name, who, target, got)
				}
			}
			if _, body := e.get("/", opts...); strings.Contains(string(body), "admin") {
				t.Errorf("%s, %s: the page mentions admin", name, who)
			}
		}
		if e.hidden.Len() != 0 || !e.store.Snapshot().Has(key) {
			t.Errorf("%s: something was hidden", name)
		}
	}

	// An admin token but no hidden list (a server built without one): an
	// admin session, but nothing to hide with.
	e := newEnv(t, records(4), tokenAndAdmin)
	srv, err := New(Deps{Config: e.cfg, Store: e.store, Fetcher: e.fetcher, Cache: e.cache, Web: web.Files})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	e.h = srv.Handler()
	admin := cookie(adminSessionValue(testAdminToken))
	if got := e.answer("POST", adminHidePath+e.store.Snapshot().Keys()[0], admin); got.status != 405 {
		t.Errorf("no hidden list: POST hide: %+v", got)
	}
	for _, target := range []string{adminHiddenPage, adminListPath, "/admin.js", "/admin.css"} {
		if got := e.answer("GET", target, admin); got.status != 404 {
			t.Errorf("no hidden list: GET %s: %+v", target, got)
		}
	}
	_, body := e.get("/", admin)
	if s := string(body); !strings.Contains(s, `data-admin="1"`) || strings.Contains(s, "admin.js") || strings.Contains(s, "/admin/") {
		t.Error("no hidden list: the admin's page offers hiding")
	}
}

// Hiding and restoring: in force when the answer arrives, for the listing,
// the page and every image route, and kept on disk.
func TestHideAndRestore(t *testing.T) {
	for name, vars := range map[string]map[string]string{"token": tokenAndAdmin, "public": publicAndAdmin} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, records(9), vars) // records 0, 3 and 6 have two images
			viewer := []reqOpt{basic}
			if e.cfg.Public {
				viewer = nil
			}
			admin := cookie(adminSessionValue(testAdminToken))
			tag := imaging.Tag()
			snap := e.store.Snapshot()
			before := e.listed(viewer...)
			if len(before) != 12 {
				t.Fatalf("%d tiles", len(before))
			}
			monet := source.Key(fileID(0, "monet")) // one of two images of the newest record
			base := source.Key(fileID(0, "base"))
			single := source.Key(fileID(1, "base")) // the only image of its record
			ghost := source.Key("never recorded")
			urls := func(key string) []string {
				return []string{"/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + tag, "/img/" + key + "-480.webp?v=" + tag}
			}
			// Seen once, so that the cache has the files and a browser would have validators.
			etags := map[string]string{}
			for _, key := range []string{monet, single} {
				for _, u := range urls(key) {
					resp, _ := e.get(u, viewer...)
					if resp.StatusCode != 200 || resp.Header.Get("ETag") == "" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
						t.Fatalf("%s before hiding: %d %v", u, resp.StatusCode, resp.Header)
					}
					etags[u] = resp.Header.Get("ETag")
				}
			}
			fetches := e.fetcher.total()

			for _, key := range []string{monet, single} {
				resp, body := e.do("POST", adminHidePath+key, nil, admin, header("Sec-Fetch-Site", "same-origin"))
				if resp.StatusCode != 200 || string(body) != `{"key":"`+key+`","hidden":true}`+"\n" || resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("hide %s: %d %s", key, resp.StatusCode, body)
				}
			}
			// No refresh has run: the change is in the snapshot already.
			if e.store.Snapshot() == snap {
				t.Fatal("the snapshot was not rebuilt")
			}
			want := slices.DeleteFunc(slices.Clone(before), func(k string) bool { return k == monet || k == single })
			for who, opts := range map[string][]reqOpt{"viewer": viewer, "admin": {admin}} {
				if got := e.listed(opts...); !slices.Equal(got, want) {
					t.Errorf("%s: listing after hiding:\n got %q\nwant %q", who, got, want)
				}
				// Nothing the listing or the page sends names a hidden image.
				for _, target := range []string{"/", "/api/tiles?limit=120", "/api/tiles/new?tiles=1"} {
					_, body := e.get(target, opts...)
					for _, key := range []string{monet, single} {
						if strings.Contains(string(body), key) {
							t.Errorf("%s: GET %s names the hidden image %s", who, target, key)
						}
					}
				}
			}
			// The record that lost one of its two images shows the other, alone.
			_, p := e.page("/api/tiles?limit=1", viewer...)
			if len(p.Tiles) != 1 || p.Tiles[0].Key != base || len(p.Tiles[0].Styles) != 1 || p.Tiles[0].Current != "_" {
				t.Errorf("the record with one image hidden: %+v", p.Tiles)
			}

			// Image routes: for a viewer the hidden image is an unknown key,
			// also with the validators a browser kept from before.
			for _, key := range []string{monet, single} {
				for i, u := range urls(key) {
					unknown := e.answer("GET", urls(ghost)[i], viewer...)
					for what, opts := range map[string][]reqOpt{
						"plain":             viewer,
						"If-None-Match":     append(slices.Clone(viewer), header("If-None-Match", etags[u])),
						"If-None-Match: *":  append(slices.Clone(viewer), header("If-None-Match", "*")),
						"a view cookie too": append(slices.Clone(viewer), cookie(sessionValue(testToken))),
					} {
						if got := e.answer("GET", u, opts...); got != unknown || got.status != 404 || got.cache != "no-store" {
							t.Errorf("viewer, %s: GET %s: %+v, an unknown key: %+v", what, u, got, unknown)
						}
					}
					// The admin still sees it, to know what there is to restore, and no cache may keep that.
					got := e.answer("GET", u, admin, header("If-None-Match", etags[u]))
					if got.status != 200 || got.cache != "no-store" || got.etag != "" || len(got.body) < 100 {
						t.Errorf("admin: GET %s: %d cache=%q etag=%q", u, got.status, got.cache, got.etag)
					}
				}
			}
			// Listed images are served to the admin as to everyone, cacheable.
			if got := e.answer("GET", urls(base)[0], admin); got.status != 200 || !strings.Contains(got.cache, "immutable") || got.etag == "" {
				t.Errorf("admin: a listed image: %+v", got.cache)
			}
			if e.fetcher.total() != fetches+1 { // the one new original: base
				t.Errorf("Telegram was asked %d more times", e.fetcher.total()-fetches)
			}

			// The list of hidden images, newest hidden first, without record ids.
			l := e.hiddenList(admin)
			if len(l.Hidden) != 2 || l.Gone != 0 || l.V != tag {
				t.Fatalf("list: %+v", l)
			}
			got := map[string]hiddenItem{}
			for _, it := range l.Hidden {
				got[it.Key] = it
			}
			if it := got[monet]; it.Style != "monet" || it.Label != "Monet" || it.Prompt != "prompt 0 <b>&</b>" || it.Command != "dream" || it.Updated == 0 || it.HiddenAt == 0 {
				t.Errorf("list entry: %+v", it)
			}
			_, raw := e.get(adminListPath, admin)
			if strings.Contains(string(raw), `"id"`) || strings.Contains(string(raw), "-100") {
				t.Errorf("the list carries a record id: %s", raw)
			}
			_, html := e.get(adminHiddenPage, admin)
			for _, want := range []string{
				`<html lang="en" data-admin="1">`, `<script src="/admin.js" defer></script>`,
				`data-restore="` + monet + `"`, `data-restore="` + single + `"`,
				`src="/img/` + monet + `-240.webp?v=` + tag + `"`, `prompt 0 &lt;b&gt;&amp;&lt;/b&gt;`, `/dream · Monet`,
				`<p id="none" hidden>`,
			} {
				if !strings.Contains(string(html), want) {
					t.Errorf("the page of hidden images lacks %q", want)
				}
			}
			if strings.Contains(string(html), "-100") {
				t.Error("the page of hidden images carries a chat id")
			}
			// The admin's gallery page loads the admin's files and links to that page.
			_, index := e.get("/", admin)
			for _, want := range []string{`<link rel="stylesheet" href="/admin.css">`, `<script src="/admin.js" defer></script>`, `<a href="/admin/hidden">Hidden images</a>`} {
				if !strings.Contains(string(index), want) {
					t.Errorf("the admin's page lacks %q", want)
				}
			}

			// On disk, and still hidden after a refresh and in a new process.
			file, err := os.ReadFile(filepath.Join(e.cfg.StateDir, hidden.FileName))
			if err != nil || !strings.Contains(string(file), monet) || !strings.Contains(string(file), single) {
				t.Fatalf("the file: %s (%v)", file, err)
			}
			if err := e.store.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := e.listed(viewer...); !slices.Equal(got, want) {
				t.Error("a refresh showed hidden images")
			}
			reopened, err := hidden.Open(e.cfg.StateDir, nil)
			if err != nil || reopened.Len() != 2 || !reopened.Has(monet) || !reopened.Has(single) {
				t.Errorf("the list as a new process reads it: %v", err)
			}

			// Hiding again changes nothing; things that are not there are not found.
			if got := e.answer("POST", adminHidePath+monet, admin); got.status != 200 || e.hidden.Len() != 2 {
				t.Errorf("hiding twice: %+v", got)
			}
			for _, target := range []string{
				adminHidePath + ghost, adminHidePath, adminHidePath + "short", adminHidePath + monet + "/", adminHidePath + monet + "x",
				adminHidePath + "../../" + monet, adminRestore + ghost, adminRestore + base, adminRestore, adminRestore + monet + ".jpg",
			} {
				if got := e.answer("POST", target, admin); got.status != 404 || got.body != `{"error":"not found"}`+"\n" {
					t.Errorf("POST %s: %+v", target, got)
				}
			}
			if e.hidden.Len() != 2 {
				t.Fatal("the list changed")
			}

			// Restore: everything as before, with the files still in the cache.
			fetches = e.fetcher.total()
			for _, key := range []string{single, monet} {
				resp, body := e.do("POST", adminRestore+key, nil, admin)
				if resp.StatusCode != 200 || string(body) != `{"key":"`+key+`","hidden":false}`+"\n" {
					t.Fatalf("restore %s: %d %s", key, resp.StatusCode, body)
				}
			}
			if got := e.listed(viewer...); !slices.Equal(got, before) {
				t.Errorf("listing after restoring:\n got %q\nwant %q", got, before)
			}
			for _, key := range []string{monet, single} {
				for _, u := range urls(key) {
					if got := e.answer("GET", u, viewer...); got.status != 200 || got.etag != etags[u] || !strings.Contains(got.cache, "immutable") {
						t.Errorf("GET %s after restoring: %d %q", u, got.status, got.cache)
					}
				}
			}
			if e.fetcher.total() != fetches {
				t.Error("a restored image was downloaded again")
			}
			if l := e.hiddenList(admin); len(l.Hidden) != 0 || l.Hidden == nil || l.Gone != 0 {
				t.Errorf("list after restoring: %+v", l)
			}
			if _, html := e.get(adminHiddenPage, admin); !strings.Contains(string(html), `<p id="none">No hidden images to show.</p>`) || strings.Contains(string(html), "data-restore") {
				t.Error("the page after restoring everything")
			}
			if got := e.answer("POST", adminRestore+monet, admin); got.status != 404 {
				t.Errorf("restoring twice: %+v", got)
			}
			if s := e.logs.String(); !strings.Contains(s, `msg="image hidden" key=`+monet) || !strings.Contains(s, `msg="image restored" key=`+monet) {
				t.Error("hiding and restoring are not in the log")
			}
		})
	}
}

// An image that leaves the bot's records while hidden stays on the list
// (counted, not shown), stays hidden if it returns, and can be restored.
func TestHiddenImageLeavesTheRecords(t *testing.T) {
	e := newEnv(t, nil, tokenAndAdmin)
	admin := cookie(adminSessionValue(testAdminToken))
	recs := records(4)
	src := &swapSource{records: recs}
	e.store = source.NewStore(src, source.Options{StoreDays: 90, DefaultCommand: "dream", Hidden: e.hidden.Has}, nil)
	srv, err := New(Deps{Config: e.cfg, Store: e.store, Fetcher: e.fetcher, Cache: e.cache, Hidden: e.hidden, Web: web.Files})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	e.h = srv.Handler()
	refresh := func(r []source.Record) {
		t.Helper()
		src.set(r)
		if err := e.store.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	refresh(recs)
	key := source.Key(fileID(1, "base"))
	if got := e.answer("POST", adminHidePath+key, admin); got.status != 200 {
		t.Fatalf("hide: %+v", got)
	}
	refresh(append(slices.Clone(recs[:1]), recs[2:]...)) // the bot deleted the record
	if l := e.hiddenList(admin); len(l.Hidden) != 0 || l.Gone != 1 {
		t.Errorf("list once the image is gone: %+v", l)
	}
	if _, html := e.get(adminHiddenPage, admin); !strings.Contains(string(html), "no longer in the bot's records: 1.") {
		t.Errorf("the page does not count the image that is gone: %s", html)
	}
	if got := e.answer("GET", "/img/"+key+".jpg", admin); got.status != 404 {
		t.Errorf("an image that is gone is served to the admin: %+v", got)
	}
	// Hiding it again is not an error, though it is not in the records now.
	if got := e.answer("POST", adminHidePath+key, admin); got.status != 200 {
		t.Errorf("hiding a hidden image that is gone: %+v", got)
	}
	refresh(recs) // it returns
	if slices.Contains(e.listed(basic), key) {
		t.Error("the image came back unhidden")
	}
	if l := e.hiddenList(admin); len(l.Hidden) != 1 || l.Gone != 0 {
		t.Errorf("list once the image is back: %+v", l)
	}
	refresh(append(slices.Clone(recs[:1]), recs[2:]...))
	if got := e.answer("POST", adminRestore+key, admin); got.status != 200 || e.hidden.Len() != 0 {
		t.Errorf("restoring an image that is gone: %+v", got)
	}
	refresh(recs)
	if !slices.Contains(e.listed(basic), key) {
		t.Error("the restored image is not listed when it returns")
	}
}

type swapSource struct {
	mu      sync.Mutex
	records []source.Record
}

func (s *swapSource) set(r []source.Record) {
	s.mu.Lock()
	s.records = r
	s.mu.Unlock()
}

func (s *swapSource) Records(context.Context) ([]source.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records), nil
}

// A change that cannot be written is not made: the admin is told, and the
// listing stays as the file on disk says.
func TestHideCannotBeSaved(t *testing.T) {
	e := newEnv(t, records(4), publicAndAdmin)
	admin := cookie(adminSessionValue(testAdminToken))
	keys := e.store.Snapshot().Keys()
	if got := e.answer("POST", adminHidePath+keys[0], admin); got.status != 200 {
		t.Fatalf("hide: %+v", got)
	}
	// The list's name is taken by a directory that a rename cannot replace
	// (a read-only state directory would do the same, but not for root).
	path := filepath.Join(e.cfg.StateDir, hidden.FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{adminHidePath + keys[1], adminRestore + keys[0]} {
		got := e.answer("POST", target, admin)
		if got.status != 500 || got.body != `{"error":"the change could not be saved"}`+"\n" {
			t.Errorf("POST %s: %+v", target, got)
		}
	}
	if got := e.listed(); !slices.Equal(got, keys[1:]) {
		t.Errorf("listing after failed changes: %q", got)
	}
	if e.hidden.Len() != 1 || !e.hidden.Has(keys[0]) {
		t.Error("the list in memory changed")
	}
	if s := e.logs.String(); !strings.Contains(s, "level=ERROR") || !strings.Contains(s, "saving the list of hidden images failed") {
		t.Errorf("log: %s", s)
	}
	if list, _ := os.ReadDir(e.cfg.StateDir); len(list) != 1 {
		t.Errorf("files left in the state directory: %v", list)
	}
}

// The cache keeps the files of a hidden image while its record lasts, so
// restoring needs no second download; the warmer does not make thumbnails
// of hidden images.
func TestHiddenImagesAndTheCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(6), map[string]string{"GALLERY_ADMIN_TOKEN": testAdminToken, "GALLERY_WARM": "3"})
		e.store.OnSwap(e.srv.OnSwap)
		admin := cookie(adminSessionValue(testAdminToken))
		tag := imaging.Tag()
		keys := e.store.Snapshot().Keys()
		seen, cold := keys[0], keys[1]
		if resp, _ := e.get("/img/"+seen+"-240.webp?v="+tag, basic); resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		for _, key := range []string{cold, seen} { // cold first: hiding the other would warm it
			if got := e.answer("POST", adminHidePath+key, admin); got.status != 200 {
				t.Fatalf("hide: %+v", got)
			}
		}
		synctest.Wait() // the warm-up and the pruning that the rebuild started
		if !e.cache.HasOriginal(seen) || !e.cache.HasThumb(seen, 240, tag) {
			t.Error("the cache dropped the files of a hidden image")
		}
		if e.cache.HasOriginal(cold) || e.cache.HasThumb(cold, 240, tag) {
			t.Error("the warmer fetched a hidden image")
		}
		for _, key := range e.store.Snapshot().Keys()[:3] {
			if !e.cache.HasThumb(key, 240, tag) {
				t.Errorf("the newest listed tile %s was not warmed", key)
			}
		}
		// A refresh prunes too, and still keeps them.
		if err := e.store.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !e.cache.HasOriginal(seen) {
			t.Error("a refresh pruned the original of a hidden image")
		}
		before := e.fetcher.count(fileID(0, "base")) + e.fetcher.count(fileID(0, "monet"))
		if got := e.answer("POST", adminRestore+seen, admin); got.status != 200 {
			t.Fatalf("restore: %+v", got)
		}
		synctest.Wait()
		if resp, _ := e.get("/img/"+seen+".jpg", basic); resp.StatusCode != 200 {
			t.Errorf("the restored image: %d", resp.StatusCode)
		}
		if after := e.fetcher.count(fileID(0, "base")) + e.fetcher.count(fileID(0, "monet")); after != before {
			t.Error("the restored image was downloaded again")
		}
	})
}

// Many admins' requests at once, with viewers reading: no race, and the end
// state is what the last change of each key says.
func TestHideConcurrently(t *testing.T) {
	e := newEnv(t, records(40), publicAndAdmin)
	admin := cookie(adminSessionValue(testAdminToken))
	keys := e.store.Snapshot().Keys()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := w; i < len(keys); i += 8 {
				if got := e.answer("POST", adminHidePath+keys[i], admin); got.status != 200 {
					t.Errorf("hide: %+v", got)
				}
				if i%2 == 0 {
					if got := e.answer("POST", adminRestore+keys[i], admin); got.status != 200 {
						t.Errorf("restore: %+v", got)
					}
				}
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 20 {
				e.get("/api/tiles?limit=120")
				e.get(adminListPath, admin)
				_ = e.store.Refresh(context.Background())
			}
		})
	}
	wg.Wait()
	var want []string
	for i, k := range keys {
		if i%2 == 0 {
			want = append(want, k)
		}
	}
	if got := e.listed(); !slices.Equal(got, want) {
		t.Errorf("listing:\n got %q\nwant %q", got, want)
	}
	if l := e.hiddenList(admin); len(l.Hidden) != len(keys)-len(want) || l.Gone != 0 {
		t.Errorf("hidden: %d, gone %d", len(l.Hidden), l.Gone)
	}
}
