// Package telegram fetches original image bytes from the Telegram Bot API.
//
// It makes exactly two kinds of request and refuses, in code, to make any
// other:
//
//	POST {apiBase}/bot{token}/getFile          (JSON body {"file_id": "..."})
//	GET  {apiBase}/file/bot{token}/{file_path}
//
// The bot that owns the token is live. Any other method (getUpdates,
// setWebhook, a send method, ...) could steal its updates or change it, so the
// allowlist is enforced twice: by the only two functions that build a URL, and
// by the HTTP transport, which rejects every request that is not one of the
// two shapes above.
//
// The token is a secret, and so are file ids, file paths and Telegram URLs.
// None of them ever appears in an error or a log line produced here.
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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/robdevops/dalibot-gallery/internal/source"
)

// MaxFileBytes is the download cap.
const MaxFileBytes = 20 << 20

const (
	getFileTimeout  = 10 * time.Second
	downloadTimeout = 30 * time.Second
	maxConcurrent   = 4 // concurrent Telegram fetches, process-wide per Client

	permanentTTL = 24 * time.Hour  // failure cache: HTTP 400, file over the cap
	temporaryTTL = 5 * time.Minute // failure cache: everything else

	// maxRetryWait bounds how long a fetch sleeps for a 429. A longer
	// retry_after fails at once (and is cached for that long).
	maxRetryWait = 30 * time.Second

	maxGetFileBody  = 64 << 10 // a getFile answer is a few hundred bytes
	maxFilePathLen  = 512
	maxFailures     = 20000 // entries in the failure cache before a sweep
	maxDescription  = 160
	redactedMark    = "[redacted]"
	getFileMethod   = "getFile"
	jsonContentType = "application/json"
)

// tokenPattern is the shape of a bot token (the same rule as internal/config).
// It guarantees the token is safe inside a URL path.
var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// Sentinels: errors.Is(err, ErrPermanent) / errors.Is(err, ErrTemporary).
var (
	ErrPermanent = errors.New("telegram: permanent failure")
	ErrTemporary = errors.New("telegram: temporary failure")
)

// Error is the only error type Fetch returns (context errors are wrapped in it
// as temporary).
type Error struct {
	Permanent  bool          // true: Telegram answered 400 (dead or unknown file_id), or the file is over the cap; cached 24 h
	Status     int           // HTTP status from Telegram, 0 if there was none
	RetryAfter time.Duration // from a 429, else 0
	Cached     bool          // true if this is a replay from the failure cache

	msg   string // already redacted
	cause error  // only ever a context error; never carries a URL
}

func (e *Error) Error() string {
	s := "telegram: " + e.msg
	if e.Cached {
		s += " (cached failure)"
	}
	return s
}

// Temporary reports whether the failure may go away: !Permanent.
func (e *Error) Temporary() bool { return !e.Permanent }

// Is matches the sentinel for e.Permanent.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrPermanent:
		return e.Permanent
	case ErrTemporary:
		return !e.Permanent
	}
	return false
}

// Unwrap returns the context error behind e, if there is one, so that
// errors.Is(err, context.Canceled) works. It never returns a transport error.
func (e *Error) Unwrap() error { return e.cause }

// Redact replaces every occurrence of each non-empty secret in s with
// "[redacted]". It also redacts the URL-escaped form of each secret.
func Redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		forms := []string{secret}
		for _, f := range []string{url.QueryEscape(secret), url.PathEscape(secret)} {
			forms = append(forms, f, lowerEscapes(f))
		}
		for _, f := range forms {
			if f != "" && strings.Contains(s, f) {
				s = strings.ReplaceAll(s, f, redactedMark)
			}
		}
	}
	return s
}

// lowerEscapes turns the hex digits of every %XX escape in s to lower case.
func lowerEscapes(s string) string {
	b := []byte(s)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' {
			b[i+1] = lowerHex(b[i+1])
			b[i+2] = lowerHex(b[i+2])
			i += 2
		}
	}
	return string(b)
}

func lowerHex(c byte) byte {
	if c >= 'A' && c <= 'F' {
		return c + ('a' - 'A')
	}
	return c
}

// Client fetches original image bytes from Telegram. Safe for concurrent use.
type Client struct {
	token   string
	base    *url.URL // nil if apiBase or the token is unusable
	baseStr string
	log     *slog.Logger
	http    *http.Client

	sem chan struct{}
	sf  singleflight.Group

	mu         sync.Mutex
	failures   map[string]failure // by file_id
	pauseUntil time.Time          // set by a 429: no request before this time
}

type failure struct {
	err   *Error
	until time.Time
}

// New returns a client. apiBase has no trailing slash (cfg.TelegramAPIBase).
// log may be nil.
//
// New makes no request. If apiBase or token is malformed (internal/config
// rejects both at start-up), every Fetch fails without a request.
func New(apiBase, token string, log *slog.Logger) *Client {
	var rt http.RoundTripper = http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t = t.Clone()
		t.MaxIdleConnsPerHost = maxConcurrent
		t.ResponseHeaderTimeout = downloadTimeout
		rt = t
	}
	return newClient(apiBase, token, log, rt)
}

// newClient is New with the transport that carries the allowed requests. Tests
// pass the transport of an in-memory server; nothing else differs from New.
func newClient(apiBase, token string, log *slog.Logger, rt http.RoundTripper) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Client{
		token:    token,
		baseStr:  strings.TrimRight(apiBase, "/"),
		log:      log,
		sem:      make(chan struct{}, maxConcurrent),
		failures: make(map[string]failure),
	}
	if u, err := url.Parse(c.baseStr); err == nil && tokenPattern.MatchString(token) &&
		(u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" {
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawPath = ""
		c.base = u
	}
	c.http = &http.Client{
		Transport: &guard{c: c, next: rt},
		// Never follow a redirect: neither endpoint redirects, and a
		// redirect could carry the request (and the token) elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

// Redact applies the package Redact with the client's own token.
func (c *Client) Redact(s string) string { return Redact(s, c.token) }

// ---- the allowlist ---------------------------------------------------------

var errNotAllowed = errors.New("telegram: request refused: not getFile or a file download")

// getFilePath and filePrefix are the only two URL paths this package knows.
func (c *Client) getFilePath() string { return c.base.Path + "/bot" + c.token + "/" + getFileMethod }
func (c *Client) filePrefix() string  { return c.base.Path + "/file/bot" + c.token + "/" }

// allowed reports whether a request is one of the two permitted shapes.
func (c *Client) allowed(method string, u *url.URL) bool {
	if c.base == nil || u == nil {
		return false
	}
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	switch method {
	case http.MethodPost:
		return u.Path == c.getFilePath()
	case http.MethodGet:
		rest, ok := strings.CutPrefix(u.Path, c.filePrefix())
		return ok && validFilePath(rest)
	}
	return false
}

// guard is the HTTP transport of the client. It refuses every request that is
// not allowed, whatever code built it.
type guard struct {
	c    *Client
	next http.RoundTripper
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if !g.c.allowed(req.Method, req.URL) {
		return nil, errNotAllowed
	}
	return g.next.RoundTrip(req)
}

// validFilePath accepts the relative paths Telegram returns ("photos/file_1.jpg")
// and nothing that could change which resource is requested.
func validFilePath(p string) bool {
	if p == "" || len(p) > maxFilePathLen {
		return false
	}
	for i := 0; i < len(p); i++ {
		switch ch := p[i]; {
		case ch < 0x20 || ch == 0x7f, ch == '\\', ch == '?', ch == '#', ch == '%':
			return false
		}
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// newGetFile builds the getFile request. With newDownload it is the only place
// a Telegram URL is made.
func (c *Client) newGetFile(ctx context.Context, fileID string) (*http.Request, error) {
	if c.base == nil {
		return nil, errNotAllowed
	}
	body, err := json.Marshal(struct {
		FileID string `json:"file_id"`
	}{fileID})
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = c.getFilePath()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", jsonContentType)
	req.Header.Set("Accept", jsonContentType)
	return req, nil
}

func (c *Client) newDownload(ctx context.Context, filePath string) (*http.Request, error) {
	if c.base == nil || !validFilePath(filePath) {
		return nil, errNotAllowed
	}
	u := *c.base
	u.Path = c.filePrefix() + filePath
	return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
}

// ---- Fetch -----------------------------------------------------------------

// Fetch returns the file's bytes (at most MaxFileBytes; a larger file is an
// error).
//
// Concurrent calls for one file_id share a single fetch. That fetch does not
// stop when one caller's ctx ends (the other callers still want it, and it is
// bounded by its own timeouts); the caller whose ctx ended returns at once
// with a temporary *Error wrapping the context error, which is not cached.
//
// Every error is a *Error and contains neither the token, a Telegram URL, the
// file_path nor the file_id.
func (c *Client) Fetch(ctx context.Context, fileID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxError(err)
	}
	if fileID == "" {
		return nil, &Error{Permanent: true, msg: "empty file id"}
	}
	if c.base == nil {
		return nil, &Error{msg: "client is not configured: bad API base or token"}
	}
	if e := c.cachedFailure(fileID); e != nil {
		return nil, e
	}
	ch := c.sf.DoChan(fileID, func() (any, error) {
		// A caller may have joined just after another flight for this
		// id failed: answer from the cache rather than ask again.
		if e := c.cachedFailure(fileID); e != nil {
			return nil, e
		}
		data, e := c.fetch(context.WithoutCancel(ctx), fileID)
		if e != nil {
			c.remember(fileID, e)
			return nil, e
		}
		return data, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctxError(ctx.Err())
	case r := <-ch:
		if r.Err != nil {
			e, ok := errors.AsType[*Error](r.Err)
			if !ok { // cannot happen: fetch returns only *Error
				return nil, &Error{msg: "internal error"}
			}
			cp := *e // callers share the flight, not the value
			return nil, &cp
		}
		return r.Val.([]byte), nil
	}
}

func ctxError(err error) *Error {
	msg := "cancelled"
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timed out"
	}
	return &Error{msg: msg, cause: err}
}

func (c *Client) cachedFailure(fileID string) *Error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.failures[fileID]
	if !ok {
		return nil
	}
	if !time.Now().Before(f.until) {
		delete(c.failures, fileID)
		return nil
	}
	cp := *f.err
	cp.Cached = true
	return &cp
}

func (c *Client) remember(fileID string, e *Error) {
	ttl := temporaryTTL
	if e.Permanent {
		ttl = permanentTTL
	} else if e.RetryAfter > ttl {
		ttl = min(e.RetryAfter, permanentTTL)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.failures) >= maxFailures {
		for k, f := range c.failures {
			if !now.Before(f.until) {
				delete(c.failures, k)
			}
		}
		for k := range c.failures { // still full: drop arbitrary entries
			if len(c.failures) < maxFailures {
				break
			}
			delete(c.failures, k)
		}
	}
	c.failures[fileID] = failure{err: e, until: now.Add(ttl)}
}

// fetch does the work of one flight: getFile, then the download, under the
// global limit. ctx is not the caller's; each request has its own timeout.
func (c *Client) fetch(ctx context.Context, fileID string) ([]byte, *Error) {
	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	start := time.Now()
	key := source.Key(fileID) // the public cache key: safe to log, unlike the file id
	fl := &flight{c: c, secrets: c.secrets(fileID)}

	data, e := fl.run(ctx, fileID)
	if e != nil {
		c.log.Warn("telegram fetch failed", "key", key, "status", e.Status,
			"permanent", e.Permanent, "retry_after", e.RetryAfter.String(), "error", e.msg)
		return nil, e
	}
	c.log.Debug("telegram fetch", "key", key, "bytes", len(data), "elapsed", time.Since(start).String())
	return data, nil
}

// secrets lists what must never appear in a message about fileID: the token,
// the file id, and the API base in every form a transport error may print it.
func (c *Client) secrets(fileID string) []string {
	s := []string{c.token, fileID, c.baseStr}
	if c.base != nil {
		s = append(s, c.base.Host, c.base.Hostname())
	}
	return s
}

// flight is the state of one fetch.
type flight struct {
	c       *Client
	secrets []string // redacted from every message
	retried bool     // the one 429 retry has been used
}

func (f *flight) redact(s string) string { return Redact(s, f.secrets...) }

func (f *flight) run(ctx context.Context, fileID string) ([]byte, *Error) {
	var filePath string
	if e := f.attempt(ctx, func() *Error {
		var e *Error
		filePath, e = f.getFile(ctx, fileID)
		return e
	}); e != nil {
		return nil, e
	}
	f.secrets = append(f.secrets, filePath)
	var data []byte
	if e := f.attempt(ctx, func() *Error {
		var e *Error
		data, e = f.download(ctx, filePath)
		return e
	}); e != nil {
		return nil, e
	}
	return data, nil
}

// attempt runs one request, waiting first if Telegram asked the client to slow
// down, and repeats it once after a 429 (once per fetch, not per request).
func (f *flight) attempt(ctx context.Context, do func() *Error) *Error {
	for {
		if e := f.c.waitPause(ctx); e != nil {
			return e
		}
		e := do()
		if e == nil || e.Status != http.StatusTooManyRequests {
			return e
		}
		f.c.pause(e.RetryAfter)
		if f.retried {
			return e
		}
		f.retried = true
	}
}

// pause records a 429: no request is sent for d.
func (c *Client) pause(d time.Duration) {
	until := time.Now().Add(d)
	c.mu.Lock()
	if until.After(c.pauseUntil) {
		c.pauseUntil = until
	}
	c.mu.Unlock()
}

// waitPause sleeps until a pause set by a 429 is over. A pause longer than
// maxRetryWait is not waited for: the fetch fails with the time left.
func (c *Client) waitPause(ctx context.Context) *Error {
	c.mu.Lock()
	d := time.Until(c.pauseUntil)
	c.mu.Unlock()
	if d <= 0 {
		return nil
	}
	if d > maxRetryWait {
		return &Error{Status: http.StatusTooManyRequests, RetryAfter: d,
			msg: fmt.Sprintf("rate limited; retry after %s", d.Round(time.Second))}
	}
	if err := sleepCtx(ctx, d); err != nil {
		return ctxError(err)
	}
	return nil
}

// sleepCtx waits for d or until ctx ends. An unreferenced timer is collected
// without a Stop (Go 1.23), so time.After leaves nothing behind when ctx wins.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// apiResponse is the envelope of every Bot API answer.
type apiResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Result      struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	} `json:"result"`
	Parameters struct {
		RetryAfter float64 `json:"retry_after"`
	} `json:"parameters"`
}

func (f *flight) getFile(ctx context.Context, fileID string) (string, *Error) {
	ctx, cancel := context.WithTimeout(ctx, getFileTimeout)
	defer cancel()
	req, err := f.c.newGetFile(ctx, fileID)
	if err != nil {
		return "", f.transportError("getFile", err)
	}
	resp, err := f.c.http.Do(req)
	if err != nil {
		return "", f.transportError("getFile", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGetFileBody))
	if err != nil {
		return "", f.transportError("getFile", err)
	}
	var api apiResponse
	parsed := json.Unmarshal(raw, &api) == nil

	if resp.StatusCode != http.StatusOK || !parsed || !api.OK {
		status := resp.StatusCode
		if status == http.StatusOK && parsed && api.ErrorCode != 0 {
			status = api.ErrorCode // an error envelope under a 200
		}
		if status == http.StatusOK {
			return "", &Error{Status: resp.StatusCode, msg: "getFile: unreadable answer"}
		}
		return "", f.statusError("getFile", status, resp.Header, &api)
	}
	if api.Result.FileSize > MaxFileBytes {
		return "", &Error{Permanent: true, Status: resp.StatusCode,
			msg: fmt.Sprintf("file is larger than the %d MiB limit", MaxFileBytes>>20)}
	}
	if !validFilePath(api.Result.FilePath) {
		return "", &Error{Status: resp.StatusCode, msg: "getFile: no usable file path in the answer"}
	}
	return api.Result.FilePath, nil
}

func (f *flight) download(ctx context.Context, filePath string) ([]byte, *Error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := f.c.newDownload(ctx, filePath)
	if err != nil {
		return nil, f.transportError("download", err)
	}
	resp, err := f.c.http.Do(req)
	if err != nil {
		return nil, f.transportError("download", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var api apiResponse
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxGetFileBody))
		_ = json.Unmarshal(raw, &api)
		return nil, f.statusError("download", resp.StatusCode, resp.Header, &api)
	}
	tooLarge := &Error{Permanent: true, Status: resp.StatusCode,
		msg: fmt.Sprintf("file is larger than the %d MiB limit", MaxFileBytes>>20)}
	if resp.ContentLength > MaxFileBytes {
		return nil, tooLarge
	}
	var buf bytes.Buffer
	if resp.ContentLength > 0 {
		buf.Grow(int(resp.ContentLength))
	}
	n, err := io.Copy(&buf, io.LimitReader(resp.Body, MaxFileBytes+1))
	if err != nil {
		return nil, f.transportError("download", err)
	}
	if n > MaxFileBytes {
		return nil, tooLarge
	}
	if n == 0 {
		return nil, &Error{Status: resp.StatusCode, msg: "download: empty file"}
	}
	return buf.Bytes(), nil
}

// statusError describes a non-success answer. Only 400 is permanent.
func (f *flight) statusError(op string, status int, h http.Header, api *apiResponse) *Error {
	e := &Error{Status: status, Permanent: status == http.StatusBadRequest}
	msg := fmt.Sprintf("%s: HTTP %d", op, status)
	if status == http.StatusTooManyRequests {
		e.RetryAfter = retryAfter(api.Parameters.RetryAfter, h.Get("Retry-After"))
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	if d := f.clean(api.Description); d != "" {
		msg += ": " + d
	}
	e.msg = msg
	return e
}

// retryAfter picks the wait from parameters.retry_after, else the Retry-After
// header (seconds), else one second. It is never less than a second or more
// than a day.
func retryAfter(param float64, header string) time.Duration {
	secs := param
	if !(secs > 0) {
		if n, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
			secs = float64(n)
		}
	}
	if !(secs >= 1) {
		secs = 1
	}
	if secs > 86400 {
		secs = 86400
	}
	d := time.Duration(secs * float64(time.Second))
	return d.Round(time.Millisecond)
}

// transportError turns an error from net/http into a redacted *Error. A
// *url.Error prints the whole request URL, token included, so only the error
// inside it is kept, and that is redacted too.
func (f *flight) transportError(op string, err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{msg: op + ": timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{msg: op + ": cancelled", cause: context.Canceled}
	}
	var ue *url.Error
	for errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		return &Error{msg: op + ": timed out"}
	}
	return &Error{msg: op + ": " + f.clean(err.Error())}
}

// clean makes text from outside (a Telegram description, a transport error)
// safe to keep: redacted, one line, bounded.
func (f *flight) clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(f.redact(s))
	if len(s) > maxDescription {
		s = strings.ToValidUTF8(s[:maxDescription], "") + "..."
	}
	return s
}
