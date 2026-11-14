// Package server is the gallery's HTTP side: routes, auth, security headers,
// the JSON API, the image routes and the thumbnail warmer.
//
// Nothing here ever writes a Telegram file_id, a token, a Telegram URL or a
// file_path to a response or to the log. A file_id is read from the snapshot
// only inside original, and is passed only to Fetcher.Fetch.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/robdevops/dalibot-gallery/internal/cache"
	"github.com/robdevops/dalibot-gallery/internal/config"
	"github.com/robdevops/dalibot-gallery/internal/hidden"
	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/source"
)

// Headers sent on every response.
const (
	CSP               = "default-src 'none'; img-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	PermissionsPolicy = "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=(), interest-cohort=()"
)

// Limits of the listing API.
const (
	DefaultLimit = 60
	MaxLimit     = 120
)

// Timeouts and sizes for http.Server, as the brief fixes them.
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 10 * time.Second
	WriteTimeout      = 60 * time.Second
	IdleTimeout       = 120 * time.Second
	MaxHeaderBytes    = 16 << 10
)

// Fetcher downloads an original image by Telegram file_id. *telegram.Client satisfies it.
type Fetcher interface {
	Fetch(ctx context.Context, fileID string) ([]byte, error)
}

// Deps is everything the server needs.
type Deps struct {
	Config  *config.Config
	Store   *source.Store
	Fetcher Fetcher
	Cache   *cache.Cache
	// Hidden is the list of images an admin has hidden. It must be the list
	// behind the Store's Options.Hidden, or hiding would change nothing. It
	// may be nil: then nothing can be hidden or restored, and the routes for
	// that do not exist.
	Hidden *hidden.List
	// Web is web.Files: index.html, login.html, hidden.html, app.js, app.css,
	// admin.js, admin.css and favicon.png at its root.
	Web fs.FS
	Log *slog.Logger
}

// asset is an embedded static file.
type asset struct {
	data  []byte
	ctype string
	etag  string
	open  bool // served without auth
	admin bool // served to admins only; to everyone else the path does not exist
}

// Server holds the handler and the background image work.
type Server struct {
	cfg     *config.Config
	store   *source.Store
	fetcher Fetcher
	cache   *cache.Cache
	hidden  *hidden.List // nil: nothing can be hidden
	log     *slog.Logger

	index, login, hiddenPage *template.Template
	assets                   map[string]asset // by URL path
	tag                      string           // imaging.Tag()
	viewSession              string           // the view session cookie's value; "" in public mode
	adminSession             string           // the admin session cookie's value; "" without an admin token
	limiter                  *loginLimiter
	handler                  http.Handler

	origins singleflight.Group // per key
	thumbs  singleflight.Group // per key and size
	invalid failures           // originals that Telegram served but that are not usable JPEGs

	// Background work (shared image jobs, the warmer, pruning) runs under ctx
	// and is counted in wg, so Close can stop it before libvips shuts down.
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup

	swapMu     sync.Mutex
	warmCancel context.CancelFunc
	pruning    bool
}

// New returns the server with every route, auth, headers and gzip applied.
// It parses the templates and returns an error if they or the assets are missing or invalid.
// imaging.Startup must have been called.
func New(d Deps) (*Server, error) {
	if d.Config == nil || d.Store == nil || d.Fetcher == nil || d.Cache == nil || d.Web == nil {
		return nil, errors.New("server: Config, Store, Fetcher, Cache and Web are all required")
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		cfg: d.Config, store: d.Store, fetcher: d.Fetcher, cache: d.Cache, hidden: d.Hidden, log: log,
		assets:  map[string]asset{},
		tag:     imaging.Tag(),
		limiter: newLoginLimiter(),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	// config.FromEnv guarantees all of this; a Config built by hand must not
	// be able to open the gallery by accident.
	switch cfg := d.Config; {
	case !cfg.Public && cfg.Token == "":
		return nil, errors.New("server: viewing is not public but no token is configured")
	case cfg.Public && cfg.Token != "":
		return nil, errors.New("server: viewing is public but a token is also configured")
	case cfg.AdminToken != "" && cfg.AdminToken == cfg.Token:
		return nil, errors.New("server: the admin token must differ from the token")
	}
	if d.Config.Token != "" {
		s.viewSession = sessionValue(d.Config.Token)
	}
	if d.Config.AdminToken != "" {
		s.adminSession = adminSessionValue(d.Config.AdminToken)
	}
	var err error
	if s.index, err = template.ParseFS(d.Web, "index.html"); err != nil {
		return nil, fmt.Errorf("server: index.html: %w", err)
	}
	if s.login, err = template.ParseFS(d.Web, "login.html"); err != nil {
		return nil, fmt.Errorf("server: login.html: %w", err)
	}
	if s.hiddenPage, err = template.ParseFS(d.Web, "hidden.html"); err != nil {
		return nil, fmt.Errorf("server: hidden.html: %w", err)
	}
	for name, ctype := range map[string]string{
		"app.js":      "text/javascript; charset=utf-8",
		"app.css":     "text/css; charset=utf-8",
		"admin.js":    "text/javascript; charset=utf-8",
		"admin.css":   "text/css; charset=utf-8",
		"favicon.png": "image/png",
	} {
		data, err := fs.ReadFile(d.Web, name)
		if err != nil {
			return nil, fmt.Errorf("server: %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		s.assets["/"+name] = asset{
			data: data, ctype: ctype, etag: `"` + hex.EncodeToString(sum[:12]) + `"`,
			open:  name == "app.css" || name == "favicon.png",
			admin: strings.HasPrefix(name, "admin."),
		}
	}
	s.handler = s.middleware(http.HandlerFunc(s.route))
	return s, nil
}

// Handler is the root handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Close stops the background image work (warmer, pruning, shared thumbnail
// jobs) and waits for it. Call it after http.Server.Shutdown and before
// imaging.Shutdown. Requests served after Close get placeholders.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// begin registers one background job that runs on the caller's goroutine,
// which must call s.wg.Done when the job ends; it reports false once Close
// has been called.
func (s *Server) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

// spawn runs fn as one background job on a goroutine of its own; it reports
// false, without running fn, once Close has been called.
func (s *Server) spawn(fn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Go(fn)
	return true
}

// route dispatches by path. Security headers are already set by the middleware.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	api := strings.HasPrefix(p, "/api/")

	// Methods: GET and HEAD everywhere; POST on /login as well, and only
	// POST on /logout. Those two exist only when there is something to sign
	// in to; in a public gallery without an admin token they are unknown paths.
	login := s.cfg.LoginEnabled()
	// The admin's routes (admin.go) exist only with an admin token, and only
	// for an admin. For everyone else these paths are like any unknown path,
	// except that a POST is answered 404 rather than 405.
	adminPath := s.adminRoutes() && isAdminPath(p)
	allow := "GET, HEAD"
	switch {
	case login && p == "/login":
		allow = "GET, HEAD, POST"
	case login && p == "/logout":
		allow = "POST"
	}
	if !slices.Contains(strings.Split(allow, ", "), r.Method) && !(adminPath && r.Method == http.MethodPost) {
		w.Header().Set("Allow", allow)
		if api {
			s.jsonError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		} else {
			plain(w, http.StatusMethodNotAllowed, "method not allowed\n")
		}
		return
	}

	// Open routes.
	switch {
	case p == "/healthz":
		s.healthz(w)
		return
	case login && p == "/login":
		s.handleLogin(w, r)
		return
	case login && p == "/logout":
		s.handleLogout(w, r)
		return
	}
	// The stylesheet is open because the login page needs it. It holds no data.
	if a, ok := s.assets[p]; ok && a.open {
		s.serveAsset(w, r, a)
		return
	}

	// Everything else needs at least view access.
	level, wait := s.authorize(r)
	admin := level == accessAdmin && wait == 0
	if adminPath {
		if admin {
			s.handleAdmin(w, r, p)
			return
		}
		if r.Method == http.MethodPost {
			// Whoever it is and whatever the key: the same answer.
			s.notFound(w, r, api)
			return
		}
		// A GET goes on as for any path that does not exist.
	}
	if level < accessView {
		switch {
		case wait > 0 && api:
			w.Header().Set("Retry-After", fmt.Sprint(int(wait/time.Second)+1))
			s.jsonError(w, r, http.StatusTooManyRequests, "too many attempts")
		case wait > 0:
			tooMany(w, wait)
		case api:
			s.jsonError(w, r, http.StatusUnauthorized, "unauthorized")
		case strings.HasPrefix(p, "/img/"):
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
		case s.assets[p].data != nil && !s.assets[p].admin:
			plain(w, http.StatusUnauthorized, "unauthorized\n")
		default:
			redirect(w, "/login")
		}
		return
	}

	// The admin's script and stylesheet are for admins only. Nobody else is
	// served them, so an ordinary visitor's browser never downloads them.
	if a, ok := s.assets[p]; ok && (!a.admin || (admin && s.adminRoutes())) {
		s.serveAsset(w, r, a)
		return
	}
	// Handlers that answer differently to an admin are given the level. A page and the listing
	// are what someone looking at the gallery asks for: the store reads the bot's records only
	// while there is such a request (Store.Touch), and not at all while nobody looks.
	switch {
	case p == "/":
		s.store.Touch()
		s.handleIndex(w, r, level)
	case p == "/api/tiles":
		s.store.Touch()
		s.handleTiles(w, r, level)
	case p == "/api/tiles/new":
		s.store.Touch()
		s.handleNew(w, r, level)
	case strings.HasPrefix(p, "/img/"):
		s.handleImage(w, r, p[len("/img/"):], admin)
	default:
		s.notFound(w, r, api)
	}
}

// notFound is the answer for a path that does not exist.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, api bool) {
	if api {
		s.jsonError(w, r, http.StatusNotFound, "not found")
		return
	}
	plain(w, http.StatusNotFound, "not found\n")
}

func (s *Server) healthz(w http.ResponseWriter) {
	if s.store.Healthy(3 * s.cfg.Refresh) {
		plain(w, http.StatusOK, "ok\n")
		return
	}
	plain(w, http.StatusServiceUnavailable, "stale\n")
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, a asset) {
	h := w.Header()
	if a.admin {
		h.Set("Cache-Control", "private, no-cache") // a shared cache must not hand it to others
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("ETag", a.etag)
	if etagMatch(r.Header.Get("If-None-Match"), a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", a.ctype)
	h.Set("Content-Length", fmt.Sprint(len(a.data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.data)
}

// plain writes a short uncacheable text response.
func plain(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// redirect answers 303 to a fixed local path.
func redirect(w http.ResponseWriter, to string) {
	h := w.Header()
	h.Set("Location", to)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
}

// etagMatch reports whether an If-None-Match header names etag (or is "*").
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// recorder notes the status and size of a response for the access log.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// middleware sets the security headers on every response, recovers from
// panics and writes the access log. The log line holds the method, the path
// (never the query string, never a header or cookie), the status, the size,
// the duration and the peer address.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", PermissionsPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		rec := &recorder{ResponseWriter: w}
		start := time.Now()
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				if rec.status == 0 {
					plain(rec, http.StatusInternalServerError, "internal error\n")
				}
			}
			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelWarn
			case rec.status < 400 && (strings.HasPrefix(r.URL.Path, "/img/") || r.URL.Path == "/healthz" || s.assets[r.URL.Path].data != nil):
				level = slog.LevelDebug
			}
			s.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"bytes", rec.bytes, "duration", time.Since(start).Round(time.Microsecond), "remote", s.clientAddr(r))
		}()
		next.ServeHTTP(rec, r)
	})
}
