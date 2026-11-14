package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/cache"
	"github.com/robdevops/dalibot-gallery/internal/config"
	"github.com/robdevops/dalibot-gallery/internal/hidden"
	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/source"
	"github.com/robdevops/dalibot-gallery/internal/telegram"
	"github.com/robdevops/dalibot-gallery/web"
)

const (
	testToken      = "unit-test-gallery-token-xyz"
	testAdminToken = "unit-test-admin-token-0123456789"
	testBotToken   = "123456:TEST-token_abc"
	fileIDMark     = "AgACAgUAAxkBAAIUNITTEST"
)

var sampleJPEG []byte

func TestMain(m *testing.M) {
	var err error
	if sampleJPEG, err = os.ReadFile("../../testdata/telegram/telegram-01.jpg"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := imaging.Startup(imaging.Options{Workers: 4, Quality: 76}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	imaging.Shutdown()
	os.Exit(code)
}

// fakeFetcher stands in for Telegram.
type fakeFetcher struct {
	mu    sync.Mutex
	calls map[string]int
	errs  map[string]error  // file ids that fail with this error
	fail  map[string]bool   // file ids that fail
	data  map[string][]byte // file ids with special bytes
	delay time.Duration
}

func (f *fakeFetcher) Fetch(ctx context.Context, fileID string) ([]byte, error) {
	f.mu.Lock()
	f.calls[fileID]++
	fail, data, delay, failWith := f.fail[fileID], f.data[fileID], f.delay, f.errs[fileID]
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if failWith != nil {
		return nil, failWith
	}
	if fail {
		return nil, errors.New("telegram says no")
	}
	if data != nil {
		return data, nil
	}
	return sampleJPEG, nil
}

func (f *fakeFetcher) count(fileID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[fileID]
}

func (f *fakeFetcher) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func (f *fakeFetcher) setFail(fileID string, fail bool) {
	f.mu.Lock()
	f.fail[fileID] = fail
	f.mu.Unlock()
}

type env struct {
	t       *testing.T
	srv     *Server
	h       http.Handler
	store   *source.Store
	fetcher *fakeFetcher
	cache   *cache.Cache
	hidden  *hidden.List // the list behind the store's Hidden rule, as in main
	cfg     *config.Config
	secrets []string // nothing in here may appear in any response
	logs    *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fileID(i int, style string) string {
	return fmt.Sprintf("%s-%04d-%s-fileid", fileIDMark, i, style)
}

// records makes n records one second apart, newest first by index; every third has a second style.
func records(n int) []source.Record {
	now := time.Now().Unix()
	out := make([]source.Record, 0, n)
	for i := range n {
		r := source.Record{
			ID: fmt.Sprintf("-100%d:%d", 1+i%3, 1000+i), Prompt: fmt.Sprintf("prompt %d <b>&</b>", i), Command: "dream", Current: "_",
			TTL:    now - int64(i) + 90*86400,
			Images: map[string]source.RawImage{"_": {FileID: fileID(i, "base"), Caption: fmt.Sprintf("caption %d \"q\"", i)}},
		}
		if i%3 == 0 {
			r.Images["monet"] = source.RawImage{FileID: fileID(i, "monet"), Caption: ""}
		}
		out = append(out, r)
	}
	return out
}

func newEnv(t *testing.T, recs []source.Record, vars map[string]string) *env {
	t.Helper()
	m := map[string]string{
		"GALLERY_TOKEN": testToken, "TELEGRAM_BOT_TOKEN": testBotToken,
		"GALLERY_CACHE_DIR": t.TempDir(), "GALLERY_STATE_DIR": t.TempDir(), "GALLERY_WARM": "10",
	}
	maps.Copy(m, vars)
	cfg, err := config.FromEnv(func(k string) string { return m[k] })
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(RedactingHandler(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}), cfg.Secrets()...))
	ca, err := cache.Open(cfg.CacheDir, cfg.CacheMaxBytes(), log)
	if err != nil {
		t.Fatal(err)
	}
	hid, err := hidden.Open(cfg.StateDir, log)
	if err != nil {
		t.Fatal(err)
	}
	store := source.NewStore(source.Static(recs), source.Options{StoreDays: 90, DefaultCommand: "dream", Hidden: hid.Has}, log)
	if recs != nil {
		if err := store.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ff := &fakeFetcher{calls: map[string]int{}, fail: map[string]bool{}, data: map[string][]byte{}, errs: map[string]error{}}
	srv, err := New(Deps{Config: cfg, Store: store, Fetcher: ff, Cache: ca, Hidden: hid, Web: web.Files, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	e := &env{t: t, srv: srv, h: srv.Handler(), store: store, fetcher: ff, cache: ca, hidden: hid, cfg: cfg, logs: logs,
		secrets: []string{testToken, testAdminToken, testBotToken, fileIDMark, "api.telegram.org", "file_path", "file_id"}}
	return e
}

type reqOpt func(*http.Request)

func basic(r *http.Request)   { r.SetBasicAuth("anyone", testToken) }
func gzipped(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip, br") }
func header(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

// do sends one request and checks what every response must satisfy: the
// security headers, and no secret, file_id or Telegram detail anywhere.
func (e *env) do(method, target string, body io.Reader, opts ...reqOpt) (*http.Response, []byte) {
	e.t.Helper()
	r := httptest.NewRequest(method, target, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	resp := w.Result()
	raw, _ := io.ReadAll(resp.Body)
	plain := raw
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			e.t.Fatalf("%s %s: bad gzip: %v", method, target, err)
		}
		if plain, err = io.ReadAll(zr); err != nil {
			e.t.Fatalf("%s %s: bad gzip: %v", method, target, err)
		}
	}
	for name, want := range map[string]string{
		"Content-Security-Policy": CSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Permissions-Policy":      PermissionsPolicy,
	} {
		if got := resp.Header.Get(name); got != want {
			e.t.Errorf("%s %s: header %s = %q, want %q", method, target, name, got, want)
		}
	}
	var hdr bytes.Buffer
	_ = resp.Header.Write(&hdr)
	for _, secret := range e.secrets {
		if bytes.Contains(plain, []byte(secret)) || bytes.Contains(raw, []byte(secret)) {
			e.t.Errorf("%s %s: the body contains %q", method, target, secret)
		}
		if strings.Contains(hdr.String(), secret) {
			e.t.Errorf("%s %s: a header contains %q", method, target, secret)
		}
	}
	return resp, plain
}

func (e *env) get(target string, opts ...reqOpt) (*http.Response, []byte) {
	e.t.Helper()
	return e.do("GET", target, nil, opts...)
}

type page struct {
	Tiles  []source.Tile `json:"tiles"`
	Next   *string       `json:"next"`
	Newest *string       `json:"newest"`
	V      string        `json:"v"`
	Count  *int          `json:"count"`
	Error  string        `json:"error"`
}

func (e *env) page(target string, opts ...reqOpt) (*http.Response, page) {
	e.t.Helper()
	resp, body := e.get(target, append([]reqOpt{basic}, opts...)...)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		e.t.Fatalf("%s: Content-Type %q", target, ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		e.t.Errorf("%s: Cache-Control %q", target, cc)
	}
	if v := resp.Header.Get("Vary"); v != "Accept-Encoding" {
		e.t.Errorf("%s: Vary %q", target, v)
	}
	var p page
	if err := json.Unmarshal(body, &p); err != nil {
		e.t.Fatalf("%s: %v in %s", target, err, body)
	}
	return resp, p
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, records(5), nil)
	key := e.store.Snapshot().Keys()[0]

	resp, _ := e.get("/")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Errorf("GET / without auth: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.get("/no/such/page")
	if resp.StatusCode != 303 {
		t.Errorf("unknown page without auth: %d", resp.StatusCode)
	}
	for _, target := range []string{"/api/tiles", "/api/tiles/new", "/api/other"} {
		resp, body := e.get(target)
		if resp.StatusCode != 401 || strings.TrimSpace(string(body)) != `{"error":"unauthorized"}` {
			t.Errorf("%s without auth: %d %s", target, resp.StatusCode, body)
		}
		if resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s: WWW-Authenticate must not be sent", target)
		}
	}
	for _, target := range []string{"/img/" + key + "-240.webp?v=" + imaging.Tag(), "/img/" + key + ".jpg", "/img/x"} {
		resp, body := e.get(target)
		if resp.StatusCode != 401 || len(body) != 0 || resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s without auth: %d, %d bytes", target, resp.StatusCode, len(body))
		}
	}
	if e.fetcher.total() != 0 {
		t.Errorf("unauthenticated requests reached Telegram")
	}

	// Basic: the password is the token, any user name.
	if resp, _ := e.get("/api/tiles", basic); resp.StatusCode != 200 {
		t.Errorf("Basic auth: %d", resp.StatusCode)
	}
	for _, bad := range []string{"", "wrong", testToken + "x", testToken[:len(testToken)-1], sessionValue(testToken)} {
		resp, _ := e.get("/api/tiles", func(r *http.Request) { r.SetBasicAuth(testToken, bad) })
		if resp.StatusCode != 401 {
			t.Errorf("Basic password %q accepted", bad)
		}
	}

	// Open routes: the health check, the login page, its stylesheet and the icon. Nothing else.
	for _, target := range []string{"/healthz", "/app.css", "/favicon.png", "/login"} {
		if resp, _ := e.get(target); resp.StatusCode != 200 {
			t.Errorf("%s without auth: %d", target, resp.StatusCode)
		}
	}
	// The gallery's script is behind the login like every other route.
	resp, body := e.get("/app.js")
	if resp.StatusCode != 401 || strings.Contains(string(body), "function") || resp.Header.Get("ETag") != "" {
		t.Errorf("/app.js without auth: %d, %d bytes", resp.StatusCode, len(body))
	}
	if resp, _ := e.get("/app.js", header("If-None-Match", "*")); resp.StatusCode != 401 {
		t.Errorf("/app.js with If-None-Match without auth: %d", resp.StatusCode)
	}
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: sessionValue(testToken)}) }
	if resp, body := e.get("/app.js", cookie); resp.StatusCode != 200 || len(body) == 0 {
		t.Errorf("/app.js with the cookie: %d", resp.StatusCode)
	}
}

// Both pages carry the short description that link previews show. The login
// page has it too, because a preview fetcher is not signed in.
func TestLinkPreviewText(t *testing.T) {
	e := newEnv(t, records(3), nil)
	for target, opts := range map[string][]reqOpt{"/": {basic}, "/login": nil} {
		_, body := e.get(target, opts...)
		for _, want := range []string{
			`<meta name="description" content="Gallery for dalibot AI images">`,
			`<meta property="og:title" content="Gallery">`,
			`<meta property="og:description" content="Gallery for dalibot AI images">`,
			`<meta property="og:image" content="https://raw.githubusercontent.com/robdevops/dalibot-gallery/main/docs/og-image.png">`,
			`<meta property="og:image:width" content="480">`,
			`<meta name="twitter:card" content="summary">`,
		} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s: missing %s", target, want)
			}
		}
	}
}

func TestLoginCookie(t *testing.T) {
	e := newEnv(t, records(5), nil)
	post := func(token string, opts ...reqOpt) (*http.Response, []byte) {
		return e.do("POST", "/login", strings.NewReader(url.Values{"token": {token}}.Encode()), opts...)
	}

	resp, body := post("not the token")
	if resp.StatusCode != 401 || !strings.Contains(string(body), `role="alert"`) || len(resp.Cookies()) != 0 {
		t.Fatalf("wrong token: %d, cookies %v", resp.StatusCode, resp.Cookies())
	}
	resp, body = e.get("/login")
	if resp.StatusCode != 200 || strings.Contains(string(body), `role="alert"`) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET /login: %d", resp.StatusCode)
	}

	resp, _ = post(testToken)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("right token: %d", resp.StatusCode)
	}
	want := CookieName + "=" + sessionValue(testToken) + "; Path=/; Max-Age=31536000; HttpOnly; Secure; SameSite=Strict"
	if got := resp.Header.Get("Set-Cookie"); got != want {
		t.Fatalf("Set-Cookie = %q, want %q", got, want)
	}
	cookie := resp.Cookies()[0]
	if cookie.Value == testToken || len(cookie.Value) != 64 {
		t.Fatalf("the cookie must hold an HMAC, not the token")
	}
	withCookie := func(v string) reqOpt {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: v}) }
	}
	if resp, _ := e.get("/", withCookie(cookie.Value)); resp.StatusCode != 200 {
		t.Errorf("GET / with the cookie: %d", resp.StatusCode)
	}
	if resp, _ := e.get("/api/tiles", withCookie(cookie.Value)); resp.StatusCode != 200 {
		t.Errorf("API with the cookie: %d", resp.StatusCode)
	}
	if resp, _ := e.get("/login", withCookie(cookie.Value)); resp.StatusCode != 303 {
		t.Errorf("GET /login when signed in: %d", resp.StatusCode)
	}
	for _, forged := range []string{"", testToken, strings.Repeat("0", 64), cookie.Value[:63], strings.ToUpper(cookie.Value)} {
		if resp, _ := e.get("/api/tiles", withCookie(forged)); resp.StatusCode != 401 {
			t.Errorf("forged cookie %q accepted", forged)
		}
	}

	// An oversized body is refused.
	big := strings.NewReader("token=" + strings.Repeat("a", 5000))
	if resp, _ := e.do("POST", "/login", big); resp.StatusCode != 400 {
		t.Errorf("oversized login body: %d", resp.StatusCode)
	}
	// Neither the cookie value nor the token is ever logged.
	if logs := e.logs.String(); strings.Contains(logs, cookie.Value) || strings.Contains(logs, testToken) {
		t.Errorf("the log contains the cookie value or the token:\n%s", logs)
	}
}

// A sign-in posted from another site's page is refused, with the right token
// too, and does not count as a failed attempt.
func TestLoginCrossOrigin(t *testing.T) {
	e := newEnv(t, records(5), nil)
	post := func(opts ...reqOpt) *http.Response {
		resp, _ := e.do("POST", "/login", strings.NewReader(url.Values{"token": {testToken}}.Encode()), opts...)
		return resp
	}
	header := func(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

	for i := range 20 { // far more than the limiter allows failures
		for name, opt := range map[string]reqOpt{
			"Sec-Fetch-Site: cross-site": header("Sec-Fetch-Site", "cross-site"),
			"Sec-Fetch-Site: same-site":  header("Sec-Fetch-Site", "same-site"),
			"foreign Origin":             header("Origin", "https://evil.example"),
		} {
			if resp := post(opt); resp.StatusCode != 403 || len(resp.Cookies()) != 0 {
				t.Fatalf("round %d, %s: status %d, cookies %v; want 403 and none", i, name, resp.StatusCode, resp.Cookies())
			}
		}
	}
	// The browser's own form, and clients that send neither header, still sign in.
	for name, opts := range map[string][]reqOpt{
		"Sec-Fetch-Site: same-origin": {header("Sec-Fetch-Site", "same-origin")},
		"Sec-Fetch-Site: none":        {header("Sec-Fetch-Site", "none")},
		"no headers":                  nil,
	} {
		if resp := post(opts...); resp.StatusCode != 303 || len(resp.Cookies()) != 1 {
			t.Errorf("%s: status %d; want 303 and the cookie", name, resp.StatusCode)
		}
	}
	// Reading the page is never refused.
	if resp, _ := e.get("/login", header("Sec-Fetch-Site", "cross-site")); resp.StatusCode != 200 {
		t.Errorf("cross-site GET /login: %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(1), nil)
		post := func(token, addr string) *http.Response {
			resp, _ := e.do("POST", "/login", strings.NewReader("token="+token), func(r *http.Request) { r.RemoteAddr = addr })
			return resp
		}
		for i := range loginPerClient {
			if resp := post("wrong", "203.0.113.5:1000"); resp.StatusCode != 401 {
				t.Fatalf("attempt %d: %d", i, resp.StatusCode)
			}
		}
		// The sixth is refused even with the right token, and says when to come back.
		resp := post(testToken, "203.0.113.5:2000")
		if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || len(resp.Cookies()) != 0 {
			t.Fatalf("after %d failures: %d, Retry-After %q", loginPerClient, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
		// Another client is not affected.
		if resp := post(testToken, "203.0.113.6:1000"); resp.StatusCode != 303 {
			t.Fatalf("another client: %d", resp.StatusCode)
		}
		// After the window the first client may try again.
		time.Sleep(loginWindow + time.Second)
		if resp := post(testToken, "203.0.113.5:3000"); resp.StatusCode != 303 {
			t.Fatalf("after the window: %d", resp.StatusCode)
		}
		// The overall limit: many clients, one failure each.
		for i := range loginOverall {
			if resp := post("wrong", fmt.Sprintf("198.51.%d.%d:1", 100+i/250, i%250)); resp.StatusCode != 401 {
				t.Fatalf("overall attempt %d: %d", i, resp.StatusCode)
			}
		}
		if resp := post(testToken, "192.0.2.1:1"); resp.StatusCode != 429 {
			t.Fatalf("overall limit: %d", resp.StatusCode)
		}
	})
}

// A few clients cannot lock everyone else out: the overall ceiling is far
// above what the per-client limit lets them reach.
func TestLoginLockoutNeedsManyClients(t *testing.T) {
	e := newEnv(t, records(1), nil)
	post := func(token, addr string) int {
		resp, _ := e.do("POST", "/login", strings.NewReader("token="+token), func(r *http.Request) { r.RemoteAddr = addr })
		return resp.StatusCode
	}
	refused := 0
	for client := range 20 { // 20 clients, 40 wrong tokens each
		for range 40 {
			if post("wrong", fmt.Sprintf("198.51.100.%d:1", client)) == 401 {
				refused++
			}
		}
	}
	if refused != 20*loginPerClient {
		t.Fatalf("%d wrong tokens were evaluated, want %d", refused, 20*loginPerClient)
	}
	if code := post(testToken, "192.0.2.77:1"); code != 303 {
		t.Fatalf("an uninvolved client with the right token got %d", code)
	}
}

// Wrong Basic passwords count against the same limiter as failed logins, on
// every protected route, so Basic is not a faster way to guess the token.
func TestBasicAuthRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(2), nil)
		key := e.store.Snapshot().Keys()[0]
		targets := []string{"/api/tiles", "/", "/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + imaging.Tag(), "/app.js", "/nope"}
		try := func(target, pass, addr string) *http.Response {
			resp, _ := e.get(target, func(r *http.Request) { r.RemoteAddr = addr; r.SetBasicAuth("u", pass) })
			return resp
		}
		for i := range loginPerClient {
			target := targets[i%len(targets)]
			if resp := try(target, fmt.Sprintf("guess-%d", i), "203.0.113.5:1000"); resp.StatusCode == 200 || resp.StatusCode == 429 {
				t.Fatalf("wrong password %d on %s: %d", i, target, resp.StatusCode)
			}
		}
		// From now on the client is refused on every route, right password or not.
		for _, target := range targets {
			for _, pass := range []string{"guess-again", testToken} {
				resp := try(target, pass, "203.0.113.5:2000")
				if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
					t.Fatalf("%s after %d wrong passwords: %d, Retry-After %q", target, loginPerClient, resp.StatusCode, resp.Header.Get("Retry-After"))
				}
			}
		}
		if e.fetcher.total() != 0 {
			t.Error("a blocked client reached Telegram")
		}
		// The login form shares the count.
		resp, _ := e.do("POST", "/login", strings.NewReader("token="+testToken), func(r *http.Request) { r.RemoteAddr = "203.0.113.5:3000" })
		if resp.StatusCode != 429 {
			t.Fatalf("login after wrong Basic passwords: %d", resp.StatusCode)
		}
		// Blocked attempts are not recorded, so the block does not extend itself.
		if n := strings.Count(e.logs.String(), "basic auth failed"); n != loginPerClient {
			t.Errorf("%d failures logged, want %d", n, loginPerClient)
		}
		// A session cookie is not a guess: it keeps working for the blocked address.
		cookie := func(r *http.Request) {
			r.RemoteAddr = "203.0.113.5:4000"
			r.AddCookie(&http.Cookie{Name: CookieName, Value: sessionValue(testToken)})
		}
		if resp, _ := e.get("/api/tiles", cookie); resp.StatusCode != 200 {
			t.Fatalf("cookie from a blocked address: %d", resp.StatusCode)
		}
		// Other clients are unaffected, and right passwords never count.
		for i := range 3 * loginPerClient {
			if resp := try("/api/tiles", testToken, "203.0.113.6:1"); resp.StatusCode != 200 {
				t.Fatalf("right password, request %d: %d", i, resp.StatusCode)
			}
		}
		// Requests without credentials are not guesses either.
		for range 3 * loginPerClient {
			e.get("/api/tiles", func(r *http.Request) { r.RemoteAddr = "203.0.113.7:1" })
		}
		if resp := try("/api/tiles", testToken, "203.0.113.7:1"); resp.StatusCode != 200 {
			t.Fatalf("after anonymous requests: %d", resp.StatusCode)
		}
		// After the window the first client is back.
		time.Sleep(loginWindow + time.Second)
		if resp := try("/api/tiles", testToken, "203.0.113.5:5000"); resp.StatusCode != 200 {
			t.Fatalf("after the window: %d", resp.StatusCode)
		}
		// IPv6: the limit is per /64, so a client cannot step through its own prefix.
		for i := range loginPerClient {
			try("/api/tiles", fmt.Sprintf("wrong-%d", i), fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1))
		}
		if resp := try("/api/tiles", testToken, "[2001:db8:1:2:ffff::9]:1"); resp.StatusCode != 429 {
			t.Fatalf("another address in the same /64: %d", resp.StatusCode)
		}
		if resp := try("/api/tiles", testToken, "[2001:db8:1:3::1]:1"); resp.StatusCode != 200 {
			t.Fatalf("another /64: %d", resp.StatusCode)
		}
	})
}

// The limiter checks and records in one step. Many logins from one client
// that are all in flight at once (headers sent, bodies held back, then
// completed together) must not all be evaluated.
func TestLoginRateLimitConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(1), nil)
		ts := httptest.NewTestServer(t, e.h) // in-memory network, closed with the test
		client := ts.Client()                // also sets ts.URL
		const n = 60
		type pending struct {
			w    *io.PipeWriter
			done chan int
		}
		var all []pending
		for range n {
			pr, pw := io.Pipe()
			req, err := http.NewRequest("POST", ts.URL+"/login", pr)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			p := pending{w: pw, done: make(chan int, 1)}
			go func() {
				resp, err := client.Do(req)
				if err != nil {
					p.done <- 0
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				p.done <- resp.StatusCode
			}()
			// The first bytes of the body: the handler is now running and waiting for the rest.
			if _, err := pw.Write([]byte("tok")); err != nil {
				t.Fatal(err)
			}
			all = append(all, p)
		}
		synctest.Wait() // every request is in its handler, waiting for the rest of its body
		for i, p := range all {
			_, _ = p.w.Write([]byte(fmt.Sprintf("en=wrong-%d", i)))
			p.w.Close()
		}
		codes := map[int]int{}
		for _, p := range all {
			codes[<-p.done]++
		}
		if codes[401] != loginPerClient || codes[429] != n-loginPerClient {
			t.Fatalf("%d concurrent wrong logins from one client: %v, want %d x 401 and the rest 429", n, codes, loginPerClient)
		}
	})
}

func TestLimiterAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLoginLimiter()
		// Right tokens are never recorded.
		for i := range 100 {
			if wait, ok := l.attempt("a", true); wait != 0 || !ok {
				t.Fatalf("right token %d: %v %v", i, wait, ok)
			}
		}
		if len(l.clients) != 0 || len(l.all) != 0 {
			t.Fatalf("right tokens left state behind: %d clients, %d attempts", len(l.clients), len(l.all))
		}
		for i := range loginPerClient {
			if wait, ok := l.attempt("a", false); wait != 0 || ok {
				t.Fatalf("wrong token %d: %v %v", i, wait, ok)
			}
			time.Sleep(time.Second)
		}
		// Blocked: the wait runs to the end of the oldest failure's window, and nothing is recorded.
		wait, ok := l.attempt("a", true)
		if ok || wait != loginWindow-loginPerClient*time.Second {
			t.Fatalf("blocked: wait %v, ok %v", wait, ok)
		}
		if len(l.all) != loginPerClient {
			t.Fatalf("a blocked attempt was recorded: %d", len(l.all))
		}
		time.Sleep(wait)
		if wait, ok := l.attempt("a", true); wait != 0 || !ok {
			t.Fatalf("after the wait: %v %v", wait, ok)
		}
		// Old clients are forgotten.
		time.Sleep(2 * loginWindow)
		l.attempt("a", true)
		if len(l.clients) != 0 {
			t.Fatalf("%d clients remembered after their window", len(l.clients))
		}
	})
}

// A wrong Basic password repeated by the same client counts once; a
// different one counts again; tokens posted at /login always count; and a
// repeated password is still refused, never let through.
func TestLimiterRepeatedGuess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLoginLimiter()
		for i := range 10 * loginPerClient {
			wait, ok, repeated := l.guess("a", false, "same")
			if wait != 0 || ok || repeated != (i > 0) {
				t.Fatalf("same wrong password %d: %v %v %v", i, wait, ok, repeated)
			}
		}
		if len(l.clients["a"]) != 1 || len(l.all) != 1 {
			t.Fatalf("a repeated password was recorded: %d for the client, %d overall", len(l.clients["a"]), len(l.all))
		}
		for _, f := range l.clients["a"] {
			if strings.Contains(f.guess, "same") {
				t.Fatal("the limiter keeps the password itself")
			}
		}
		// Another client's same password is that client's first guess.
		if _, _, repeated := l.guess("b", false, "same"); repeated {
			t.Fatal("a password was treated as repeated for another client")
		}
		for i := range loginPerClient - 1 {
			if wait, ok, repeated := l.guess("a", false, fmt.Sprint("other-", i)); wait != 0 || ok || repeated {
				t.Fatalf("distinct wrong password %d: %v %v %v", i, wait, ok, repeated)
			}
		}
		if wait, ok, _ := l.guess("a", true, "right"); wait == 0 || ok {
			t.Fatalf("after %d distinct wrong passwords the client is not blocked: %v %v", loginPerClient, wait, ok)
		}
		// /login attempts are not deduplicated.
		for i := range loginPerClient {
			if wait, ok := l.attempt("c", false); wait != 0 || ok {
				t.Fatalf("wrong login %d: %v %v", i, wait, ok)
			}
		}
		if wait, _ := l.attempt("c", false); wait == 0 {
			t.Fatal("repeated wrong logins were not counted")
		}
		// After the window a repeated password is a new guess again.
		time.Sleep(loginWindow + time.Second)
		if _, _, repeated := l.guess("b", false, "same"); repeated {
			t.Fatal("a password was remembered past the window")
		}
	})
}

func TestClientAddr(t *testing.T) {
	prefixes := func(list ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, p := range list {
			out = append(out, netip.MustParsePrefix(p))
		}
		return out
	}
	loopback := prefixes("127.0.0.0/8", "::1/128")
	docker := prefixes("172.18.0.1/32", "198.51.100.0/24")
	for _, c := range []struct {
		trusted           []netip.Prefix
		remote, xff, want string
	}{
		{loopback, "203.0.113.5:1", "", "203.0.113.5"},
		{loopback, "203.0.113.5:1", "1.2.3.4", "203.0.113.5"}, // a public peer cannot choose its address
		{loopback, "127.0.0.1:1", "", "127.0.0.1"},
		{loopback, "127.0.0.1:1", "9.9.9.9, 1.2.3.4", "1.2.3.4"}, // the entry the proxy appended
		{loopback, "[::1]:1", "junk", "::1"},
		{loopback, "[::1]:1", "2001:db8::7", "2001:db8::7"},
		{loopback, "[::ffff:127.0.0.1]:1", "1.2.3.4", "1.2.3.4"},
		// A private peer that is not a configured proxy cannot choose its address either.
		{loopback, "10.0.0.2:1", "1.2.3.4", "10.0.0.2"},
		{loopback, "172.18.0.5:1", "1.2.3.4", "172.18.0.5"},
		{loopback, "192.168.1.9:1", "1.2.3.4", "192.168.1.9"},
		// No proxies trusted at all: always the peer.
		{nil, "127.0.0.1:1", "1.2.3.4", "127.0.0.1"},
		// A configured proxy, and a chain of two.
		{docker, "172.18.0.1:1", "1.2.3.4", "1.2.3.4"},
		{docker, "172.18.0.2:1", "1.2.3.4", "172.18.0.2"},
		{docker, "127.0.0.1:1", "1.2.3.4", "127.0.0.1"},
		{docker, "172.18.0.1:1", "6.6.6.6, 1.2.3.4, 198.51.100.9", "1.2.3.4"},
		{docker, "172.18.0.1:1", "198.51.100.8, 198.51.100.9", "198.51.100.8"}, // all trusted: the farthest
		{docker, "172.18.0.1:1", "junk, 198.51.100.9", "198.51.100.9"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientAddr(r, c.trusted); got != c.want {
			t.Errorf("clientAddr(%s, %q, %v) = %q, want %q", c.remote, c.xff, c.trusted, got, c.want)
		}
	}
	// Two header lines are read as one list.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Add("X-Forwarded-For", "1.2.3.4")
	r.Header.Add("X-Forwarded-For", "127.0.0.1")
	if got := clientAddr(r, loopback); got != "1.2.3.4" {
		t.Errorf("two header lines: %q", got)
	}
}

// A client on a private network that reaches the gallery directly cannot
// dodge the per-client limit by sending a different X-Forwarded-For each time.
func TestForwardedForCannotEvadeLimit(t *testing.T) {
	e := newEnv(t, records(1), nil)
	post := func(remote, xff string) int {
		resp, _ := e.do("POST", "/login", strings.NewReader("token=wrong"), func(r *http.Request) {
			r.RemoteAddr = remote
			r.Header.Set("X-Forwarded-For", xff)
		})
		return resp.StatusCode
	}
	for i := range 12 {
		want := 401
		if i >= loginPerClient {
			want = 429
		}
		if got := post("10.9.9.9:1", fmt.Sprintf("203.0.113.%d", i)); got != want {
			t.Fatalf("attempt %d from a private peer with a spoofed header: %d, want %d", i, got, want)
		}
	}
	// Behind the trusted proxy (loopback by default) clients are told apart.
	for i := range 12 {
		if got := post("127.0.0.1:1", fmt.Sprintf("203.0.113.%d", 100+i)); got != 401 {
			t.Fatalf("client %d behind the proxy: %d", i, got)
		}
	}
	if !strings.Contains(e.logs.String(), "remote=10.9.9.9") || strings.Contains(e.logs.String(), "remote=203.0.113.3 ") {
		t.Errorf("the log shows a spoofed address")
	}
}

func TestMethodsAndNotFound(t *testing.T) {
	e := newEnv(t, records(2), nil)
	for _, c := range []struct{ method, target, allow string }{
		{"POST", "/", "GET, HEAD"}, {"DELETE", "/api/tiles", "GET, HEAD"}, {"PUT", "/login", "GET, HEAD, POST"},
		{"POST", "/img/x.jpg", "GET, HEAD"}, {"POST", "/healthz", "GET, HEAD"}, {"PATCH", "/app.js", "GET, HEAD"},
		{"GET", "/logout", "POST"}, {"HEAD", "/logout", "POST"}, {"DELETE", "/logout", "POST"}, {"GE", "/", "GET, HEAD"},
	} {
		resp, _ := e.do(c.method, c.target, nil, basic)
		if resp.StatusCode != 405 || resp.Header.Get("Allow") != c.allow {
			t.Errorf("%s %s: %d, Allow %q", c.method, c.target, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	for _, target := range []string{"/nope", "/api/nope", "/img/", "/img/short.jpg", "/app.js/", "/index.html", "/login.html", "/favicon.ico"} {
		if resp, _ := e.get(target, basic); resp.StatusCode != 404 {
			t.Errorf("%s: %d, want 404", target, resp.StatusCode)
		}
	}
	// HEAD is GET without the body.
	r := httptest.NewRequest("HEAD", "/api/tiles", nil)
	basic(r)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD /api/tiles: %d", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, records(1), nil)
	resp, body := e.get("/healthz")
	if resp.StatusCode != 200 || string(body) != "ok\n" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("healthy: %d %q", resp.StatusCode, body)
	}
	stale := newEnv(t, nil, nil) // never refreshed
	resp, body = stale.get("/healthz")
	if resp.StatusCode != 503 || string(body) != "stale\n" {
		t.Errorf("stale: %d %q", resp.StatusCode, body)
	}
	// An empty listing still answers properly.
	_, p := stale.page("/api/tiles")
	if p.Tiles == nil || len(p.Tiles) != 0 || p.Next != nil || p.Newest != nil {
		t.Errorf("empty listing: %+v", p)
	}
	_, body = stale.get("/api/tiles", basic)
	if !strings.Contains(string(body), `"tiles":[]`) || !strings.Contains(string(body), `"next":null`) {
		t.Errorf("empty listing body: %s", body)
	}
}

func TestStaticAssets(t *testing.T) {
	e := newEnv(t, records(1), nil)
	for target, ctype := range map[string]string{"/app.js": "text/javascript; charset=utf-8", "/app.css": "text/css; charset=utf-8", "/favicon.png": "image/png"} {
		resp, body := e.get(target, basic, gzipped)
		etag := resp.Header.Get("ETag")
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != ctype || resp.Header.Get("Cache-Control") != "no-cache" ||
			!strings.HasPrefix(etag, `"`) || len(body) == 0 || resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: %d %v", target, resp.StatusCode, resp.Header)
		}
		resp, body = e.get(target, basic, header("If-None-Match", etag))
		if resp.StatusCode != 304 || len(body) != 0 {
			t.Errorf("%s with If-None-Match: %d", target, resp.StatusCode)
		}
		if resp, _ := e.get(target, basic, header("If-None-Match", `"other"`)); resp.StatusCode != 200 {
			t.Errorf("%s with another ETag: %d", target, resp.StatusCode)
		}
	}
}

func TestTilesPaging(t *testing.T) {
	e := newEnv(t, records(150), nil) // 150 records, 200 tiles
	snap := e.store.Snapshot()
	if snap.Len() != 200 {
		t.Fatalf("%d tiles", snap.Len())
	}

	_, first := e.page("/api/tiles")
	if n := len(first.Tiles); n < 59 || n > 60 { // pages hold whole records
		t.Errorf("default page: %d tiles", n)
	}
	if first.V != imaging.Tag() || first.Newest == nil || *first.Newest != snap.Newest(1)[0].Cursor() || first.Next == nil {
		t.Errorf("first page: v %q newest %v next %v", first.V, first.Newest, first.Next)
	}
	// The cap: anything above 120 is 120.
	for _, limit := range []string{"120", "121", "5000"} {
		_, p := e.page("/api/tiles?limit=" + limit)
		if n := len(p.Tiles); n < 119 || n > 120 {
			t.Errorf("limit=%s: %d tiles", limit, n)
		}
	}
	// Malformed cursors and those of the earlier format are refused.
	next := *first.Next
	oldFormat := base64.RawURLEncoding.EncodeToString([]byte("1|1799997500|0|-1001:5"))
	for _, bad := range []string{"limit=0", "limit=-1", "limit=abc", "limit=1.5", "cursor=%21%21", "cursor=AAAA",
		"cursor=" + oldFormat, "cursor=" + next[1:], "cursor=" + next + "A", "cursor=" + strings.Repeat("A", 2000)} {
		resp, p := e.page("/api/tiles?" + bad)
		if resp.StatusCode != 400 || p.Error == "" {
			t.Errorf("%s: %d %+v", bad, resp.StatusCode, p)
		}
	}

	// Page through everything at several sizes: no duplicates, no gaps, snapshot order.
	want := snap.Keys()
	for _, limit := range []int{1, 7, 60, 120} {
		var got []string
		seen := map[string]bool{}
		target := fmt.Sprintf("/api/tiles?limit=%d", limit)
		for pages := 0; ; pages++ {
			_, p := e.page(target)
			for _, tile := range p.Tiles {
				if seen[tile.Key] {
					t.Fatalf("limit %d: duplicate %s", limit, tile.Key)
				}
				seen[tile.Key] = true
				got = append(got, tile.Key)
				if len(tile.Styles) == 0 || tile.Label == "" || tile.Command != "dream" {
					t.Fatalf("tile %+v", tile)
				}
			}
			if p.Next == nil {
				break
			}
			if pages > 300 {
				t.Fatal("paging does not end")
			}
			target = fmt.Sprintf("/api/tiles?limit=%d&cursor=%s", limit, url.QueryEscape(*p.Next))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("limit %d: %d tiles, not the snapshot's %d in order", limit, len(got), len(want))
		}
	}
}

func TestNewTiles(t *testing.T) {
	recs := records(10)
	e := newEnv(t, recs[3:], nil) // the three newest come later
	_, first := e.page("/api/tiles")
	since := url.QueryEscape(*first.Newest)

	_, p := e.page("/api/tiles/new?since=" + since)
	if p.Count == nil || *p.Count != 0 || p.Newest == nil || *p.Newest != *first.Newest || p.Tiles != nil {
		t.Errorf("nothing new: %+v", p)
	}
	_, body := e.get("/api/tiles/new?since="+since, basic)
	if strings.Contains(string(body), "tiles") {
		t.Errorf("tiles sent without tiles=1: %s", body)
	}

	// The bot draws three more (records 0..2; record 0 has two images).
	fresh := newEnv(t, recs, nil)
	_, p = fresh.page("/api/tiles/new?since=" + since)
	if *p.Count != 4 || *p.Newest == *first.Newest {
		t.Errorf("after new records: count %d", *p.Count)
	}
	_, p = fresh.page("/api/tiles/new?tiles=1&since=" + since)
	if *p.Count != 4 || len(p.Tiles) != 4 || p.V != imaging.Tag() || p.Tiles[0].Key != fresh.store.Snapshot().Keys()[0] {
		t.Errorf("tiles=1: %+v", p)
	}
	// No since: everything counts; tiles are capped at 120.
	big := newEnv(t, records(150), nil)
	_, p = big.page("/api/tiles/new?tiles=1")
	if *p.Count != 200 || len(p.Tiles) != 120 {
		t.Errorf("no since: count %d, %d tiles", *p.Count, len(p.Tiles))
	}
	if resp, p := e.page("/api/tiles/new?since=%21"); resp.StatusCode != 400 || p.Error == "" {
		t.Errorf("bad since: %d", resp.StatusCode)
	}
	_, body = e.get("/api/tiles/new?tiles=1&since="+since, basic)
	if !strings.Contains(string(body), `"tiles":[]`) {
		t.Errorf("tiles=1 with nothing new: %s", body)
	}
}

func TestGzipOnlyForJSON(t *testing.T) {
	e := newEnv(t, records(40), nil)
	key := e.store.Snapshot().Keys()[0]
	resp, _ := e.page("/api/tiles", gzipped)
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("JSON was not gzip-encoded on request")
	}
	resp, _ = e.page("/api/tiles")
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("JSON was encoded without being asked")
	}
	resp, _ = e.page("/api/tiles", header("Accept-Encoding", "gzip;q=0, identity"))
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("gzip;q=0 was ignored")
	}
	for _, target := range []string{"/", "/login", "/app.js", "/app.css", "/healthz", "/nope",
		"/img/" + key + ".jpg", "/img/" + key + "-480.webp?v=" + imaging.Tag(), "/img/" + key + "-240.webp?v=" + imaging.Tag()} {
		resp, _ := e.get(target, basic, gzipped)
		if enc := resp.Header.Get("Content-Encoding"); enc != "" {
			t.Errorf("%s: Content-Encoding %q; only JSON may be compressed", target, enc)
		}
	}
}

func webpSize(t *testing.T, data []byte) {
	t.Helper()
	if len(data) < 16 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Fatalf("not a WebP file (%d bytes)", len(data))
	}
}

func TestImages(t *testing.T) {
	e := newEnv(t, records(6), nil)
	snap := e.store.Snapshot()
	tile := snap.Newest(1)[0]
	key, tag := tile.Key, imaging.Tag()
	fid, _ := snap.FileID(key)

	// Thumbnails.
	for _, size := range []string{"240", "480"} {
		target := "/img/" + key + "-" + size + ".webp?v=" + tag
		resp, body := e.get(target, basic)
		etag := `"` + key + "-" + size + "-v" + tag + `"`
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/webp" ||
			resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" || resp.Header.Get("ETag") != etag ||
			resp.Header.Get("Content-Length") != fmt.Sprint(len(body)) || resp.Header.Get("X-Gallery-Placeholder") != "" {
			t.Fatalf("%s: %d %v", target, resp.StatusCode, resp.Header)
		}
		webpSize(t, body)
		if bytes.Equal(body, imaging.Placeholder()) {
			t.Fatalf("%s is the placeholder", target)
		}
		// From the cache the bytes are the same.
		if _, again := e.get(target, basic); !bytes.Equal(again, body) {
			t.Errorf("%s: different bytes the second time", target)
		}
		resp, body = e.get(target, basic, header("If-None-Match", etag))
		if resp.StatusCode != 304 || len(body) != 0 || resp.Header.Get("ETag") != etag {
			t.Errorf("%s with If-None-Match: %d", target, resp.StatusCode)
		}
		if resp, _ := e.get(target, basic, header("If-None-Match", `W/"zzz", `+etag)); resp.StatusCode != 304 {
			t.Errorf("%s with an If-None-Match list: %d", target, resp.StatusCode)
		}
	}
	// The original, byte for byte.
	resp, body := e.get("/img/"+key+".jpg", basic)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(body, sampleJPEG) ||
		resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" || resp.Header.Get("ETag") != `"`+key+`"` {
		t.Fatalf("original: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := e.get("/img/"+key+".jpg", basic, header("If-None-Match", `"`+key+`"`)); resp.StatusCode != 304 {
		t.Errorf("original with If-None-Match: %d", resp.StatusCode)
	}
	// All of that fetched the file from Telegram exactly once, and it is on disk.
	if n := e.fetcher.count(fid); n != 1 {
		t.Errorf("the original was fetched %d times", n)
	}
	if !e.cache.HasOriginal(key) || !e.cache.HasThumb(key, 240, tag) || !e.cache.HasThumb(key, 480, tag) {
		t.Errorf("the cache does not hold the original and both thumbnails")
	}

	// The pipeline version must match; no version is no match.
	for _, v := range []string{"", "?v=", "?v=0", "?v=" + tag + "x", "?V=" + tag} {
		if resp, _ := e.get("/img/"+key+"-240.webp"+v, basic); resp.StatusCode != 404 {
			t.Errorf("thumbnail with %q: %d, want 404", v, resp.StatusCode)
		}
	}
	// Names that are not exactly a key plus a known size.
	for _, name := range []string{
		key + "-120.webp", key + "-480.jpg", key + ".webp", key + "-240.webp.jpg", key + ".jpeg", key, key + "x.jpg",
		key[:21] + ".jpg", "../" + key + ".jpg", key + "-240.WEBP", "%2e%2e%2f" + key + ".jpg",
	} {
		if resp, _ := e.get("/img/"+name+"?v="+tag, basic); resp.StatusCode != 404 {
			t.Errorf("/img/%s: %d, want 404", name, resp.StatusCode)
		}
	}
}

func TestOnlyKeysInSnapshot(t *testing.T) {
	e := newEnv(t, records(3), nil)
	tag := imaging.Tag()
	// A well-formed key for a file that is not in the table, even one whose files are in the cache.
	ghost := source.Key("some-other-file-id")
	if err := e.cache.PutOriginal(ghost, sampleJPEG); err != nil {
		t.Fatal(err)
	}
	if err := e.cache.PutThumb(ghost, 240, tag, imaging.Placeholder()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/img/" + ghost + ".jpg", "/img/" + ghost + "-240.webp?v=" + tag, "/img/" + ghost + "-480.webp?v=" + tag} {
		resp, _ := e.get(target, basic)
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d, want 404", target, resp.StatusCode)
		}
		if resp, _ := e.get(target, basic, header("If-None-Match", "*")); resp.StatusCode != 404 {
			t.Errorf("%s with If-None-Match: %d, want 404", target, resp.StatusCode)
		}
	}
	// A raw file_id is not a key.
	fid := fileID(0, "base")
	if resp, _ := e.get("/img/"+fid+".jpg", basic); resp.StatusCode != 404 {
		t.Errorf("a file_id was accepted as a key")
	}
	if e.fetcher.total() != 0 {
		t.Errorf("a key outside the snapshot reached Telegram")
	}
}

func TestPlaceholderOnFailure(t *testing.T) {
	e := newEnv(t, records(4), nil)
	snap := e.store.Snapshot()
	key, tag := snap.Keys()[0], imaging.Tag()
	fid, _ := snap.FileID(key)
	e.fetcher.setFail(fid, true)

	target := "/img/" + key + "-240.webp?v=" + tag
	resp, body := e.get(target, basic)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/webp" || !bytes.Equal(body, imaging.Placeholder()) ||
		resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Gallery-Placeholder") != "1" || resp.Header.Get("ETag") != "" {
		t.Fatalf("placeholder: %d %v", resp.StatusCode, resp.Header)
	}
	resp, body = e.get("/img/"+key+".jpg", basic)
	if resp.StatusCode != 502 || string(body) != "image unavailable\n" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("ETag") != "" {
		t.Fatalf("original unavailable: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if e.cache.HasThumb(key, 240, tag) || e.cache.HasOriginal(key) {
		t.Fatalf("a failure was stored in the cache")
	}
	// The fetcher's own message goes to the log, never to the client (checked by do) .
	if !strings.Contains(e.logs.String(), "fetching an original failed") {
		t.Errorf("the failure was not logged")
	}
	// When Telegram answers again, the real image is served.
	e.fetcher.setFail(fid, false)
	resp, body = e.get(target, basic)
	if resp.Header.Get("X-Gallery-Placeholder") != "" || bytes.Equal(body, imaging.Placeholder()) || resp.Header.Get("ETag") == "" {
		t.Fatalf("no recovery after the failure: %v", resp.Header)
	}

	// Bytes that are not a JPEG: placeholder, nothing stored, and no second download.
	key2 := snap.Keys()[1]
	fid2, _ := snap.FileID(key2)
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	e.fetcher.mu.Lock()
	e.fetcher.data[fid2] = pngBuf.Bytes()
	e.fetcher.mu.Unlock()
	for range 3 {
		resp, _ := e.get("/img/"+key2+"-480.webp?v="+tag, basic)
		if resp.Header.Get("X-Gallery-Placeholder") != "1" {
			t.Fatalf("a PNG was not refused")
		}
	}
	if resp, _ := e.get("/img/"+key2+".jpg", basic); resp.StatusCode != 502 {
		t.Errorf("a PNG was served as the original: %d", resp.StatusCode)
	}
	if n := e.fetcher.count(fid2); n != 1 {
		t.Errorf("an unusable file was downloaded %d times", n)
	}
	if e.cache.HasOriginal(key2) {
		t.Errorf("an unusable file was stored")
	}
}

// An original whose header is fine but whose body is cut short is found by
// the full decode before anything is stored: no thumbnail of it is cached as
// immutable, the damaged file is not served as the original, and it is not
// downloaded again and again.
func TestTruncatedOriginal(t *testing.T) {
	e := newEnv(t, records(3), nil)
	snap := e.store.Snapshot()
	tag := imaging.Tag()
	for i, percent := range []int{10, 50, 99} {
		key := snap.Keys()[i]
		fid, _ := snap.FileID(key)
		cut := sampleJPEG[:len(sampleJPEG)*percent/100]
		if err := imaging.Validate(cut); err != nil {
			t.Fatalf("%d%%: the header check should pass: %v", percent, err)
		}
		e.fetcher.mu.Lock()
		e.fetcher.data[fid] = cut
		e.fetcher.mu.Unlock()
		for _, size := range []string{"240", "480"} {
			resp, body := e.get("/img/"+key+"-"+size+".webp?v="+tag, basic)
			if resp.Header.Get("X-Gallery-Placeholder") != "1" || resp.Header.Get("Cache-Control") != "no-store" ||
				resp.Header.Get("ETag") != "" || !bytes.Equal(body, imaging.Placeholder()) {
				t.Errorf("%d%% at %s px: a thumbnail of a truncated image was served: %v", percent, size, resp.Header)
			}
		}
		if resp, body := e.get("/img/"+key+".jpg", basic); resp.StatusCode != 502 || len(body) > 100 {
			t.Errorf("%d%%: a truncated original was served: %d, %d bytes", percent, resp.StatusCode, len(body))
		}
		if e.cache.HasOriginal(key) || e.cache.HasThumb(key, 240, tag) || e.cache.HasThumb(key, 480, tag) {
			t.Errorf("%d%%: a truncated image was stored", percent)
		}
		if n := e.fetcher.count(fid); n != 1 {
			t.Errorf("%d%%: downloaded %d times", percent, n)
		}
	}
	if n := strings.Count(e.logs.String(), "an original is not a usable JPEG"); n != 3 {
		t.Errorf("%d log lines about unusable originals, want 3", n)
	}
}

// A failure replayed from the Telegram client's failure cache is logged at
// debug level, and a warm-up that makes nothing is too: a dead file among the
// newest tiles must not write warnings on every refresh.
func TestCachedFailuresAreQuiet(t *testing.T) {
	e := newEnv(t, records(3), nil)
	snap := e.store.Snapshot()
	key := snap.Keys()[0]
	fid, _ := snap.FileID(key)
	target := "/img/" + key + "-240.webp?v=" + imaging.Tag()

	level := func(msg string) (warn, debug int) {
		for line := range strings.SplitSeq(e.logs.String(), "\n") {
			if !strings.Contains(line, msg) {
				continue
			}
			switch {
			case strings.Contains(line, "level=WARN"), strings.Contains(line, "level=INFO"):
				warn++
			case strings.Contains(line, "level=DEBUG"):
				debug++
			}
		}
		return
	}
	// The first failure is news.
	e.fetcher.mu.Lock()
	e.fetcher.errs[fid] = &telegram.Error{Permanent: true, Status: 400}
	e.fetcher.mu.Unlock()
	if resp, _ := e.get(target, basic); resp.Header.Get("X-Gallery-Placeholder") != "1" {
		t.Fatal("no placeholder")
	}
	if warn, _ := level("fetching an original failed"); warn != 1 {
		t.Fatalf("the first failure was logged %d times at WARN, want 1", warn)
	}
	// Replays are not.
	e.fetcher.mu.Lock()
	e.fetcher.errs[fid] = &telegram.Error{Permanent: true, Status: 400, Cached: true}
	e.fetcher.mu.Unlock()
	for range 5 {
		if resp, _ := e.get(target, basic); resp.Header.Get("X-Gallery-Placeholder") != "1" {
			t.Fatal("no placeholder")
		}
	}
	e.srv.Warm(context.Background(), snap) // makes the other tiles
	e.srv.Warm(context.Background(), snap) // makes nothing: only the dead tile is left
	e.srv.Warm(context.Background(), snap)
	warn, debug := level("fetching an original failed")
	if warn != 1 || debug < 5 {
		t.Errorf("cached failures: %d at WARN (want 1, the first), %d at DEBUG", warn, debug)
	}
	if info, debug := level("thumbnails warmed"); info != 1 || debug != 2 {
		t.Errorf("warm-up summaries: %d at INFO (want 1), %d at DEBUG (want 2)", info, debug)
	}
}

// A file that is not a usable JPEG is remembered for invalidTTL, and then
// may be downloaded and judged again.
func TestInvalidOriginalExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(2), nil)
		snap := e.store.Snapshot()
		key := snap.Keys()[0]
		fid, _ := snap.FileID(key)
		var pngBuf bytes.Buffer
		_ = png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 8, 8)))
		e.fetcher.mu.Lock()
		e.fetcher.data[fid] = pngBuf.Bytes()
		e.fetcher.mu.Unlock()
		original := func() int {
			resp, _ := e.get("/img/"+key+".jpg", basic)
			return resp.StatusCode
		}
		if code := original(); code != 502 || e.fetcher.count(fid) != 1 {
			t.Fatalf("a PNG: %d after %d downloads", code, e.fetcher.count(fid))
		}
		// Just before the end of the TTL it is still remembered.
		time.Sleep(invalidTTL - time.Second)
		if code := original(); code != 502 || e.fetcher.count(fid) != 1 {
			t.Fatalf("within the TTL: %d after %d downloads, want 502 after 1", code, e.fetcher.count(fid))
		}
		// After it the file is fetched again; by now Telegram serves a good one.
		time.Sleep(2 * time.Second)
		e.fetcher.mu.Lock()
		delete(e.fetcher.data, fid)
		e.fetcher.mu.Unlock()
		if code := original(); code != 200 || e.fetcher.count(fid) != 2 {
			t.Fatalf("after the TTL: %d after %d downloads, want 200 after 2", code, e.fetcher.count(fid))
		}
		if !e.cache.HasOriginal(key) {
			t.Error("the good original was not stored")
		}
		e.srv.invalid.mu.Lock()
		left := len(e.srv.invalid.until)
		e.srv.invalid.mu.Unlock()
		if left != 0 {
			t.Errorf("%d expired entries were kept", left)
		}
	})
}

func TestConcurrentColdTile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(2), nil)
		snap := e.store.Snapshot()
		key := snap.Keys()[0]
		fid, _ := snap.FileID(key)
		e.fetcher.mu.Lock()
		e.fetcher.delay = 100 * time.Millisecond
		e.fetcher.mu.Unlock()
		var wg sync.WaitGroup
		bodies := make([][]byte, 24)
		for i := range bodies {
			wg.Go(func() {
				size := []string{"240", "480"}[i%2]
				r := httptest.NewRequest("GET", "/img/"+key+"-"+size+".webp?v="+imaging.Tag(), nil)
				basic(r)
				w := httptest.NewRecorder()
				e.h.ServeHTTP(w, r)
				bodies[i] = w.Body.Bytes()
			})
		}
		wg.Wait()
		for i, b := range bodies {
			webpSize(t, b)
			if !bytes.Equal(b, bodies[i%2]) {
				t.Errorf("request %d got different bytes", i)
			}
		}
		if n := e.fetcher.count(fid); n != 1 {
			t.Errorf("24 concurrent requests fetched the original %d times", n)
		}
	})
}

func TestWarmAndPrune(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(30), map[string]string{"GALLERY_WARM": "8"})
		snap := e.store.Snapshot()
		tag := imaging.Tag()
		dead := snap.Newest(8)[2].Key
		fid, _ := snap.FileID(dead)
		e.fetcher.setFail(fid, true)

		e.srv.Warm(context.Background(), snap)
		for i, tile := range snap.Newest(9) {
			for _, size := range imaging.Sizes {
				has := e.cache.HasThumb(tile.Key, size, tag)
				if want := i < 8 && tile.Key != dead; has != want {
					t.Errorf("tile %d size %d: warmed = %v, want %v", i, size, has, want)
				}
			}
		}
		// A second run has nothing left to do except the failed one.
		before := e.fetcher.total()
		e.srv.Warm(context.Background(), snap)
		if got := e.fetcher.total() - before; got > 2 {
			t.Errorf("the second warm-up made %d fetches", got)
		}

		// OnSwap: warms and prunes in the background. Files of keys not in the snapshot go.
		ghost := source.Key("gone-from-the-table")
		if err := e.cache.PutOriginal(ghost, sampleJPEG); err != nil {
			t.Fatal(err)
		}
		if err := e.cache.PutThumb(snap.Keys()[0], 240, "0old", imaging.Placeholder()); err != nil {
			t.Fatal(err)
		}
		e.store.OnSwap(e.srv.OnSwap)
		if err := e.store.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // the warm-up and the pruning started by the swap are done
		if e.cache.HasOriginal(ghost) || e.cache.HasThumb(snap.Keys()[0], 240, "0old") {
			t.Fatal("the cache was not pruned after a swap")
		}
		if !e.cache.HasOriginal(snap.Keys()[0]) {
			t.Errorf("pruning removed a live original")
		}
		// Close waits for the background work; afterwards requests still answer.
		e.srv.Close()
		cold := snap.Keys()[snap.Len()-1]
		resp, _ := e.get("/img/"+cold+"-240.webp?v="+tag, basic)
		if resp.StatusCode != 200 || resp.Header.Get("X-Gallery-Placeholder") != "1" {
			t.Errorf("after Close: %d %v", resp.StatusCode, resp.Header)
		}
	})
}

func TestIndexNoscript(t *testing.T) {
	e := newEnv(t, records(100), nil)
	snap := e.store.Snapshot()
	tag := imaging.Tag()
	resp, body := e.get("/", basic)
	html := string(body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("index: %d %v", resp.StatusCode, resp.Header)
	}
	tiles, next, _ := snap.Page("", DefaultLimit)
	if n := strings.Count(html, `<li class="tile">`); n != len(tiles) {
		t.Errorf("%d server-rendered tiles, want %d", n, len(tiles))
	}
	first := tiles[0]
	for _, want := range []string{
		`src="/img/` + first.Key + `-240.webp?v=` + tag + `"`,
		`/img/` + first.Key + `-480.webp?v=` + tag + ` 480w`,
		`href="/img/` + first.Key + `.jpg"`,
		`href="/?cursor=` + next + `"`,
		`alt="caption 0 &#34;q&#34;"`, // escaped
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	if strings.Contains(html, "<b>") {
		t.Errorf("a prompt reached the page unescaped")
	}
	// "Load more" leads to the following page, and so on to the end, each tile once.
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		target := "/"
		if cursor != "" {
			target = "/?cursor=" + url.QueryEscape(cursor)
		}
		_, body := e.get(target, basic)
		page, n, _ := snap.Page(cursor, DefaultLimit)
		for _, tile := range page {
			if !strings.Contains(string(body), `href="/img/`+tile.Key+`.jpg"`) || seen[tile.Key] {
				t.Fatalf("page %d: tile %s missing or repeated", pages, tile.Key)
			}
			seen[tile.Key] = true
		}
		if n == "" {
			if strings.Contains(string(body), "Load more") {
				t.Errorf("the last page offers more")
			}
			break
		}
		cursor = n
		pages++
	}
	if len(seen) != snap.Len() {
		t.Errorf("the no-script pages show %d of %d tiles", len(seen), snap.Len())
	}
	oldFormat := base64.RawURLEncoding.EncodeToString([]byte("1|1799997500|0|-1001:5")) // held the record id
	for _, bad := range []string{"%21%21", oldFormat, next + "A"} {
		if resp, _ := e.get("/?cursor="+bad, basic); resp.StatusCode != 400 {
			t.Errorf("bad cursor %q on /: %d", bad, resp.StatusCode)
		}
	}
}

func TestRedactingHandler(t *testing.T) {
	var buf bytes.Buffer
	const secret, other = "s3cret-token-value/+x", "123456:BOT-token"
	for name, inner := range map[string]slog.Handler{
		"text": slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		"json": slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	} {
		buf.Reset()
		log := slog.New(RedactingHandler(inner, secret, "", other))
		u := "https://api.example/bot" + other + "/getFile?x=" + url.QueryEscape(secret)
		log.Info("message with "+secret, "plain", secret, "err", &url.Error{Op: "Post", URL: u, Err: errors.New("boom " + other)},
			slog.Group("g", "inner", secret, slog.Group("deep", "x", other)), "any", struct{ A string }{secret}, "n", 5, secret, "key")
		log.With("with", secret).WithGroup("grp").Warn("again", "url", u)
		slog.NewLogLogger(log.Handler(), slog.LevelWarn).Print("http: error " + u)
		out := buf.String()
		for _, s := range []string{secret, other, url.QueryEscape(secret)} {
			if strings.Contains(out, s) {
				t.Errorf("%s handler: the log contains %q:\n%s", name, s, out)
			}
		}
		if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "boom") || !strings.Contains(out, "5") {
			t.Errorf("%s handler: unexpected output:\n%s", name, out)
		}
	}
}

func TestAccessLog(t *testing.T) {
	e := newEnv(t, records(2), nil)
	e.get("/api/tiles?cursor=&secretquery=1", basic, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: "cookie-value-123"})
		r.Header.Set("X-Other", "header-value-456")
	})
	e.get("/"+testToken+"/x", basic)
	logs := e.logs.String()
	if !strings.Contains(logs, "path=/api/tiles") || !strings.Contains(logs, "status=200") {
		t.Errorf("no access log line:\n%s", logs)
	}
	for _, s := range []string{"cookie-value-123", "header-value-456", "secretquery", testToken, "Basic", fileIDMark} {
		if strings.Contains(logs, s) {
			t.Errorf("the log contains %q:\n%s", s, logs)
		}
	}
}

func TestNewRejectsBadDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Error("New accepted empty Deps")
	}
}

// ?style= narrows the listing, the "N new" count and the page without JavaScript to one style.
func TestStyleFilter(t *testing.T) {
	e := newEnv(t, records(150), nil) // 150 records; every third has a Monet too
	snap := e.store.Snapshot()
	var monet []string
	for _, tl := range snap.Newest(1000) {
		if tl.Style == "monet" {
			monet = append(monet, tl.Key)
		}
	}
	if len(monet) != 50 {
		t.Fatalf("%d monet tiles", len(monet))
	}

	for _, limit := range []int{1, 7, 60, 120} {
		var got []string
		target := fmt.Sprintf("/api/tiles?style=monet&limit=%d", limit)
		for range 300 {
			_, p := e.page(target)
			for _, tile := range p.Tiles {
				if tile.Style != "monet" {
					t.Fatalf("a %s tile in the monet listing", tile.Style)
				}
				got = append(got, tile.Key)
			}
			if p.Newest == nil || *p.Newest != snap.Style("monet").Newest(1)[0].Cursor() {
				t.Fatalf("newest %v", p.Newest)
			}
			if p.Next == nil {
				break
			}
			target = fmt.Sprintf("/api/tiles?style=monet&limit=%d&cursor=%s", limit, url.QueryEscape(*p.Next))
		}
		if strings.Join(got, ",") != strings.Join(monet, ",") {
			t.Errorf("limit %d: %d tiles, want the %d monet tiles in order", limit, len(got), len(monet))
		}
	}

	// "N new" counts the style's tiles only.
	_, all := e.page("/api/tiles?style=monet&limit=1")
	_, p := e.page("/api/tiles/new?style=monet&since=" + url.QueryEscape(*all.Newest))
	if *p.Count != 0 {
		t.Errorf("nothing new: %d", *p.Count)
	}
	_, p = e.page("/api/tiles/new?style=monet")
	if *p.Count != len(monet) {
		t.Errorf("count of all: %d, want %d", *p.Count, len(monet))
	}
	_, p = e.page("/api/tiles/new?style=monet&tiles=1")
	if len(p.Tiles) != len(monet) || p.Tiles[0].Style != "monet" {
		t.Errorf("tiles=1: %d tiles", len(p.Tiles))
	}

	// A style that nobody has is an empty list; a malformed one is refused, on every route that takes one.
	_, p = e.page("/api/tiles?style=nosuchstyle")
	if len(p.Tiles) != 0 || p.Next != nil {
		t.Errorf("a style with no images: %d tiles, next %v", len(p.Tiles), p.Next)
	}
	for _, bad := range []string{"Monet", "a%20b", "mo%2Fnet", "%3Cb%3E", strings.Repeat("a", 33), "a%3Bb"} {
		for _, route := range []string{"/api/tiles?style=", "/api/tiles/new?style=", "/?style="} {
			if resp, _ := e.get(route+bad, basic); resp.StatusCode != 400 {
				t.Errorf("%s%s: %d", route, bad, resp.StatusCode)
			}
		}
	}

	// The page without JavaScript: the first page of that style, and "Load more" keeps the style.
	resp, body := e.get("/?style=monet", basic)
	html := string(body)
	if resp.StatusCode != 200 || !strings.Contains(html, "<title>Monet · Gallery</title>") || !strings.Contains(html, `<h1 class="sr">Monet · Gallery</h1>`) {
		t.Fatalf("narrowed page: %d\n%.400s", resp.StatusCode, html)
	}
	for _, key := range monet[:3] {
		if !strings.Contains(html, "/img/"+key+".jpg") {
			t.Errorf("the monet page lacks %s", key)
		}
	}
	for _, tl := range snap.Newest(1000) {
		if tl.Style != "monet" && strings.Contains(html, "/img/"+tl.Key+".jpg") {
			t.Fatalf("a %s tile on the monet page", tl.Style)
		}
	}
	// 50 tiles fit one page, so there is no more; with the whole listing there is, and it carries no style.
	if strings.Contains(html, "Load more") {
		t.Error("Load more on a one-page listing")
	}
	_, body = e.get("/", basic)
	if !strings.Contains(string(body), "<title>Gallery</title>") || strings.Contains(string(body), "style=") {
		t.Errorf("the whole listing mentions a style")
	}
}
