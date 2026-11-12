// Command devstack runs the whole gallery locally with no AWS account and no
// Telegram bot: a moto DynamoDB server seeded with about 150 items in the
// bot's real shapes, a fake Telegram Bot API serving the sample JPEGs in
// testdata/telegram, and the real gallery binary pointed at both.
//
// Run it from the repository root, usually through scripts/dev-stack.sh:
//
//	scripts/dev-stack.sh                      # gallery on http://127.0.0.1:8080
//	scripts/dev-stack.sh -port 9000 -auth=false
//	scripts/dev-stack.sh -port 9000 -control 9001 -refresh 5s
//
// When everything is up it prints one line of JSON to stdout and keeps running
// until interrupted (Ctrl-C, SIGTERM):
//
//	{"ready":true,"url":"http://127.0.0.1:8080","token":"dev-gallery-token-0123456789","control":"http://127.0.0.1:8081","tiles":193,"records":145}
//
// Sign in at <url>/login with the token (or use it as the HTTP Basic password).
//
// The control server (loopback only) is for tests:
//
//	POST <control>/add?n=3   write 3 new records to moto, as if the bot had just drawn them;
//	                         the gallery shows them after its next refresh (-refresh, default 5s)
//	GET  <control>/state     {"tiles":…,"records":…,"getFile":…,"downloads":…,"forbidden":[…]}
//	                         "forbidden" lists any Telegram request other than getFile and the
//	                         file download; it must stay empty
//
// Requirements: Go, libvips with headers (to build the gallery), and the moto
// server at /opt/moto/bin/moto_server or $MOTO_SERVER (pip install 'moto[server]').
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/devstack"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devstack:", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 8080, "port for the gallery on 127.0.0.1")
	control := flag.Int("control", -1, "port for the control server; -1 means port+1, 0 means none")
	token := flag.String("token", devstack.GalleryToken, "the gallery's access token (at least 16 characters)")
	auth := flag.Bool("auth", true, "require the token; -auth=false runs with GALLERY_AUTH=off")
	refresh := flag.Duration("refresh", 5*time.Second, "how often the gallery re-reads the table (GALLERY_REFRESH)")
	warm := flag.Int("warm", 120, "how many of the newest tiles to pre-generate (GALLERY_WARM)")
	delay := flag.Duration("telegram-delay", 0, "delay added to every fake Telegram answer")
	cacheDir := flag.String("cache", "", "cache directory; default is a temporary one that is removed on exit")
	images := flag.String("images", "testdata/telegram", "directory of sample JPEGs the fake Telegram serves")
	binary := flag.String("binary", "", "an already built gallery binary; default is to build ./cmd/gallery")
	debug := flag.Bool("debug", false, "log every image request (GALLERY_DEBUG=1)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	tmp, err := os.MkdirTemp("", "dalibot-gallery-dev-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if *cacheDir == "" {
		*cacheDir = filepath.Join(tmp, "cache")
	}

	imgs, err := devstack.LoadImages(*images)
	if err != nil {
		return fmt.Errorf("%w (run from the repository root)", err)
	}

	bin := *binary
	if bin == "" {
		bin = filepath.Join(tmp, "gallery")
		build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/gallery")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("building the gallery (run from the repository root): %w", err)
		}
	}

	moto, err := devstack.StartMoto(ctx)
	if err != nil {
		return err
	}
	defer moto.Stop()
	seeded, err := devstack.Seed(ctx, moto.URL, devstack.Table, time.Now(), devstack.StoreDays)
	if err != nil {
		return err
	}

	// The fake Telegram API, on a free loopback port.
	tg := devstack.NewTelegram(devstack.BotToken, imgs)
	tg.Delay = *delay
	tgListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	tgServer := &http.Server{Handler: tg, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = tgServer.Serve(tgListener) }()
	defer tgServer.Close()

	// The gallery itself.
	listen := "127.0.0.1:" + strconv.Itoa(*port)
	env := append(os.Environ(), devstack.AWSEnv(moto.URL)...)
	env = append(env,
		"GALLERY_LISTEN="+listen,
		"DALIBOT_TABLE="+devstack.Table,
		"TELEGRAM_BOT_TOKEN="+devstack.BotToken,
		"TELEGRAM_API_BASE=http://"+tgListener.Addr().String(),
		"GALLERY_STORE_DAYS="+strconv.Itoa(devstack.StoreDays),
		"GALLERY_REFRESH="+refresh.String(),
		"GALLERY_WARM="+strconv.Itoa(*warm),
		"GALLERY_CACHE_DIR="+*cacheDir,
		"GALLERY_CHATS=",
	)
	if *auth {
		env = append(env, "GALLERY_TOKEN="+*token, "GALLERY_AUTH=")
	} else {
		env = append(env, "GALLERY_TOKEN=", "GALLERY_AUTH=off")
	}
	if *debug {
		env = append(env, "GALLERY_DEBUG=1")
	}
	gallery := exec.Command(bin)
	gallery.Env = env
	gallery.Stdout, gallery.Stderr = os.Stderr, os.Stderr
	if err := gallery.Start(); err != nil {
		return fmt.Errorf("starting the gallery: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- gallery.Wait() }()
	defer func() {
		_ = gallery.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = gallery.Process.Kill()
		}
	}()

	// Ready when /healthz says so (the first scan has succeeded).
	url := "http://" + listen
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-exited:
			exited <- err
			return fmt.Errorf("the gallery exited during start-up: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return errors.New("the gallery did not become healthy within 60 s")
		}
	}

	// The control server.
	var mu sync.Mutex
	tiles, records := seeded.Tiles, seeded.Records
	controlURL := ""
	if *control < 0 {
		*control = *port + 1
	}
	if *control > 0 {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
			count := 1
			if v := r.URL.Query().Get("n"); v != "" {
				c, err := strconv.Atoi(v)
				if err != nil || c < 1 || c > 500 {
					http.Error(w, "n must be between 1 and 500", http.StatusBadRequest)
					return
				}
				count = c
			}
			added, err := devstack.AddNew(r.Context(), moto.URL, devstack.Table, time.Now(), devstack.StoreDays, count)
			mu.Lock()
			tiles += len(added)
			records += len(added)
			mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"added": len(added)})
		})
		mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
			getFile, downloads := tg.Calls()
			forbidden := tg.Forbidden()
			if forbidden == nil {
				forbidden = []string{}
			}
			mu.Lock()
			state := map[string]any{"tiles": tiles, "records": records, "getFile": getFile, "downloads": downloads, "forbidden": forbidden}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(state)
		})
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(*control))
		if err != nil {
			return fmt.Errorf("control server: %w", err)
		}
		cs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = cs.Serve(l) }()
		defer cs.Close()
		controlURL = "http://" + l.Addr().String()
	}

	ready := map[string]any{"ready": true, "url": url, "control": controlURL, "tiles": seeded.Tiles, "records": seeded.Records}
	if *auth {
		ready["token"] = *token
	}
	line, _ := json.Marshal(ready)
	fmt.Println(string(line))

	select {
	case <-ctx.Done():
		return nil
	case err := <-exited:
		exited <- err
		return fmt.Errorf("the gallery exited: %v", err)
	}
}
