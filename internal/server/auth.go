package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// Session cookie and limits on token guesses.
const (
	CookieName = "gallery_session"
	// The two kinds of session are HMACs of different messages under
	// different keys (the view token and the admin token), so neither can be
	// computed from the other, nor from the cookie of the other kind.
	sessionMessage      = "dalibot-gallery session v1"
	adminSessionMessage = "dalibot-gallery admin session v1"
	cookieMaxAge        = 31536000
	maxLoginBody        = 4 << 10
	loginWindow         = time.Minute
	// loginPerClient is how many wrong tokens one client (an IPv4 address or
	// an IPv6 /64) may present per window, at /login and as a Basic password.
	// A guess is a guess at both tokens at once and is counted once.
	loginPerClient = 5
	// loginOverall is the ceiling on wrong tokens from all clients together.
	// It only matters against a guesser with many addresses. It is far above
	// the per-client limit so that a few clients cannot lock everyone out:
	// reaching it takes sixty addresses failing at full rate.
	loginOverall     = 300
	maxLimiterClient = 10000
)

// access is what a request may do. The levels are ordered: an admin can
// also view.
type access int

const (
	accessNone  access = iota // must sign in first
	accessView                // may see the gallery
	accessAdmin               // may also administer it
)

func sessionMAC(token, message string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(message))
	return hex.EncodeToString(m.Sum(nil))
}

// sessionValue is the cookie value of a view session: lower-case hex of
// HMAC-SHA256(key = GALLERY_TOKEN, message = sessionMessage). The token
// itself never leaves the server.
func sessionValue(token string) string { return sessionMAC(token, sessionMessage) }

// adminSessionValue is the cookie value of an admin session: lower-case hex
// of HMAC-SHA256(key = GALLERY_ADMIN_TOKEN, message = adminSessionMessage).
func adminSessionValue(token string) string { return sessionMAC(token, adminSessionMessage) }

// equal compares two strings in constant time, whatever their lengths.
func equal(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return hmac.Equal(x[:], y[:])
}

// cookieAccess is the level the request's session cookie gives: admin, view
// or none. It never looks at a Basic password and never counts as a guess.
// A session that is not configured has the value "" and matches nothing.
func (s *Server) cookieAccess(r *http.Request) access {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return accessNone
	}
	// Both comparisons are always made.
	admin := equal(c.Value, s.adminSession) && s.adminSession != ""
	view := equal(c.Value, s.viewSession) && s.viewSession != ""
	switch {
	case admin:
		return accessAdmin
	case view:
		return accessView
	}
	return accessNone
}

// tokenAccess is the level a presented token gives. Both tokens are always
// compared, in constant time. A token that is not configured matches nothing.
func (s *Server) tokenAccess(token string) access {
	admin := equal(token, s.cfg.AdminToken) && s.cfg.AdminToken != ""
	view := equal(token, s.cfg.Token) && s.cfg.Token != ""
	switch {
	case admin:
		return accessAdmin
	case view:
		return accessView
	}
	return accessNone
}

// authorize reports what the request may do: accessNone, accessView or
// accessAdmin. Viewing is open to everyone in public mode; otherwise it
// needs a session cookie or the token as the Basic password. Admin needs the
// admin session cookie or the admin token as the Basic password, in either
// mode. Nothing else grants anything: not the peer address, not a header a
// proxy sets.
//
// A Basic password is a guess at both tokens and goes through the limiter,
// like a sign-in at /login, so neither token can be guessed faster through
// Basic. It is only looked at when it could raise the level (so never in a
// public gallery without an admin token, and not for an admin session).
// The same wrong password sent again by the same client within the window
// is not a new guess and is not counted again: a browser resends it with
// every request, and a proxy that does its own Basic sign-in forwards its
// password on every page and image. Counting each of those would block the
// client, /login included, after the first page.
//
// wait > 0 means the client is over the limit and its password was not
// evaluated, right or wrong. The level is then whatever the request has
// without the password: none (the caller answers 429) or, for a public
// visitor or a view session, view. Callers that need admin must treat
// wait > 0 as a refusal.
func (s *Server) authorize(r *http.Request) (level access, wait time.Duration) {
	level = s.cookieAccess(r)
	if s.cfg.Public && level < accessView {
		level = accessView
	}
	if level == accessAdmin || (level == accessView && !s.cfg.AdminEnabled()) {
		return level, 0
	}
	_, pass, has := r.BasicAuth()
	if !has {
		return level, 0
	}
	client := s.clientAddr(r)
	got := s.tokenAccess(pass)
	wait, ok, repeated := s.limiter.guess(limiterKey(client), got > accessNone, pass)
	if wait > 0 {
		return level, wait
	}
	if !ok {
		if !repeated {
			s.log.Warn("basic auth failed", "remote", client)
		}
		return level, 0
	}
	return max(level, got), 0
}

// tooMany answers 429 with Retry-After.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", fmt.Sprint(int(wait/time.Second)+1))
	plain(w, http.StatusTooManyRequests, "too many attempts; try again later\n")
}

// crossOrigin refuses POSTs that a browser marks as coming from another
// origin (Sec-Fetch-Site, or an Origin header whose host differs from Host).
// Requests with neither header, such as curl, are allowed.
var crossOrigin http.CrossOriginProtection

// handleLogin serves GET and POST /login. The route exists only when there
// is something to sign in to (cfg.LoginEnabled). One form takes either
// token: the view token gives a view session, the admin token an admin
// session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Only the cookie counts here: a Basic password is not evaluated on
		// this open page. A view session still sees the form when there is an
		// admin token, since signing in again is how it becomes an admin session.
		if have := s.cookieAccess(r); have == accessAdmin || (have == accessView && !s.cfg.AdminEnabled()) {
			redirect(w, "/")
			return
		}
		s.loginPage(w, http.StatusOK, false)
		return
	}
	// A sign-in posted by another site's page is refused before the body is
	// read and without using up the client's allowance of attempts.
	if err := crossOrigin.Check(r); err != nil {
		plain(w, http.StatusForbidden, "cross-origin request refused\n")
		return
	}
	// The body is read before the limiter is asked, and the limiter then
	// checks and records in one step. Checking first and recording after the
	// read would let one client hold many requests open past the check.
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		plain(w, http.StatusBadRequest, "bad request\n")
		return
	}
	client := s.clientAddr(r)
	got := s.tokenAccess(r.PostForm.Get("token"))
	wait, ok := s.limiter.attempt(limiterKey(client), got > accessNone)
	if wait > 0 {
		tooMany(w, wait)
		return
	}
	if !ok {
		s.log.Warn("login failed", "remote", client)
		s.loginPage(w, http.StatusUnauthorized, true)
		return
	}
	value := s.viewSession
	if got == accessAdmin {
		value = s.adminSession
		s.log.Info("admin signed in", "remote", client)
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", MaxAge: cookieMaxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	redirect(w, "/")
}

// handleLogout serves POST /logout: it drops the session cookie, view or
// admin, and goes back to the gallery (which, when viewing needs the token,
// leads to the login page). Like /login it exists only when cfg.LoginEnabled.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := crossOrigin.Check(r); err != nil {
		plain(w, http.StatusForbidden, "cross-origin request refused\n")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	redirect(w, "/")
}

// loginData is what login.html is executed with.
type loginData struct {
	Error bool // the token just posted was wrong
	// AdminOnly: the gallery is public, so the only thing to sign in for is admin.
	AdminOnly bool
}

func (s *Server) loginPage(w http.ResponseWriter, status int, failed bool) {
	s.html(w, status, s.login, loginData{Error: failed, AdminOnly: s.cfg.Public})
}

// clientAddr is the address a request is counted under by the limiter and
// shown in the access log. It is the peer address, unless the peer is one of
// the configured trusted proxies (GALLERY_TRUSTED_PROXIES; loopback by
// default). Then X-Forwarded-For is read from the right, skipping entries
// that are trusted proxies themselves: the first other entry was appended by
// a trusted proxy, so the client cannot choose it. A peer that is not a
// trusted proxy cannot choose its address at all.
func (s *Server) clientAddr(r *http.Request) string {
	return clientAddr(r, s.cfg.TrustedProxies)
}

func clientAddr(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap().WithZone("")
	if !isTrusted(peer, trusted) {
		return peer.String()
	}
	client := peer
	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(line, ",")...)
	}
	for _, entry := range slices.Backward(entries) {
		addr, err := netip.ParseAddr(strings.TrimSpace(entry))
		if err != nil {
			break // not an address: stop at the last hop that is known
		}
		client = addr.Unmap().WithZone("")
		if !isTrusted(client, trusted) {
			break
		}
	}
	return client.String()
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// limiterKey is the limiter's name for a client address: the address itself
// for IPv4, the /64 for IPv6 (one subscriber usually holds a whole /64).
func limiterKey(client string) string {
	addr, err := netip.ParseAddr(client)
	if err != nil || addr.Is4() {
		return client
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return client
	}
	return p.String()
}

// loginLimiter counts wrong tokens over a sliding minute, per client and overall.
type loginLimiter struct {
	mu      sync.Mutex
	clients map[string][]failure
	all     []time.Time
	// key keys the fingerprints of wrong Basic passwords, so that the
	// limiter's memory never holds a password or a plain hash of one.
	key []byte
}

// failure is one wrong token. guess is the keyed fingerprint of a wrong Basic
// password, or "" for a token posted at /login.
type failure struct {
	at    time.Time
	guess string
}

func newLoginLimiter() *loginLimiter {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err) // crypto/rand.Read does not fail on supported platforms
	}
	return &loginLimiter{clients: map[string][]failure{}, key: key}
}

// recent drops the entries older than the window.
func recent(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

func recentFailures(fs []failure, cutoff time.Time) []failure {
	i := 0
	for i < len(fs) && !fs[i].at.After(cutoff) {
		i++
	}
	return fs[i:]
}

// attempt judges one token posted at /login in a single step under the lock,
// so concurrent requests cannot all pass a check before any failure is
// recorded. correct says whether the token matched (compared by the caller in
// constant time). If the client is over a limit, wait is how long it must
// stay away and the attempt is refused even when correct, so that a blocked
// guesser learns nothing. Otherwise ok is correct, and a wrong token is
// recorded.
func (l *loginLimiter) attempt(client string, correct bool) (wait time.Duration, ok bool) {
	wait, ok, _ = l.judge(client, correct, "")
	return wait, ok
}

// guess is attempt for a Basic password. A wrong password that this client
// already presented within the window is refused without being recorded
// again (repeated is then true): it tells the client nothing new, and
// browsers and proxies resend the same password with every request.
func (l *loginLimiter) guess(client string, correct bool, password string) (wait time.Duration, ok, repeated bool) {
	m := hmac.New(sha256.New, l.key)
	m.Write([]byte(password))
	return l.judge(client, correct, string(m.Sum(nil)[:16]))
}

func (l *loginLimiter) judge(client string, correct bool, guess string) (wait time.Duration, ok, repeated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-loginWindow)
	l.all = recent(l.all, cutoff)
	if len(l.all) >= loginOverall {
		wait = l.all[len(l.all)-loginOverall].Add(loginWindow).Sub(now)
	}
	fs := recentFailures(l.clients[client], cutoff)
	if len(fs) >= loginPerClient {
		wait = max(wait, fs[len(fs)-loginPerClient].at.Add(loginWindow).Sub(now))
	}
	if !correct && wait == 0 && guess != "" {
		repeated = slices.ContainsFunc(fs, func(f failure) bool { return f.guess == guess })
	}
	if wait > 0 || correct || repeated {
		if len(fs) == 0 {
			delete(l.clients, client)
		} else {
			l.clients[client] = fs
		}
		return wait, wait == 0 && correct, repeated
	}
	l.all = append(l.all, now)
	if len(l.clients) >= maxLimiterClient {
		// Bounded memory: forget everything but the overall count, which still limits attempts.
		l.clients = map[string][]failure{}
	}
	l.clients[client] = append(fs, failure{at: now, guess: guess})
	return 0, false, false
}
