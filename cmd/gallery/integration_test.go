package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/image/webp"

	"github.com/robdevops/dalibot-gallery/internal/devstack"
	"github.com/robdevops/dalibot-gallery/internal/source"
)

// lockedBuffer collects the gallery's log.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// stack is the real binary running against moto and the fake Telegram.
type stack struct {
	t       *testing.T
	base    string
	env     []string
	bin     string
	cmd     *exec.Cmd
	logs    *lockedBuffer
	secrets []string
	client  *http.Client
}

func (s *stack) start() {
	s.t.Helper()
	s.cmd = exec.Command(s.bin)
	s.cmd.Env = s.env
	s.cmd.Stdout, s.cmd.Stderr = s.logs, s.logs
	if err := s.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the gallery did not become healthy:\n%s", s.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop sends SIGTERM and expects a clean exit.
func (s *stack) stop() {
	s.t.Helper()
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			s.t.Errorf("the gallery did not exit cleanly on SIGTERM: %v", err)
		}
	case <-time.After(20 * time.Second):
		_ = s.cmd.Process.Kill()
		s.t.Errorf("the gallery did not stop within 20 s of SIGTERM")
	}
	s.cmd = nil
}

// get sends a GET and checks what every response must satisfy.
func (s *stack) get(target string, auth bool, hdr ...string) (*http.Response, []byte) {
	s.t.Helper()
	req, err := http.NewRequest("GET", s.base+target, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	if auth {
		req.SetBasicAuth("it", devstack.GalleryToken)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return s.send(req)
}

func (s *stack) send(req *http.Request) (*http.Response, []byte) {
	s.t.Helper()
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	body := raw
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			s.t.Fatalf("%s: bad gzip: %v", req.URL.Path, err)
		}
		if body, err = io.ReadAll(zr); err != nil {
			s.t.Fatalf("%s: bad gzip: %v", req.URL.Path, err)
		}
	}
	s.scan(req.Method+" "+req.URL.Path, resp, body)
	return resp, body
}

// scan fails the test if a response carries a file_id, a token, a Telegram
// URL or a file_path, or lacks a security header.
func (s *stack) scan(what string, resp *http.Response, body []byte) {
	s.t.Helper()
	var hdr bytes.Buffer
	_ = resp.Header.Write(&hdr)
	for _, secret := range s.secrets {
		if bytes.Contains(body, []byte(secret)) {
			s.t.Errorf("%s: the body contains %q", what, secret)
		}
		if strings.Contains(hdr.String(), secret) {
			s.t.Errorf("%s: a header contains %q", what, secret)
		}
	}
	const csp = "default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	if resp.Header.Get("Content-Security-Policy") != csp || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Permissions-Policy") == "" {
		s.t.Errorf("%s: security headers missing or wrong: %v", what, resp.Header)
	}
}

type tilesPage struct {
	Tiles  []source.Tile `json:"tiles"`
	Next   *string       `json:"next"`
	Newest *string       `json:"newest"`
	V      string        `json:"v"`
	Count  int           `json:"count"`
}

func (s *stack) json(target string) tilesPage {
	s.t.Helper()
	resp, body := s.get(target, true)
	if resp.StatusCode != 200 {
		s.t.Fatalf("%s: %d %s", target, resp.StatusCode, body)
	}
	var p tilesPage
	if err := json.Unmarshal(body, &p); err != nil {
		s.t.Fatalf("%s: %v", target, err)
	}
	return p
}

// all pages through the whole listing.
func (s *stack) all(limit int) (tiles []source.Tile, first tilesPage) {
	s.t.Helper()
	target := fmt.Sprintf("/api/tiles?limit=%d", limit)
	for i := 0; ; i++ {
		p := s.json(target)
		if i == 0 {
			first = p
		}
		tiles = append(tiles, p.Tiles...)
		if p.Next == nil {
			return tiles, first
		}
		if i > 1000 {
			s.t.Fatal("paging does not end")
		}
		target = fmt.Sprintf("/api/tiles?limit=%d&cursor=%s", limit, url.QueryEscape(*p.Next))
	}
}

// TestIntegration builds the real binary and runs it against a moto DynamoDB
// server seeded with about 150 items in the bot's shapes and a fake Telegram
// API that records any request other than getFile and the file download.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	moto, err := devstack.StartMoto(ctx)
	if errors.Is(err, devstack.ErrNoMoto) {
		t.Skipf("skipping the integration test: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(moto.Stop)

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	images, err := devstack.LoadImages(filepath.Join(root, "testdata", "telegram"))
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := devstack.Seed(ctx, moto.URL, devstack.Table, time.Now(), devstack.StoreDays)
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Items < 150 {
		t.Fatalf("seeded only %d items", seeded.Items)
	}

	tg := devstack.NewTelegram(devstack.BotToken, images)
	tgServer := httptest.NewServer(tg)
	t.Cleanup(tgServer.Close)
	// Whatever else happens, the gallery may only ever have asked Telegram for getFile and file downloads.
	t.Cleanup(func() {
		if bad := tg.Forbidden(); len(bad) > 0 {
			t.Errorf("the gallery made %d forbidden Telegram requests: %v", len(bad), bad)
		}
	})

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gallery")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the gallery: %v\n%s", err, out)
	}

	// A missing configuration is reported and exits with status 2, before anything else.
	bare := exec.Command(bin)
	bare.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := bare.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "GALLERY_TOKEN") {
		t.Fatalf("no configuration: %v\n%s", err, out)
	}

	port, err := devstack.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	tgURL, _ := url.Parse(tgServer.URL)
	jar, _ := cookiejar.New(nil)
	s := &stack{
		t: t, base: "http://" + listen, bin: bin, logs: &lockedBuffer{},
		env: append(append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp}, devstack.AWSEnv(moto.URL)...),
			"GALLERY_LISTEN="+listen,
			"GALLERY_TOKEN="+devstack.GalleryToken,
			"DALIBOT_TABLE="+devstack.Table,
			"TELEGRAM_BOT_TOKEN="+devstack.BotToken,
			"TELEGRAM_API_BASE="+tgServer.URL,
			"GALLERY_REFRESH=5s",
			"GALLERY_WARM=120",
			"GALLERY_CACHE_DIR="+filepath.Join(tmp, "cache"),
			"GALLERY_DEBUG=1",
		),
		secrets: []string{devstack.FileIDPrefix, devstack.BotToken, devstack.GalleryToken, tgServer.URL, tgURL.Host,
			"api.telegram.org", "photos/", "file_path", "file_id", "/bot", "testing"},
		client: &http.Client{
			Timeout:       60 * time.Second,
			Jar:           jar,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 16},
		},
	}
	s.start()
	t.Cleanup(func() {
		if s.cmd != nil {
			_ = s.cmd.Process.Kill()
			_, _ = s.cmd.Process.Wait()
		}
		if t.Failed() {
			t.Logf("gallery log (last 4000 bytes):\n%s", tail(s.logs.String(), 4000))
		}
	})

	// --- health, auth --------------------------------------------------------
	if resp, body := s.get("/healthz", false); resp.StatusCode != 200 || string(body) != "ok\n" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, body)
	}
	hc := exec.Command(bin, "-healthcheck")
	hc.Env = s.env
	if out, err := hc.CombinedOutput(); err != nil {
		t.Errorf("gallery -healthcheck: %v %s", err, out)
	}
	if resp, _ := s.get("/", false); resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Errorf("GET / without auth: %d", resp.StatusCode)
	}
	if resp, _ := s.get("/api/tiles", false); resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("API without auth: %d", resp.StatusCode)
	}

	if resp, _ := s.get("/app.js", false); resp.StatusCode != 401 {
		t.Errorf("/app.js without auth: %d", resp.StatusCode)
	}
	if resp, _ := s.get("/app.css", false); resp.StatusCode != 200 {
		t.Errorf("/app.css without auth: %d", resp.StatusCode)
	}
	// Wrong Basic passwords are limited per client, on the real binary too. The
	// client address is a made-up one (the test's loopback peer is a trusted
	// proxy by default), so the rest of the test is not blocked.
	var codes []int
	for i := 0; i < 7; i++ {
		req, _ := http.NewRequest("GET", s.base+"/api/tiles", nil)
		req.SetBasicAuth("it", fmt.Sprintf("wrong-password-%d", i))
		req.Header.Set("X-Forwarded-For", "203.0.113.200")
		resp, _ := s.send(req)
		codes = append(codes, resp.StatusCode)
	}
	if fmt.Sprint(codes) != "[401 401 401 401 401 429 429]" {
		t.Errorf("wrong Basic passwords from one client: %v", codes)
	}
	if resp, _ := s.get("/api/tiles", true, "X-Forwarded-For", "203.0.113.200"); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("the right password from a blocked client: %d", resp.StatusCode)
	}
	if resp, _ := s.get("/api/tiles", true); resp.StatusCode != 200 {
		t.Errorf("another client while one is blocked: %d", resp.StatusCode)
	}

	// --- the listing: page through everything --------------------------------
	tiles, first := s.all(60)
	if len(tiles) != seeded.Tiles {
		t.Fatalf("the API lists %d tiles, the seed should give %d", len(tiles), seeded.Tiles)
	}
	seen := map[string]bool{}
	recs := map[string]bool{}
	for i, tile := range tiles {
		if seen[tile.Key] {
			t.Fatalf("duplicate tile %s", tile.Key)
		}
		seen[tile.Key] = true
		recs[tile.ID] = true
		if !source.ValidKey(tile.Key) || len(tile.Styles) == 0 || tile.Label == "" {
			t.Fatalf("tile %d: %+v", i, tile)
		}
		if i > 0 && tile.Updated > tiles[i-1].Updated {
			t.Fatalf("tile %d is newer than the one before it", i)
		}
		chat := tile.ID[:strings.LastIndex(tile.ID, ":")]
		if chat != devstack.Chats[0] && chat != devstack.Chats[1] && chat != devstack.Chats[2] {
			t.Fatalf("tile %d is from chat %q", i, chat)
		}
	}
	if len(recs) != seeded.Records {
		t.Errorf("%d records listed, want %d", len(recs), seeded.Records)
	}
	// Other page sizes give the same sequence: no gaps, no duplicates.
	for _, limit := range []int{7, 120} {
		other, _ := s.all(limit)
		if len(other) != len(tiles) {
			t.Fatalf("limit %d: %d tiles", limit, len(other))
		}
		for i := range other {
			if other[i].Key != tiles[i].Key {
				t.Fatalf("limit %d: tile %d differs", limit, i)
			}
		}
	}
	// Items that must not be listed are not, and their keys are not served.
	for _, fid := range seeded.Hidden {
		key := source.Key(fid)
		if seen[key] {
			t.Errorf("a hidden item is listed")
		}
		for _, target := range []string{"/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + first.V} {
			if resp, _ := s.get(target, true); resp.StatusCode != 404 {
				t.Errorf("a key outside the snapshot was served: %d", resp.StatusCode)
			}
		}
	}
	// The awkward shapes came through.
	byID := map[string][]source.Tile{}
	for _, tile := range tiles {
		byID[tile.ID] = append(byID[tile.ID], tile)
	}
	check := func(id string, n int, current string) {
		t.Helper()
		got := byID[id]
		if len(got) != n || (n > 0 && (got[0].Current != current || got[0].Style != current)) {
			t.Errorf("record %s: %d tiles %+v, want %d with current %q", id, len(got), got, n, current)
		}
	}
	check(devstack.Chats[0]+":5001", 0, "")          // empty images
	check(devstack.Chats[0]+":5002", 2, "_")         // current names a missing style
	check(devstack.Chats[1]+":5003", 2, "sketch")    // no current, no "_"
	check(devstack.Chats[2]+":5004", 2, "neon_noir") // unknown style
	check(devstack.Chats[2]+":5005", 3, "tattoo")    // carry_over copy keeps the images
	check(devstack.Chats[2]+":1005", 0, "")          // ... and the older record loses them
	check(devstack.Chats[1]+":5008", 1, "_")         // no prompt, no command
	if got := byID[devstack.Chats[2]+":5004"]; len(got) == 2 && got[0].Label != "Neon Noir" {
		t.Errorf("unknown style label %q", got[0].Label)
	}
	if got := byID[devstack.Chats[1]+":5008"]; len(got) == 1 && got[0].Command != "dream" {
		t.Errorf("default command %q", got[0].Command)
	}

	// --- gzip only for JSON --------------------------------------------------
	if resp, _ := s.get("/api/tiles", true, "Accept-Encoding", "gzip"); resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("JSON is not gzip-encoded")
	}
	for _, target := range []string{"/", "/app.js", "/app.css", "/img/" + tiles[0].Key + "-240.webp?v=" + first.V, "/img/" + tiles[0].Key + ".jpg"} {
		if resp, _ := s.get(target, true, "Accept-Encoding", "gzip"); resp.Header.Get("Content-Encoding") != "" || resp.StatusCode != 200 {
			t.Errorf("%s: status %d, Content-Encoding %q", target, resp.StatusCode, resp.Header.Get("Content-Encoding"))
		}
	}

	// --- images --------------------------------------------------------------
	deadKey := source.Key(seeded.Dead[0])
	const immutable = "private, max-age=31536000, immutable"
	for i, tile := range tiles {
		sizes := []int{240}
		if i < 60 {
			sizes = []int{240, 480}
		}
		for _, size := range sizes {
			target := fmt.Sprintf("/img/%s-%d.webp?v=%s", tile.Key, size, first.V)
			resp, body := s.get(target, true)
			if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/webp" {
				t.Fatalf("%s: %d %s", target, resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			cfg, err := webp.DecodeConfig(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("%s: not a WebP image: %v", target, err)
			}
			if tile.Key == deadKey {
				if resp.Header.Get("X-Gallery-Placeholder") != "1" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("ETag") != "" {
					t.Errorf("the dead file's tile is not an uncached placeholder: %v", resp.Header)
				}
				continue
			}
			if cfg.Width != size || cfg.Height != size {
				t.Fatalf("%s: %dx%d", target, cfg.Width, cfg.Height)
			}
			if resp.Header.Get("Cache-Control") != immutable || resp.Header.Get("ETag") == "" || resp.Header.Get("X-Gallery-Placeholder") != "" {
				t.Fatalf("%s: headers %v", target, resp.Header)
			}
			if size == 480 && len(body) > 68<<10 {
				t.Errorf("%s: %d bytes", target, len(body))
			}
			if i == 0 {
				if resp, _ := s.get(target, true, "If-None-Match", resp.Header.Get("ETag")); resp.StatusCode != 304 {
					t.Errorf("%s with If-None-Match: %d", target, resp.StatusCode)
				}
			}
		}
	}
	// Originals: valid JPEGs, byte for byte one of the files Telegram serves.
	for _, tile := range []source.Tile{tiles[0], tiles[1], tiles[len(tiles)/2], tiles[len(tiles)-2]} {
		resp, body := s.get("/img/"+tile.Key+".jpg", true)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || resp.Header.Get("Cache-Control") != immutable {
			t.Fatalf("original %s: %d %v", tile.Key, resp.StatusCode, resp.Header)
		}
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 1024 || cfg.Height != 1024 {
			t.Errorf("original %s: not the 1024x1024 JPEG: %v", tile.Key, err)
		}
		match := false
		for _, img := range images {
			match = match || bytes.Equal(img, body)
		}
		if !match {
			t.Errorf("original %s is not byte for byte a file Telegram served", tile.Key)
		}
	}
	if resp, _ := s.get("/img/"+deadKey+".jpg", true); resp.StatusCode != 502 {
		t.Errorf("dead original: %d", resp.StatusCode)
	}
	if resp, _ := s.get("/img/"+tiles[0].Key+"-240.webp?v=0", true); resp.StatusCode != 404 {
		t.Errorf("old pipeline version: %d", resp.StatusCode)
	}
	// Every live file was fetched from Telegram exactly once.
	getFile, downloads := tg.Calls()
	if downloads != seeded.Tiles-1 || getFile != seeded.Tiles {
		t.Errorf("Telegram saw %d getFile and %d downloads, want %d and %d", getFile, downloads, seeded.Tiles, seeded.Tiles-1)
	}

	// --- warm-cache tile latency ---------------------------------------------
	var lat []time.Duration
	for round := 0; round < 5; round++ {
		for _, tile := range tiles[:60] {
			target := fmt.Sprintf("/img/%s-240.webp?v=%s", tile.Key, first.V)
			start := time.Now()
			resp, _ := s.get(target, true)
			lat = append(lat, time.Since(start))
			if resp.StatusCode != 200 {
				t.Fatalf("%s: %d", target, resp.StatusCode)
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	median, p95 := lat[len(lat)/2], lat[len(lat)*95/100]
	t.Logf("warm-cache 240px tile over loopback HTTP, %d requests: median %v, p95 %v, max %v", len(lat), median, p95, lat[len(lat)-1])
	if median > 5*time.Millisecond {
		t.Errorf("a warm tile takes %v (median); the target is under 5 ms", median)
	}

	// --- the no-script page and the login form -------------------------------
	resp, body := s.get("/", true)
	if resp.StatusCode != 200 || strings.Count(string(body), `<li class="tile">`) < 55 || !strings.Contains(string(body), `<a href="/?cursor=`) {
		t.Errorf("index: %d, %d server-rendered tiles", resp.StatusCode, strings.Count(string(body), `<li class="tile">`))
	}
	if strings.Contains(string(body), "<script>alert") || strings.Contains(string(body), "<img src=x") {
		t.Errorf("seeded markup reached the page unescaped")
	}
	post := func(token string) *http.Response {
		req, _ := http.NewRequest("POST", s.base+"/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, _ := s.send(req)
		return resp
	}
	if resp := post("wrong-token-wrong-token"); resp.StatusCode != 401 || len(resp.Cookies()) != 0 {
		t.Errorf("wrong login: %d", resp.StatusCode)
	}
	resp = post(devstack.GalleryToken)
	if resp.StatusCode != 303 || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	c := resp.Cookies()[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Value == devstack.GalleryToken {
		t.Errorf("session cookie attributes: %+v", c)
	}
	req, _ := http.NewRequest("GET", s.base+"/api/tiles?limit=1", nil)
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	if resp, _ := s.send(req); resp.StatusCode != 200 {
		t.Errorf("API with the session cookie: %d", resp.StatusCode)
	}

	// --- new images appear after a refresh -----------------------------------
	added, err := devstack.AddNew(ctx, moto.URL, devstack.Table, time.Now(), devstack.StoreDays, 3)
	if err != nil {
		t.Fatal(err)
	}
	since := url.QueryEscape(*first.Newest)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if p := s.json("/api/tiles/new?since=" + since); p.Count == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new records did not show up after a refresh")
		}
		time.Sleep(250 * time.Millisecond)
	}
	p := s.json("/api/tiles/new?tiles=1&since=" + since)
	if len(p.Tiles) != 3 || p.Tiles[0].Key != source.Key(added[2]) {
		t.Errorf("new tiles: %d", len(p.Tiles))
	}
	if after, _ := s.all(60); len(after) != seeded.Tiles+3 {
		t.Errorf("%d tiles after adding 3", len(after))
	}

	// --- restart: everything comes from the disk cache -----------------------
	s.stop()
	_, before := tg.Calls()
	s.start()
	for _, tile := range tiles[:100] {
		if tile.Key == deadKey {
			continue
		}
		if resp, _ := s.get(fmt.Sprintf("/img/%s-240.webp?v=%s", tile.Key, first.V), true); resp.StatusCode != 200 || resp.Header.Get("ETag") == "" {
			t.Fatalf("after restart: %d", resp.StatusCode)
		}
		if resp, _ := s.get("/img/"+tile.Key+".jpg", true); resp.StatusCode != 200 {
			t.Fatalf("original after restart: %d", resp.StatusCode)
		}
	}
	// Only the three new records may have been fetched (by the warmer).
	if _, after := tg.Calls(); after-before > 3 {
		t.Errorf("%d downloads after a restart; originals must come from the disk cache", after-before)
	}
	s.stop()

	// --- the log -------------------------------------------------------------
	logs := s.logs.String()
	for _, secret := range []string{devstack.FileIDPrefix, devstack.BotToken, devstack.GalleryToken, c.Value, "photos/file_"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the gallery's log contains %q", secret)
		}
	}
	for _, want := range []string{"vips_version=", "snapshot refreshed", "thumbnails warmed", "listening", "stopped"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the gallery's log lacks %q", want)
		}
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
