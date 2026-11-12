package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const (
	testToken = "123456:TEST-token_abc"
	testPath  = "photos/file_42.jpg"
)

// syncBuffer is a goroutine-safe log sink.
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

// fake is a stand-in for the Bot API. It serves only the two allowed
// endpoints; any other request fails the test.
type fake struct {
	t   *testing.T
	srv *httptest.Server

	getFiles  atomic.Int64
	downloads atomic.Int64
	others    atomic.Int64

	mu       sync.Mutex
	getFile  func(w http.ResponseWriter, r *http.Request, fileID string)
	download func(w http.ResponseWriter, r *http.Request, filePath string)
	fileIDs  []string

	log *syncBuffer
}

func okGetFile(path string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, 200, map[string]any{"ok": true, "result": map[string]any{
			"file_id": "x", "file_unique_id": "u", "file_size": 3, "file_path": path}})
	}
}

func apiError(w http.ResponseWriter, code int, desc string, retryAfter int) {
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	writeJSON(w, code, body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func serveBytes(data []byte) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write(data) }
}

// newFake starts the fake API on loopback and a client pointed at it, with a
// captured debug-level logger. The client is the one New makes, real transport
// and real clock included.
func newFake(t *testing.T) (*fake, *Client) {
	t.Helper()
	f := newFakeAPI(t)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f, f.client(f.srv.URL)
}

// newBubbleFake is newFake for a synctest bubble: the fake API listens on an
// in-memory network and the client's transport dials it, so nothing blocks on
// real I/O and time is the bubble's. Every wait in the client (a 429's pause,
// the request timeouts, the failure cache's TTLs) then runs at its real length
// in no real time.
func newBubbleFake(t *testing.T) (*fake, *Client) {
	t.Helper()
	f := newFakeAPI(t)
	f.srv = httptest.NewTestServer(t, http.HandlerFunc(f.serve))
	return f, newClient(f.srv.URL, testToken, f.logger(), f.srv.Client().Transport)
}

// bubble runs fn as a subtest in a synctest bubble; fn uses newBubbleFake.
func bubble(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) { synctest.Test(t, fn) })
}

func newFakeAPI(t *testing.T) *fake {
	f := &fake{t: t, log: &syncBuffer{}}
	f.getFile = okGetFile(testPath)
	f.download = serveBytes([]byte("\xff\xd8\xffjpeg"))
	t.Cleanup(func() { // runs after the server has closed
		if n := f.others.Load(); n != 0 {
			t.Errorf("%d request(s) to paths other than getFile and the file download", n)
		}
	})
	return f
}

func (f *fake) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (f *fake) client(base string) *Client { return New(base, testToken, f.logger()) }

func (f *fake) set(getFile func(http.ResponseWriter, *http.Request, string), download func(http.ResponseWriter, *http.Request, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if getFile != nil {
		f.getFile = getFile
	}
	if download != nil {
		f.download = download
	}
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	const filePrefix = "/file/bot" + testToken + "/"
	f.mu.Lock()
	getFile, download := f.getFile, f.download
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/bot"+testToken+"/getFile" && r.Method == http.MethodPost && r.URL.RawQuery == "":
		f.getFiles.Add(1)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			f.t.Errorf("getFile Content-Type = %q", ct)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("getFile body: %v", err)
		}
		id, _ := body["file_id"].(string)
		if len(body) != 1 || id == "" {
			f.t.Errorf("getFile body = %v, want only file_id", body)
		}
		f.mu.Lock()
		f.fileIDs = append(f.fileIDs, id)
		f.mu.Unlock()
		getFile(w, r, id)
	case strings.HasPrefix(r.URL.Path, filePrefix) && r.Method == http.MethodGet && r.URL.RawQuery == "":
		f.downloads.Add(1)
		download(w, r, strings.TrimPrefix(r.URL.Path, filePrefix))
	default:
		f.others.Add(1)
		f.t.Errorf("forbidden request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "forbidden", http.StatusTeapot)
	}
}

// asError checks that err is a *Error and returns it.
func asError(t *testing.T, err error) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("error is %T, want *Error: %v", err, err)
	}
	return e
}

// noSecrets fails if s contains anything that must stay server-side.
func noSecrets(t *testing.T, what, s string, secrets ...string) {
	t.Helper()
	secrets = append(secrets, testToken, url.QueryEscape(testToken), "TEST-token_abc", "/bot1", "123456%3A", "123456%3a")
	for _, sec := range secrets {
		if sec != "" && strings.Contains(s, sec) {
			t.Errorf("%s contains %q:\n%s", what, sec, s)
		}
	}
}

func TestFetch(t *testing.T) {
	f, c := newFake(t)
	want := []byte("\xff\xd8\xff the original bytes")
	var gotPath string
	f.set(nil, func(w http.ResponseWriter, _ *http.Request, p string) {
		gotPath = p
		_, _ = w.Write(want)
	})
	got, err := c.Fetch(context.Background(), "AgACAgUAAx0-file-id")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if gotPath != testPath {
		t.Errorf("download path = %q, want %q", gotPath, testPath)
	}
	if len(f.fileIDs) != 1 || f.fileIDs[0] != "AgACAgUAAx0-file-id" {
		t.Errorf("file ids sent = %v", f.fileIDs)
	}
	if g, d := f.getFiles.Load(), f.downloads.Load(); g != 1 || d != 1 {
		t.Errorf("getFile = %d, downloads = %d, want 1 and 1", g, d)
	}
	// Download links are never stored: a second Fetch asks getFile again.
	if _, err := c.Fetch(context.Background(), "AgACAgUAAx0-file-id"); err != nil {
		t.Fatal(err)
	}
	if g, d := f.getFiles.Load(), f.downloads.Load(); g != 2 || d != 2 {
		t.Errorf("after a second fetch: getFile = %d, downloads = %d, want 2 and 2", g, d)
	}
	noSecrets(t, "log", f.log.String(), "AgACAgUAAx0-file-id", testPath, f.srv.URL)
}

func TestBasePathPrefix(t *testing.T) {
	// A local Bot API server may sit under a path prefix.
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			okGetFile("a/b.jpg")(w, r, "")
			return
		}
		_, _ = w.Write([]byte("data"))
	}))
	defer srv.Close()
	c := New(srv.URL+"/tg/", testToken, nil) // nil logger, trailing slash tolerated
	if _, err := c.Fetch(context.Background(), "id"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /tg/bot" + testToken + "/getFile", "GET /tg/file/bot" + testToken + "/a/b.jpg"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Errorf("requests = %v, want %v", paths, want)
	}
}

// TestAllowlist drives the client's own HTTP client with requests the package
// never builds. The transport must refuse every one before it leaves.
func TestAllowlist(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	c := New(srv.URL, testToken, nil)
	bot := srv.URL + "/bot" + testToken
	file := srv.URL + "/file/bot" + testToken

	cases := []struct{ method, url string }{
		{"POST", bot + "/setWebhook"},
		{"POST", bot + "/deleteWebhook"},
		{"POST", bot + "/getUpdates"},
		{"GET", bot + "/getUpdates"},
		{"POST", bot + "/getMe"},
		{"POST", bot + "/sendMessage"},
		{"POST", bot + "/sendPhoto"},
		{"POST", bot + "/setMyCommands"},
		{"POST", bot + "/getfile"},
		{"POST", bot + "/getFile/"},
		{"POST", bot + "/getFile?offset=1"},
		{"GET", bot + "/getFile"},
		{"PUT", bot + "/getFile"},
		{"DELETE", bot + "/getFile"},
		{"HEAD", file + "/photos/a.jpg"},
		{"POST", file + "/photos/a.jpg"},
		{"GET", file + "/"},
		{"GET", file + "/photos/a.jpg?x=1"},
		{"GET", file + "/../bot" + testToken + "/getUpdates"},
		{"GET", file + "/photos/../../bot" + testToken + "/getUpdates"},
		{"GET", file + "/photos//a.jpg"},
		{"GET", srv.URL + "/file/bot999:other/photos/a.jpg"},
		{"POST", srv.URL + "/bot999:other/getFile"},
		{"GET", srv.URL + "/"},
		{"POST", other.URL + "/bot" + testToken + "/getFile"},
		{"GET", other.URL + "/file/bot" + testToken + "/photos/a.jpg"},
		{"POST", strings.Replace(bot, "http://", "https://", 1) + "/getFile"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, tc.url, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.http.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s %s: request was sent, want a refusal", tc.method, strings.ReplaceAll(tc.url, testToken, "TOKEN"))
			continue
		}
		if !errors.Is(err, errNotAllowed) {
			t.Errorf("%s %s: error %v, want errNotAllowed", tc.method, strings.ReplaceAll(tc.url, testToken, "TOKEN"), c.Redact(err.Error()))
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d forbidden request(s) reached a server", n)
	}

	// The two allowed shapes do pass.
	for _, tc := range []struct{ method, url string }{{"POST", bot + "/getFile"}, {"GET", file + "/photos/a.jpg"}} {
		req, _ := http.NewRequest(tc.method, tc.url, nil)
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.method, c.Redact(err.Error()))
		}
		resp.Body.Close()
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("allowed requests reaching the server = %d, want 2", n)
	}
}

// A file_path that would leave /file/bot<token>/ is never requested.
func TestHostileFilePath(t *testing.T) {
	for _, p := range []string{
		"../bot" + testToken + "/getUpdates", "/etc/passwd", "photos/../../x", "a//b",
		"a?b=c", "a#b", "a%2e%2e/b", `a\b`, "a\nb", "", "./a", strings.Repeat("a", 600),
	} {
		t.Run("", func(t *testing.T) {
			f, c := newFake(t)
			f.set(okGetFile(p), nil)
			_, err := c.Fetch(context.Background(), "some-file-id")
			e := asError(t, err)
			if e.Permanent {
				t.Errorf("path %q: permanent, want temporary", p)
			}
			if f.downloads.Load() != 0 {
				t.Errorf("path %q: a download was attempted", p)
			}
			noSecrets(t, "error", err.Error(), "some-file-id")
			noSecrets(t, "log", f.log.String(), "some-file-id")
		})
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var hits atomic.Int64
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer evil.Close()
	f, c := newFake(t)

	f.set(nil, func(w http.ResponseWriter, r *http.Request, _ string) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	})
	_, err := c.Fetch(context.Background(), "id-redirect-download")
	if e := asError(t, err); e.Permanent || e.Status != http.StatusFound {
		t.Errorf("download redirect: %+v", e)
	}

	f.set(func(w http.ResponseWriter, r *http.Request, _ string) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
	}, nil)
	_, err = c.Fetch(context.Background(), "id-redirect-getfile")
	if e := asError(t, err); e.Permanent || e.Status != http.StatusTemporaryRedirect {
		t.Errorf("getFile redirect: %+v", e)
	}
	// A redirect to another path on the same host is not followed either.
	f.set(func(w http.ResponseWriter, r *http.Request, _ string) {
		http.Redirect(w, r, "/bot"+testToken+"/getUpdates", http.StatusTemporaryRedirect)
	}, nil)
	_, err = c.Fetch(context.Background(), "id-redirect-same-host")
	asError(t, err)

	if n := hits.Load(); n != 0 {
		t.Errorf("redirect was followed %d time(s)", n)
	}
	noSecrets(t, "log", f.log.String(), evil.URL, f.srv.URL)
}

// TestNoSecretsInLogsOrErrors runs every failure mode and checks that neither
// the captured log nor any error mentions the token, the file id, the file
// path or the API URL.
func TestNoSecretsInLogsOrErrors(t *testing.T) {
	const fileID = "AgACAgUAAx0CSECRETFILEID"
	const secretPath = "photos/secret_file_77.jpg"
	// What a hostile or chatty server might echo back. The file path is only
	// echoed by the download endpoint: before getFile succeeds the client has
	// never seen it.
	leakGetFile := func(base string) string {
		return fmt.Sprintf("Bad Request: %s/bot%s/getFile %s file_id=%s",
			base, testToken, url.QueryEscape(testToken), fileID)
	}
	leak := func(base string) string {
		return leakGetFile(base) + fmt.Sprintf(" %s/file/bot%s/%s", base, testToken, secretPath)
	}
	type setup func(f *fake, c *Client)
	cases := map[string]setup{
		"success": func(f *fake, c *Client) { f.set(okGetFile(secretPath), nil) },
		"getFile 400 echoing secrets": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, 400, leakGetFile(f.srv.URL), 0) }, nil)
		},
		"getFile 401": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, 401, "Unauthorized "+testToken, 0) }, nil)
		},
		"getFile 500 not json": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) {
				http.Error(w, "<html>"+leakGetFile(f.srv.URL), 500)
			}, nil)
		},
		"getFile 200 garbage": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(leakGetFile(f.srv.URL))) }, nil)
		},
		"getFile 200 ok false": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) {
				writeJSON(w, 200, map[string]any{"ok": false, "error_code": 400, "description": leakGetFile(f.srv.URL)})
			}, nil)
		},
		"getFile connection dropped": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { hijackClose(w) }, nil)
		},
		"download 404 echoing secrets": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(w http.ResponseWriter, _ *http.Request, _ string) {
				apiError(w, 404, leak(f.srv.URL), 0)
			})
		},
		"download 400": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(w http.ResponseWriter, _ *http.Request, _ string) {
				http.Error(w, leak(f.srv.URL), 400)
			})
		},
		"download connection dropped": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(w http.ResponseWriter, _ *http.Request, _ string) { hijackClose(w) })
		},
		"download truncated": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(w http.ResponseWriter, _ *http.Request, _ string) {
				w.Header().Set("Content-Length", "1000")
				_, _ = w.Write([]byte("short"))
			})
		},
		"download empty": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(http.ResponseWriter, *http.Request, string) {})
		},
		"download too large": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(w http.ResponseWriter, _ *http.Request, _ string) {
				w.Header().Set("Content-Length", fmt.Sprint(MaxFileBytes+1))
			})
		},
	}
	// These wait: for a 429's pause, or for the 10 s and 30 s timeouts. They
	// run on virtual time against the in-memory server.
	waiting := map[string]setup{
		"getFile 429 twice": func(f *fake, c *Client) {
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, 429, leakGetFile(f.srv.URL), 2) }, nil)
		},
		"getFile timeout": func(f *fake, c *Client) {
			f.set(func(_ http.ResponseWriter, r *http.Request, _ string) { <-r.Context().Done() }, nil)
		},
		"download timeout": func(f *fake, c *Client) {
			f.set(okGetFile(secretPath), func(w http.ResponseWriter, r *http.Request, _ string) {
				_, _ = w.Write([]byte("partial"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
		},
	}
	check := func(name string, start func(*testing.T) (*fake, *Client), prepare setup) func(*testing.T) {
		return func(t *testing.T) {
			f, c := start(t)
			prepare(f, c)
			var errs []string
			for i := range 2 { // the second call replays the failure cache
				_, err := c.Fetch(context.Background(), fileID)
				if name == "success" {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				e := asError(t, err)
				if i == 1 && !e.Cached {
					t.Errorf("second error is not from the failure cache: %+v", e)
				}
				errs = append(errs, err.Error(), fmt.Sprintf("%v|%+v|%#v|%s", err, err, err, e.msg))
			}
			logged := f.log.String()
			if logged == "" {
				t.Error("nothing was logged")
			}
			if name != "success" && !strings.Contains(logged, "telegram fetch failed") {
				t.Errorf("failure was not logged:\n%s", logged)
			}
			secrets := []string{fileID, secretPath, "secret_file_77", f.srv.URL, strings.TrimPrefix(f.srv.URL, "http://")}
			noSecrets(t, "log", logged, secrets...)
			for _, s := range errs {
				noSecrets(t, "error", s, secrets...)
			}
		}
	}
	for name, prepare := range cases {
		t.Run(name, check(name, newFake, prepare))
	}
	for name, prepare := range waiting {
		bubble(t, name, check(name, newBubbleFake, prepare))
	}

	t.Run("server down", func(t *testing.T) {
		f, _ := newFake(t)
		dead := httptest.NewServer(http.NotFoundHandler())
		base := dead.URL
		dead.Close()
		c := f.client(base)
		_, err := c.Fetch(context.Background(), fileID)
		e := asError(t, err)
		if e.Permanent || e.Status != 0 {
			t.Errorf("%+v", e)
		}
		secrets := []string{fileID, base, strings.TrimPrefix(base, "http://")}
		noSecrets(t, "error", fmt.Sprintf("%v|%#v", err, err), secrets...)
		noSecrets(t, "log", f.log.String(), secrets...)
		if !strings.Contains(f.log.String(), "telegram fetch failed") {
			t.Error("failure was not logged")
		}
	})
}

func hijackClose(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

// A *url.Error prints the request URL. Whatever the transport returns, the
// *Error made from it must not.
func TestTransportErrorIsRedacted(t *testing.T) {
	c := New("https://api.telegram.org", testToken, nil)
	fl := &flight{c: c, secrets: c.secrets("the-file-id")}
	raw := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + testToken + "/getFile",
		Err: fmt.Errorf("proxy said: https://api.telegram.org/bot%s/getFile the-file-id %s", testToken, url.QueryEscape(testToken))}
	if !strings.Contains(raw.Error(), testToken) {
		t.Fatal("test is broken: the raw error should contain the token")
	}
	e := fl.transportError("getFile", raw)
	noSecrets(t, "error", e.Error(), "the-file-id", "api.telegram.org")
	// Nothing reachable through the error chain may hold the raw error.
	if _, ok := errors.AsType[*url.Error](e); ok {
		t.Error("*url.Error is reachable through the error chain")
	}
}

func TestRetryAfter(t *testing.T) {
	bubble(t, "getFile retried once after retry_after", func(t *testing.T) {
		f, c := newBubbleFake(t)
		var n atomic.Int64
		f.set(func(w http.ResponseWriter, r *http.Request, id string) {
			if n.Add(1) == 1 {
				apiError(w, 429, "Too Many Requests: retry after 7", 7)
				return
			}
			okGetFile(testPath)(w, r, id)
		}, nil)
		start := time.Now()
		data, err := c.Fetch(context.Background(), "id")
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Error("no data")
		}
		if got := time.Since(start); got != 7*time.Second {
			t.Errorf("waited %v, want 7s", got)
		}
		if g := f.getFiles.Load(); g != 2 {
			t.Errorf("getFile calls = %d, want 2", g)
		}
	})

	bubble(t, "download retried once", func(t *testing.T) {
		f, c := newBubbleFake(t)
		var n atomic.Int64
		f.set(nil, func(w http.ResponseWriter, _ *http.Request, _ string) {
			if n.Add(1) == 1 {
				w.Header().Set("Retry-After", "3") // header only, no JSON body
				w.WriteHeader(429)
				return
			}
			_, _ = w.Write([]byte("data"))
		})
		start := time.Now()
		if _, err := c.Fetch(context.Background(), "id"); err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 3*time.Second {
			t.Errorf("waited %v, want 3s", got)
		}
		if g, d := f.getFiles.Load(), f.downloads.Load(); g != 1 || d != 2 {
			t.Errorf("getFile = %d, downloads = %d, want 1 and 2", g, d)
		}
	})

	bubble(t, "only one retry per fetch", func(t *testing.T) {
		f, c := newBubbleFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, 429, "Too Many Requests", 5) }, nil)
		start := time.Now()
		_, err := c.Fetch(context.Background(), "id")
		e := asError(t, err)
		if e.Status != 429 || e.RetryAfter != 5*time.Second || e.Permanent || !e.Temporary() {
			t.Errorf("%+v", e)
		}
		if g := f.getFiles.Load(); g != 2 {
			t.Errorf("getFile calls = %d, want 2 (one retry)", g)
		}
		if got := time.Since(start); got != 5*time.Second {
			t.Errorf("waited %v, want 5s", got)
		}
	})

	bubble(t, "a long retry_after is not waited for", func(t *testing.T) {
		f, c := newBubbleFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, 429, "Too Many Requests", 3600) }, nil)
		start := time.Now()
		_, err := c.Fetch(context.Background(), "id-a")
		e := asError(t, err)
		if e.Status != 429 || e.RetryAfter != time.Hour || e.Permanent {
			t.Errorf("%+v", e)
		}
		if got := time.Since(start); got != 0 {
			t.Errorf("waited %v, want no wait", got)
		}
		if g := f.getFiles.Load(); g != 1 {
			t.Errorf("getFile calls = %d, want 1", g)
		}
		// While Telegram's pause lasts, no request is sent for any file.
		_, err = c.Fetch(context.Background(), "id-b")
		if e := asError(t, err); e.Status != 429 || e.Cached {
			t.Errorf("%+v", e)
		}
		time.Sleep(59 * time.Minute)
		_, err = c.Fetch(context.Background(), "id-a")
		if e := asError(t, err); !e.Cached {
			t.Errorf("after 59 min: not from the failure cache: %+v", e)
		}
		if g := f.getFiles.Load(); g != 1 {
			t.Errorf("getFile calls during the pause = %d, want 1", g)
		}
		// After it, requests resume.
		time.Sleep(2 * time.Minute)
		f.set(okGetFile(testPath), nil)
		if _, err := c.Fetch(context.Background(), "id-a"); err != nil {
			t.Fatal(err)
		}
	})

	bubble(t, "missing retry_after means one second", func(t *testing.T) {
		f, c := newBubbleFake(t)
		var n atomic.Int64
		f.set(func(w http.ResponseWriter, r *http.Request, id string) {
			if n.Add(1) == 1 {
				w.WriteHeader(429)
				return
			}
			okGetFile(testPath)(w, r, id)
		}, nil)
		start := time.Now()
		if _, err := c.Fetch(context.Background(), "id"); err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != time.Second {
			t.Errorf("waited %v, want 1s", got)
		}
	})

	bubble(t, "the wait ends with the caller's context", func(t *testing.T) {
		f, c := newBubbleFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, 429, "Too Many Requests", 20) }, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := c.Fetch(ctx, "id")
		if d := time.Since(start); d != 150*time.Millisecond {
			t.Errorf("Fetch returned after %v, want 150ms: when its context ended", d)
		}
		e := asError(t, err)
		if e.Permanent || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTemporary) {
			t.Errorf("%+v", e)
		}
		// The flight is not the caller's: it sits out the pause and asks once
		// more. Let it end before the bubble does.
		synctest.Sleep(20 * time.Second)
		if g := f.getFiles.Load(); g != 2 {
			t.Errorf("getFile calls = %d, want 2 (the flight outlives its caller)", g)
		}
	})
}

func TestSleepCtx(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if err := sleepCtx(context.Background(), time.Hour); err != nil {
			t.Errorf("err = %v", err)
		}
		if d := time.Since(start); d != time.Hour {
			t.Errorf("slept %v, want 1h", d)
		}
	})
}

func TestFailureCache(t *testing.T) {
	bubble(t, "400 is cached for 24 h", func(t *testing.T) {
		f, c := newBubbleFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) {
			apiError(w, 400, "Bad Request: wrong file_id or the file is temporarily unavailable", 0)
		}, nil)
		_, err := c.Fetch(context.Background(), "dead")
		e := asError(t, err)
		if !e.Permanent || e.Temporary() || e.Status != 400 || e.Cached {
			t.Errorf("first: %+v", e)
		}
		if !errors.Is(err, ErrPermanent) || errors.Is(err, ErrTemporary) {
			t.Error("sentinels do not match a permanent error")
		}
		if !strings.Contains(err.Error(), "wrong file_id") {
			t.Errorf("Telegram's description is missing: %v", err)
		}

		time.Sleep(24*time.Hour - time.Second)
		_, err = c.Fetch(context.Background(), "dead")
		e = asError(t, err)
		if !e.Cached || !e.Permanent || e.Status != 400 {
			t.Errorf("cached: %+v", e)
		}
		if g := f.getFiles.Load(); g != 1 {
			t.Errorf("getFile calls = %d, want 1 (a cached failure makes no request)", g)
		}
		// Another file id is not affected.
		f.set(okGetFile(testPath), nil)
		if _, err := c.Fetch(context.Background(), "alive"); err != nil {
			t.Fatal(err)
		}
		// After 24 h the file is tried again, and a success clears nothing else.
		time.Sleep(2 * time.Second)
		if _, err := c.Fetch(context.Background(), "dead"); err != nil {
			t.Fatalf("after expiry: %v", err)
		}
		if g := f.getFiles.Load(); g != 3 {
			t.Errorf("getFile calls = %d, want 3", g)
		}
	})

	bubble(t, "other errors are cached for 5 min", func(t *testing.T) {
		for _, status := range []int{401, 403, 404, 500, 502} {
			f, c := newBubbleFake(t)
			f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { apiError(w, status, "nope", 0) }, nil)
			_, err := c.Fetch(context.Background(), "id")
			e := asError(t, err)
			if e.Permanent || e.Status != status || e.Cached || !errors.Is(err, ErrTemporary) || errors.Is(err, ErrPermanent) {
				t.Errorf("%d first: %+v", status, e)
			}
			time.Sleep(5*time.Minute - time.Second)
			_, err = c.Fetch(context.Background(), "id")
			if e := asError(t, err); !e.Cached || e.Status != status || e.Permanent {
				t.Errorf("%d cached: %+v", status, e)
			}
			if g := f.getFiles.Load(); g != 1 {
				t.Errorf("%d: getFile calls = %d, want 1", status, g)
			}
			time.Sleep(2 * time.Second)
			_, err = c.Fetch(context.Background(), "id")
			if e := asError(t, err); e.Cached {
				t.Errorf("%d after expiry: still cached", status)
			}
			if g := f.getFiles.Load(); g != 2 {
				t.Errorf("%d: getFile calls = %d, want 2", status, g)
			}
		}
	})

	t.Run("download failures are cached too", func(t *testing.T) {
		f, c := newFake(t)
		f.set(nil, func(w http.ResponseWriter, _ *http.Request, _ string) { http.Error(w, "gone", 404) })
		_, err := c.Fetch(context.Background(), "id")
		if e := asError(t, err); e.Permanent || e.Status != 404 {
			t.Errorf("%+v", e)
		}
		_, err = c.Fetch(context.Background(), "id")
		if e := asError(t, err); !e.Cached {
			t.Errorf("%+v", e)
		}
		if g, d := f.getFiles.Load(), f.downloads.Load(); g != 1 || d != 1 {
			t.Errorf("getFile = %d, downloads = %d, want 1 and 1", g, d)
		}
		f.set(nil, func(w http.ResponseWriter, _ *http.Request, _ string) { http.Error(w, "bad", 400) })
		_, err = c.Fetch(context.Background(), "id2")
		if e := asError(t, err); !e.Permanent || e.Status != 400 {
			t.Errorf("download 400: %+v", e)
		}
	})

	t.Run("a network error is cached for 5 min", func(t *testing.T) {
		f, c := newFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) { hijackClose(w) }, nil)
		_, err := c.Fetch(context.Background(), "id")
		if e := asError(t, err); e.Permanent || e.Status != 0 || e.Cached {
			t.Errorf("%+v", e)
		}
		_, err = c.Fetch(context.Background(), "id")
		if e := asError(t, err); !e.Cached {
			t.Errorf("%+v", e)
		}
		if g := f.getFiles.Load(); g != 1 {
			t.Errorf("getFile calls = %d, want 1", g)
		}
	})

	t.Run("the cache is bounded", func(t *testing.T) {
		_, c := newFake(t)
		for i := range maxFailures + 50 {
			c.remember(fmt.Sprint("id-", i), &Error{msg: "x"})
		}
		if n := len(c.failures); n > maxFailures {
			t.Errorf("failure cache holds %d entries, limit %d", n, maxFailures)
		}
	})
}

func TestCap(t *testing.T) {
	big := bytes.Repeat([]byte{0xAB}, MaxFileBytes+1)

	t.Run("exactly the cap is accepted", func(t *testing.T) {
		f, c := newFake(t)
		f.set(nil, serveBytes(big[:MaxFileBytes]))
		data, err := c.Fetch(context.Background(), "id")
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != MaxFileBytes {
			t.Errorf("len = %d, want %d", len(data), MaxFileBytes)
		}
	})

	t.Run("one byte over, with Content-Length", func(t *testing.T) {
		f, c := newFake(t)
		f.set(nil, serveBytes(big))
		_, err := c.Fetch(context.Background(), "id")
		e := asError(t, err)
		if !e.Permanent || !errors.Is(err, ErrPermanent) {
			t.Errorf("%+v", e)
		}
		_, err = c.Fetch(context.Background(), "id")
		if e := asError(t, err); !e.Cached || !e.Permanent {
			t.Errorf("not cached as permanent: %+v", e)
		}
		if d := f.downloads.Load(); d != 1 {
			t.Errorf("downloads = %d, want 1", d)
		}
	})

	t.Run("over the cap, chunked without Content-Length", func(t *testing.T) {
		f, c := newFake(t)
		var sent atomic.Int64
		f.set(nil, func(w http.ResponseWriter, _ *http.Request, _ string) {
			chunk := big[:1<<20]
			w.(http.Flusher).Flush() // forces chunked encoding: no Content-Length
			for range 64 {           // 64 MiB if the client kept reading
				n, err := w.Write(chunk)
				sent.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
		_, err := c.Fetch(context.Background(), "id")
		if e := asError(t, err); !e.Permanent {
			t.Errorf("%+v", e)
		}
		if n := sent.Load(); n >= 60<<20 {
			t.Errorf("the client read on past the cap: server sent %d MiB", n>>20)
		}
	})

	t.Run("getFile reports a size over the cap", func(t *testing.T) {
		f, c := newFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) {
			writeJSON(w, 200, map[string]any{"ok": true, "result": map[string]any{
				"file_size": MaxFileBytes + 1, "file_path": testPath}})
		}, nil)
		_, err := c.Fetch(context.Background(), "id")
		if e := asError(t, err); !e.Permanent {
			t.Errorf("%+v", e)
		}
		if d := f.downloads.Load(); d != 0 {
			t.Errorf("downloads = %d, want 0", d)
		}
	})

	t.Run("a huge getFile answer is not read whole", func(t *testing.T) {
		f, c := newFake(t)
		f.set(func(w http.ResponseWriter, _ *http.Request, _ string) {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"` + strings.Repeat("a", 1<<20) + `"}}`))
		}, nil)
		_, err := c.Fetch(context.Background(), "id")
		if e := asError(t, err); e.Permanent {
			t.Errorf("%+v", e)
		}
	})
}

func TestTimeouts(t *testing.T) {
	if c := New("https://api.telegram.org", testToken, nil); cap(c.sem) != 4 {
		t.Errorf("concurrency limit = %d, want 4", cap(c.sem))
	}
	if MaxFileBytes != 20*1024*1024 {
		t.Errorf("MaxFileBytes = %d", MaxFileBytes)
	}

	bubble(t, "getFile", func(t *testing.T) {
		f, c := newBubbleFake(t)
		f.set(func(_ http.ResponseWriter, r *http.Request, _ string) { <-r.Context().Done() }, nil)
		start := time.Now()
		_, err := c.Fetch(context.Background(), "id")
		e := asError(t, err)
		if e.Permanent || e.Status != 0 || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("%+v %v", e, err)
		}
		if d := time.Since(start); d != 10*time.Second {
			t.Errorf("getFile gave up after %v, want 10s", d)
		}
	})

	bubble(t, "download body", func(t *testing.T) {
		f, c := newBubbleFake(t)
		f.set(nil, func(w http.ResponseWriter, r *http.Request, _ string) {
			_, _ = w.Write([]byte("first part"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		start := time.Now()
		_, err := c.Fetch(context.Background(), "id")
		e := asError(t, err)
		if e.Permanent || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("%+v %v", e, err)
		}
		if d := time.Since(start); d != 30*time.Second {
			t.Errorf("the download gave up after %v, want 30s", d)
		}
	})
}

func TestSingleflight(t *testing.T) {
	synctest.Test(t, testSingleflight)
}

func testSingleflight(t *testing.T) {
	f, c := newBubbleFake(t)
	release := make(chan struct{})
	f.set(func(w http.ResponseWriter, r *http.Request, id string) {
		<-release
		okGetFile(testPath)(w, r, id)
	}, serveBytes([]byte("payload")))

	const callers = 25
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			data, err := c.Fetch(context.Background(), "same-id")
			if err == nil && string(data) != "payload" {
				err = fmt.Errorf("got %q", data)
			}
			errs <- err
		})
	}
	synctest.Wait() // every caller has joined the flight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if g, d := f.getFiles.Load(), f.downloads.Load(); g != 1 || d != 1 {
		t.Errorf("getFile = %d, downloads = %d, want 1 and 1 for %d concurrent callers", g, d, callers)
	}
}

// Concurrent callers of a failing file id each get their own *Error value.
func TestSingleflightFailure(t *testing.T) {
	synctest.Test(t, testSingleflightFailure)
}

func testSingleflightFailure(t *testing.T) {
	f, c := newBubbleFake(t)
	release := make(chan struct{})
	f.set(func(w http.ResponseWriter, _ *http.Request, _ string) {
		<-release
		apiError(w, 400, "Bad Request: wrong file_id", 0)
	}, nil)
	const callers = 10
	got := make([]*Error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			_, err := c.Fetch(context.Background(), "dead")
			got[i], _ = err.(*Error)
		})
	}
	synctest.Wait() // every caller has joined the flight
	close(release)
	wg.Wait()
	seen := map[*Error]bool{}
	for i, e := range got {
		if e == nil || !e.Permanent || e.Status != 400 {
			t.Fatalf("caller %d: %+v", i, e)
		}
		if seen[e] {
			t.Error("two callers share one *Error value")
		}
		seen[e] = true
	}
	if g := f.getFiles.Load(); g != 1 {
		t.Errorf("getFile calls = %d, want 1", g)
	}
}

func TestGlobalLimit(t *testing.T) {
	synctest.Test(t, testGlobalLimit)
}

func testGlobalLimit(t *testing.T) {
	f, c := newBubbleFake(t)
	var cur, peak atomic.Int64
	enter := func() {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	f.set(func(w http.ResponseWriter, r *http.Request, id string) {
		enter()
		time.Sleep(40 * time.Millisecond)
		cur.Add(-1)
		okGetFile(testPath)(w, r, id)
	}, func(w http.ResponseWriter, _ *http.Request, _ string) {
		enter()
		time.Sleep(40 * time.Millisecond)
		cur.Add(-1)
		_, _ = w.Write([]byte("x"))
	})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if _, err := c.Fetch(context.Background(), fmt.Sprint("id-", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	// On virtual time the peak is exact: the limit is reached and never passed.
	if p := peak.Load(); p != 4 {
		t.Errorf("%d Telegram requests ran at once, want exactly the limit of 4", p)
	}
	if g, d := f.getFiles.Load(), f.downloads.Load(); g != 16 || d != 16 {
		t.Errorf("getFile = %d, downloads = %d, want 16 and 16", g, d)
	}
}

// A caller that gives up neither fails the other callers nor poisons the
// failure cache.
func TestCallerCancel(t *testing.T) {
	synctest.Test(t, testCallerCancel)
}

func testCallerCancel(t *testing.T) {
	f, c := newBubbleFake(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.set(func(w http.ResponseWriter, r *http.Request, id string) {
		started <- struct{}{}
		<-release
		okGetFile(testPath)(w, r, id)
	}, serveBytes([]byte("payload")))

	ctx, cancel := context.WithCancel(context.Background())
	patient := make(chan error, 1)
	impatient := make(chan error, 1)
	go func() { _, err := c.Fetch(ctx, "id"); impatient <- err }()
	<-started
	go func() {
		data, err := c.Fetch(context.Background(), "id")
		if err == nil && string(data) != "payload" {
			err = errors.New("wrong data")
		}
		patient <- err
	}()
	synctest.Wait() // the patient caller has joined the flight
	cancel()
	err := <-impatient
	e := asError(t, err)
	if e.Permanent || e.Cached || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrTemporary) {
		t.Errorf("cancelled caller: %+v", e)
	}
	close(release)
	if err := <-patient; err != nil {
		t.Errorf("the other caller failed: %v", err)
	}
	if _, err := c.Fetch(context.Background(), "id"); err != nil {
		t.Errorf("fetch after a cancelled caller: %v", err)
	}

	// A context that is already over makes no request.
	before := f.getFiles.Load()
	_, err = c.Fetch(ctx, "other-id")
	if e := asError(t, err); !errors.Is(err, context.Canceled) || e.Permanent {
		t.Errorf("%+v", e)
	}
	if f.getFiles.Load() != before {
		t.Error("a request was made for a cancelled context")
	}
}

func TestBadConfiguration(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	for _, tc := range []struct{ base, token string }{
		{srv.URL, ""},
		{srv.URL, "no-colon"},
		{srv.URL, "123:abc/../../x"},
		{srv.URL, "123:abc?x"},
		{"", testToken},
		{"ftp://example.com", testToken},
		{"http://user:pw@" + strings.TrimPrefix(srv.URL, "http://"), testToken},
		{srv.URL + "?x=1", testToken},
		{"://bad", testToken},
	} {
		c := New(tc.base, tc.token, nil)
		_, err := c.Fetch(context.Background(), "id")
		e := asError(t, err)
		if e.Permanent {
			t.Errorf("New(%q, …): permanent", tc.base)
		}
		if tc.token != "" && strings.Contains(err.Error(), tc.token) {
			t.Errorf("error contains the token: %v", err)
		}
	}
	if hits.Load() != 0 {
		t.Error("a misconfigured client made a request")
	}
	c := New(srv.URL, testToken, nil)
	_, err := c.Fetch(context.Background(), "")
	if e := asError(t, err); !e.Permanent {
		t.Errorf("empty file id: %+v", e)
	}
	if hits.Load() != 0 {
		t.Error("an empty file id made a request")
	}
}

func TestRedact(t *testing.T) {
	const tok = "123456:AA/b+c d"
	cases := []struct {
		in      string
		secrets []string
		want    string
	}{
		{"plain", []string{testToken}, "plain"},
		{"a " + testToken + " b " + testToken, []string{testToken}, "a [redacted] b [redacted]"},
		{"https://api.telegram.org/bot" + testToken + "/getFile", []string{testToken}, "https://api.telegram.org/bot[redacted]/getFile"},
		{"x=" + url.QueryEscape(testToken), []string{testToken}, "x=[redacted]"},
		{"x=123456%3aTEST-token_abc", []string{testToken}, "x=[redacted]"},
		{url.QueryEscape(tok) + " " + url.PathEscape(tok) + " " + tok, []string{tok}, "[redacted] [redacted] [redacted]"},
		{"one two three", []string{"one", "", "three"}, "[redacted] two [redacted]"},
		{"nothing to do", nil, "nothing to do"},
		{"keep", []string{""}, "keep"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in, tc.secrets...); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	c := New("https://api.telegram.org", testToken, nil)
	if got := c.Redact("Post \"https://api.telegram.org/bot" + testToken + "/getFile\": EOF"); strings.Contains(got, testToken) || !strings.Contains(got, "[redacted]") {
		t.Errorf("Client.Redact = %q", got)
	}
}

func TestErrorType(t *testing.T) {
	perm := &Error{Permanent: true, Status: 400, msg: "getFile: HTTP 400"}
	temp := &Error{Status: 500, msg: "getFile: HTTP 500"}
	if !errors.Is(perm, ErrPermanent) || errors.Is(perm, ErrTemporary) || perm.Temporary() {
		t.Error("permanent error")
	}
	if !errors.Is(temp, ErrTemporary) || errors.Is(temp, ErrPermanent) || !temp.Temporary() {
		t.Error("temporary error")
	}
	if got := perm.Error(); got != "telegram: getFile: HTTP 400" {
		t.Errorf("Error() = %q", got)
	}
	cached := *perm
	cached.Cached = true
	if got := cached.Error(); !strings.Contains(got, "cached") {
		t.Errorf("Error() = %q", got)
	}
	wrapped := fmt.Errorf("thumbnail: %w", perm)
	if _, ok := errors.AsType[*Error](wrapped); !ok || !errors.Is(wrapped, ErrPermanent) {
		t.Error("wrapping loses the type")
	}
	var _ error = (*Error)(nil)
	var _ interface{ Temporary() bool } = (*Error)(nil)
	_ = io.Discard
}
