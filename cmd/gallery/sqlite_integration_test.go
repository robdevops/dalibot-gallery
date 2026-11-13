package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/webp"

	"github.com/robdevops/dalibot-gallery/internal/devstack"
	"github.com/robdevops/dalibot-gallery/internal/source"
)

// dirState describes a directory exactly: every name, size and content hash.
func dirState(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %d %v %d %x\n", e.Name(), len(raw), info.Mode(), info.ModTime().UnixNano(), sha256.Sum256(raw))
	}
	return b.String()
}

// TestIntegrationSQLite builds the real binary and runs it with
// GALLERY_SOURCE=sqlite against a database file holding the same records as
// the DynamoDB test, written the way the bot writes them, and the fake
// Telegram API. No AWS setting is given, and the ones that are present are
// wrong on purpose: nothing may read them.
func TestIntegrationSQLite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	images, err := devstack.LoadImages(filepath.Join(root, "testdata", "telegram"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	botDir := filepath.Join(tmp, "bot", "var")
	if err := os.MkdirAll(botDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(botDir, "dalibot.db")
	db, seeded, err := devstack.SeedSQLite(ctx, dbPath, time.Now(), devstack.StoreDays)
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Items < 150 {
		t.Fatalf("seeded only %d rows", seeded.Items)
	}
	// The seeder never opens a file that is already there.
	if _, _, err := devstack.SeedSQLite(ctx, dbPath, time.Now(), devstack.StoreDays); err == nil {
		t.Fatal("the seeder wrote to an existing database")
	}

	tg := devstack.NewTelegram(devstack.BotToken, images)
	tgServer := httptest.NewServer(tg)
	t.Cleanup(tgServer.Close)
	t.Cleanup(func() {
		if bad := tg.Forbidden(); len(bad) > 0 {
			t.Errorf("the gallery made %d forbidden Telegram requests: %v", len(bad), bad)
		}
	})

	bin := gallery(t)

	port, err := devstack.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	tgURL, _ := url.Parse(tgServer.URL)
	jar, _ := cookiejar.New(nil)
	s := &stack{
		t: t, base: "http://" + listen, bin: bin, logs: &lockedBuffer{},
		env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp,
			"GALLERY_SOURCE=sqlite",
			"GALLERY_SQLITE_PATH=" + dbPath,
			"GALLERY_LISTEN=" + listen,
			"GALLERY_TOKEN=" + devstack.GalleryToken,
			"TELEGRAM_BOT_TOKEN=" + devstack.BotToken,
			"TELEGRAM_API_BASE=" + tgServer.URL,
			"GALLERY_REFRESH=5s",
			"GALLERY_WARM=40",
			"GALLERY_CACHE_DIR=" + filepath.Join(tmp, "cache"),
			// For the hiding at the end: an admin, and a state directory of its own.
			"GALLERY_ADMIN_TOKEN=" + devstack.AdminToken,
			"GALLERY_STATE_DIR=" + filepath.Join(tmp, "state"),
			// Settings of the other source. With DynamoDB the first two would stop the gallery at
			// start-up and the rest would make every scan fail; with SQLite they are not read.
			"DALIBOT_TABLE=not a table!",
			"AWS_REGION=Not A Region",
			"AWS_ENDPOINT_URL_DYNAMODB=http://127.0.0.1:1",
			"AWS_CONFIG_FILE=" + filepath.Join(tmp, "no-such-aws-config"),
			"AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(tmp, "no-such-aws-credentials"),
			"AWS_EC2_METADATA_DISABLED=true",
		},
		secrets: []string{devstack.FileIDPrefix, devstack.BotToken, devstack.GalleryToken, tgServer.URL, tgURL.Host,
			"api.telegram.org", "photos/", "file_path", "file_id", "/bot", dbPath, "dalibot.db"},
		client: &http.Client{
			Timeout:       60 * time.Second,
			Jar:           jar,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 16},
		},
	}
	withEnv := func(change ...string) []string {
		return append(append([]string(nil), s.env...), change...) // a later value wins
	}

	// --- the source settings at start-up -------------------------------------
	for name, c := range map[string]struct {
		env  []string
		want []string
	}{
		"sqlite without a path": {withEnv("GALLERY_SQLITE_PATH="), []string{"GALLERY_SQLITE_PATH: is required when GALLERY_SOURCE=sqlite"}},
		"an unknown source":     {withEnv("GALLERY_SOURCE=mysql"), []string{"GALLERY_SOURCE: must be dynamodb or sqlite"}},
		"a path but no source": {withEnv("GALLERY_SOURCE=", "DALIBOT_TABLE=dalibot", "AWS_REGION=us-east-2"),
			[]string{"GALLERY_SQLITE_PATH: is set, but GALLERY_SOURCE is not sqlite"}},
		// The other source does read its settings.
		"dynamodb with these AWS settings": {withEnv("GALLERY_SOURCE=dynamodb", "GALLERY_SQLITE_PATH="), []string{"DALIBOT_TABLE:", "AWS_REGION:"}},
	} {
		cmd := exec.Command(bin)
		cmd.Env = c.env
		out, err := cmd.CombinedOutput()
		if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 2 {
			t.Errorf("%s: the gallery did not refuse to start with status 2: %v\n%s", name, err, out)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s: the message lacks %q:\n%s", name, want, out)
			}
		}
	}

	untouched := dirState(t, botDir)
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

	// --- the listing ---------------------------------------------------------
	tiles, first := s.all(60)
	if len(tiles) != seeded.Tiles {
		t.Fatalf("the API lists %d tiles, the seed should give %d", len(tiles), seeded.Tiles)
	}
	seen := map[string]bool{}
	byID := map[string][]source.Tile{}
	for i, tile := range tiles {
		if seen[tile.Key] {
			t.Fatalf("duplicate tile %s", tile.Key)
		}
		seen[tile.Key] = true
		byID[tile.ID] = append(byID[tile.ID], tile)
		if i > 0 && tile.Updated > tiles[i-1].Updated {
			t.Fatalf("tile %d is newer than the one before it", i)
		}
		chat, _, _ := strings.CutLast(tile.ID, ":")
		if chat != devstack.Chats[0] && chat != devstack.Chats[1] && chat != devstack.Chats[2] {
			t.Fatalf("tile %d is from chat %q", i, chat)
		}
	}
	if len(byID) != seeded.Records {
		t.Errorf("%d records listed, want %d", len(byID), seeded.Records)
	}
	// The newest tile is dated from its row's updated column: about 50 s before the seed.
	if age := time.Now().Unix() - tiles[0].Updated; age < 40 || age > 600 {
		t.Errorf("the newest tile is dated %d s ago", age)
	}
	// Rows that must not be listed (expired, no date, no message id, the probe) are not, and their keys are not served.
	for _, fid := range seeded.Hidden {
		key := source.Key(fid)
		if seen[key] {
			t.Errorf("a hidden row is listed")
		}
		for _, target := range []string{"/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + first.V} {
			if resp, _ := s.get(target, true); resp.StatusCode != 404 {
				t.Errorf("a key outside the snapshot was served: %d", resp.StatusCode)
			}
		}
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

	// --- images come from Telegram as with DynamoDB --------------------------
	deadKey := source.Key(seeded.Dead[0])
	for _, tile := range append(append([]source.Tile(nil), tiles[:30]...), tiles[len(tiles)-1]) {
		target := fmt.Sprintf("/img/%s-240.webp?v=%s", tile.Key, first.V)
		resp, body := s.get(target, true)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/webp" {
			t.Fatalf("%s: %d", target, resp.StatusCode)
		}
		if cfg, err := webp.DecodeConfig(bytes.NewReader(body)); err != nil || (cfg.Width != 240 && tile.Key != deadKey) {
			t.Fatalf("%s: not a 240px WebP image: %v", target, err)
		}
		if (resp.Header.Get("X-Gallery-Placeholder") == "1") != (tile.Key == deadKey) {
			t.Errorf("%s: placeholder header %q", target, resp.Header.Get("X-Gallery-Placeholder"))
		}
	}
	resp, body := s.get("/img/"+tiles[0].Key+".jpg", true)
	match := false
	for _, img := range images {
		match = match || bytes.Equal(img, body)
	}
	if resp.StatusCode != 200 || !match {
		t.Errorf("original: %d, byte for byte a file Telegram served: %v", resp.StatusCode, match)
	}
	if resp, body := s.get("/", true); resp.StatusCode != 200 || strings.Count(string(body), `<li class="tile">`) < 55 {
		t.Errorf("index: %d", resp.StatusCode)
	}

	// --- the bot's file is only read -----------------------------------------
	// Several refreshes and every request above later, the directory holds the
	// same one file with the same bytes and the same modification time.
	time.Sleep(6 * time.Second)
	if now := dirState(t, botDir); now != untouched {
		t.Errorf("the gallery changed the bot's directory:\nbefore %s\nafter  %s", untouched, now)
	}

	// --- new rows appear after a refresh -------------------------------------
	since := url.QueryEscape(*first.Newest)
	waitCount := func(want int, what string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if p := s.json("/api/tiles/new?since=" + since); p.Count == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the listing did not reach %d new tiles", what, want)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	added, err := db.AddNew(ctx, time.Now(), 3)
	if err != nil {
		t.Fatal(err)
	}
	waitCount(3, "three new rows")
	if p := s.json("/api/tiles/new?tiles=1&since=" + since); len(p.Tiles) != 3 || p.Tiles[0].Key != source.Key(added[2]) {
		t.Errorf("new tiles: %d", len(p.Tiles))
	}

	// waitLog waits for a line holding every part to be logged after the first `from` bytes.
	waitLog := func(from int, parts ...string) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for {
			for line := range strings.SplitSeq(s.logs.String()[from:], "\n") {
				ok := true
				for _, part := range parts {
					ok = ok && strings.Contains(line, part)
				}
				if ok {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("the gallery did not log %q", parts)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	listed := func(want int, when string) {
		t.Helper()
		if all, _ := s.all(120); len(all) != want {
			t.Errorf("%s: %d tiles listed, want %d", when, len(all), want)
		}
	}

	// --- the database locked by the bot for longer than the gallery waits ----
	mark := len(s.logs.String())
	release, err := db.Hold(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitLog(mark, "keeping the previous snapshot", "database is locked")
	listed(seeded.Tiles+3, "while the database is locked")
	if resp, _ := s.get("/img/"+tiles[1].Key+".jpg", true); resp.StatusCode != 200 {
		t.Errorf("an image while the database is locked: %d", resp.StatusCode)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddNew(ctx, time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	waitCount(4, "after the lock was released")

	// --- the database missing ------------------------------------------------
	away := filepath.Join(tmp, "moved.db")
	mark = len(s.logs.String())
	if err := os.Rename(dbPath, away); err != nil {
		t.Fatal(err)
	}
	waitLog(mark, "keeping the previous snapshot", "unable to open database file")
	listed(seeded.Tiles+4, "while the database is missing")
	// Until it has been unreadable for three refresh intervals the gallery stays healthy; then it says so.
	deadline := time.Now().Add(40 * time.Second)
	for {
		if resp, _ := s.get("/healthz", false); resp.StatusCode == 503 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("/healthz never reported the unreadable database")
		}
		time.Sleep(250 * time.Millisecond)
	}
	listed(seeded.Tiles+4, "while unhealthy")
	if entries, _ := os.ReadDir(botDir); len(entries) != 0 {
		t.Errorf("the gallery created something in the bot's directory: %v", entries)
	}
	if err := os.Rename(away, dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddNew(ctx, time.Now(), 2); err != nil {
		t.Fatal(err)
	}
	waitCount(6, "after the database came back")
	if resp, _ := s.get("/healthz", false); resp.StatusCode != 200 {
		t.Errorf("healthz once the database is back: %d", resp.StatusCode)
	}
	s.stop()

	// --- starting before the bot has made its database -----------------------
	if err := os.Rename(dbPath, away); err != nil {
		t.Fatal(err)
	}
	mark = len(s.logs.String())
	s.cmd = exec.Command(bin)
	s.cmd.Env = s.env
	s.cmd.Stdout, s.cmd.Stderr = s.logs, s.logs
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitLog(mark, "nothing has been read yet", "unable to open database file")
	waitLog(mark, "level=WARN", "cannot be seen yet")
	if resp, _ := s.get("/healthz", false); resp.StatusCode != 503 {
		t.Errorf("healthz with no database: %d", resp.StatusCode)
	}
	if p := s.json("/api/tiles"); len(p.Tiles) != 0 {
		t.Errorf("%d tiles with no database", len(p.Tiles))
	}
	if entries, _ := os.ReadDir(botDir); len(entries) != 0 {
		t.Errorf("the gallery created something in the bot's directory: %v", entries)
	}
	if err := os.Rename(away, dbPath); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		if resp, _ := s.get("/healthz", false); resp.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the gallery did not pick up the database once it appeared")
		}
		time.Sleep(250 * time.Millisecond)
	}
	listed(seeded.Tiles+6, "once the database appeared")

	// --- the admin hides images: the same steps as with DynamoDB --------------
	// The hidden list is the gallery's own; the bot's directory stays as it is.
	beforeHiding := dirState(t, botDir)
	hideAcrossRestarts(t, s, true, filepath.Join(tmp, "cache"), filepath.Join(tmp, "state"))
	if now := dirState(t, botDir); now != beforeHiding {
		t.Errorf("hiding and restoring changed the bot's directory:\nbefore %s\nafter  %s", beforeHiding, now)
	}

	final := dirState(t, botDir)
	s.stop()
	if now := dirState(t, botDir); now != final {
		t.Errorf("the bot's directory changed while the gallery stopped:\nbefore %s\nafter  %s", final, now)
	}
	if entries, _ := os.ReadDir(botDir); len(entries) != 1 || entries[0].Name() != "dalibot.db" {
		t.Errorf("the bot's directory holds more than its database: %v", entries)
	}

	// --- the log -------------------------------------------------------------
	logs := s.logs.String()
	for _, secret := range []string{devstack.FileIDPrefix, devstack.BotToken, devstack.GalleryToken, "photos/file_"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the gallery's log contains %q", secret)
		}
	}
	for _, want := range []string{"config.source=sqlite", "config.sqlite_path=" + dbPath, "snapshot refreshed", "thumbnails warmed", "stopped"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the gallery's log lacks %q", want)
		}
	}
	for _, unwanted := range []string{"config.table=", "config.region=", "DynamoDB", "127.0.0.1:1"} {
		if strings.Contains(logs, unwanted) {
			t.Errorf("the gallery's log mentions %q although the source is SQLite", unwanted)
		}
	}
}
