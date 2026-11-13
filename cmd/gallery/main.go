// Command gallery serves a web gallery of the images dalibot has recorded, in
// its DynamoDB table or in the SQLite database of its self-hosted mode.
//
// It is configured only through environment variables (see internal/config and
// .env.example). It reads the table with Scan, or the database file through a
// read-only connection, fetches originals from Telegram with getFile and the
// file download, and never writes to any of them.
//
// Usage:
//
//	gallery                 run the server
//	gallery -healthcheck    GET /healthz on GALLERY_LISTEN; exit 0 if healthy (for container health checks)
//	gallery -version        print the version of the thumbnail pipeline and of libvips
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/cache"
	"github.com/robdevops/dalibot-gallery/internal/config"
	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/server"
	"github.com/robdevops/dalibot-gallery/internal/source"
	"github.com/robdevops/dalibot-gallery/internal/telegram"
	"github.com/robdevops/dalibot-gallery/web"
)

func main() {
	os.Exit(run())
}

func run() int {
	healthcheck := flag.Bool("healthcheck", false, "check /healthz of the running gallery and exit 0 if it is healthy")
	version := flag.Bool("version", false, "print version information and exit")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "gallery: unexpected arguments; configuration is read from the environment")
		return 2
	}
	if *healthcheck {
		return checkHealth()
	}
	if *version {
		fmt.Printf("dalibot-gallery pipeline v%d, libvips %s\n", imaging.PipelineVersion, imaging.Version())
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// Every log line passes through the redacting handler, so neither token can be logged.
	level := slog.LevelInfo
	if os.Getenv("GALLERY_DEBUG") == "1" {
		level = slog.LevelDebug
	}
	log := slog.New(server.RedactingHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), cfg.Secrets()...))
	slog.SetDefault(log)
	log.Info("configuration", "config", cfg)

	if err := imaging.Startup(imaging.Options{Workers: cfg.ImageWorkers, Quality: cfg.WebPQuality}); err != nil {
		log.Error("starting libvips failed", "error", err)
		return 1
	}
	defer imaging.Shutdown()
	log.Info("libvips started", "vips_version", imaging.Version(), "pipeline", imaging.Tag(), "workers", cfg.ImageWorkers)

	ca, err := cache.Open(cfg.CacheDir, cfg.CacheMaxBytes(), log)
	if err != nil {
		log.Error("opening the cache failed", "dir", cfg.CacheDir, "error", err)
		return 1
	}
	tg := telegram.New(cfg.TelegramAPIBase, cfg.TelegramToken, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Only the chosen source is set up: with SQLite, no AWS setting is read and no AWS client exists.
	var src source.Source
	switch cfg.Source {
	case config.SourceSQLite:
		src, err = source.NewSQLite(source.SQLiteOptions{Path: cfg.SQLitePath, StoreDays: cfg.StoreDays, Logger: log})
		if err != nil {
			log.Error("setting up the SQLite source failed", "error", err)
			return 1
		}
		if _, statErr := os.Stat(cfg.SQLitePath); statErr != nil {
			log.Warn("the bot's SQLite database cannot be seen yet; the gallery stays empty until it can be read",
				"path", cfg.SQLitePath, "error", statErr)
		}
	default:
		src, err = source.NewDynamo(ctx, source.DynamoOptions{Table: cfg.Table, Region: cfg.Region, Logger: log})
		if err != nil {
			log.Error("setting up DynamoDB failed", "error", err)
			return 1
		}
	}
	store := source.NewStore(src, source.Options{
		StoreDays: cfg.StoreDays, Chats: cfg.Chats, DefaultCommand: cfg.DefaultCommand,
	}, log)

	srv, err := server.New(server.Deps{Config: cfg, Store: store, Fetcher: tg, Cache: ca, Web: web.Files, Log: log})
	if err != nil {
		log.Error("setting up the server failed", "error", err)
		return 1
	}
	// Runs after every successful read of the source: warms the newest thumbnails and prunes the cache.
	store.OnSwap(srv.OnSwap)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Error("listening failed", "listen", cfg.Listen, "error", err)
		return 1
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: server.ReadHeaderTimeout,
		ReadTimeout:       server.ReadTimeout,
		WriteTimeout:      server.WriteTimeout,
		IdleTimeout:       server.IdleTimeout,
		MaxHeaderBytes:    server.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	var refresher sync.WaitGroup
	refresher.Go(func() { store.Run(ctx, cfg.Refresh) })

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	log.Info("listening", "listen", ln.Addr().String(), "public", cfg.Public, "admin", cfg.AdminEnabled())
	if cfg.Public {
		log.Warn("GALLERY_PUBLIC=1: anyone who can reach this gallery can view it, without a login")
	}

	code := 0
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("the HTTP server stopped", "error", err)
			code = 1
		}
	}
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown was not clean", "error", err)
		_ = httpSrv.Close()
	}
	// Stop the refresher and all image work before libvips goes away (the deferred imaging.Shutdown).
	refresher.Wait()
	srv.Close()
	log.Info("stopped")
	return code
}

// checkHealth asks the running gallery for /healthz. It needs only GALLERY_LISTEN.
func checkHealth() int {
	listen := os.Getenv("GALLERY_LISTEN")
	if listen == "" {
		listen = config.DefaultListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gallery: GALLERY_LISTEN must be host:port")
		return 2
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gallery: health check failed:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "gallery: unhealthy:", resp.Status)
		return 1
	}
	return 0
}
