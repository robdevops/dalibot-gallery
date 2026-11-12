package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Session cookie and limits on token guesses.
const (
	CookieName     = "gallery_session"
	sessionMessage = "dalibot-gallery session v1"
	cookieMaxAge   = 31536000
	maxLoginBody   = 4 << 10
	loginWindow    = time.Minute
	// loginPerClient is how many wrong tokens one client (an IPv4 address or
	// an IPv6 /64) may present per window, at /login and as a Basic password.
	loginPerClient = 5
	// loginOverall is the ceiling on wrong tokens from all clients together.
	// It only matters against a guesser with many addresses. It is far above
	// the per-client limit so that a few clients cannot lock everyone out:
	// reaching it takes sixty addresses failing at full rate.
	loginOverall     = 300
	maxLimiterClient = 10000
)

// sessionValue is the cookie value for a token: lower-case hex of
// HMAC-SHA256(key = token, message = sessionMessage). The token itself never
// leaves the server.
func sessionValue(token string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(sessionMessage))
	return hex.EncodeToString(m.Sum(nil))
}

// equal compares two strings in constant time, whatever their lengths.
func equal(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return hmac.Equal(x[:], y[:])
}

// authorize reports whether the request may use the protected routes. A
// wrong Basic password counts against the same limiter as a failed login, so
// the token cannot be guessed faster through Basic than through /login. When
// the client is over the limit, wait is how long it must stay away, and the
// request is refused whatever password it carries.
func (s *Server) authorize(r *http.Request) (ok bool, wait time.Duration) {
	if s.cfg.AuthOff {
		return true, 0
	}
	if c, err := r.Cookie(CookieName); err == nil && equal(c.Value, s.session) {
		return true, 0
	}
	if _, pass, has := r.BasicAuth(); has {
		client := s.clientAddr(r)
		wait, ok := s.limiter.attempt(limiterKey(client), equal(pass, s.cfg.Token))
		if !ok && wait == 0 {
			s.log.Warn("basic auth failed", "remote", client)
		}
		return ok, wait
	}
	return false, 0
}

// tooMany answers 429 with Retry-After.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", fmt.Sprint(int(wait/time.Second)+1))
	plain(w, http.StatusTooManyRequests, "too many attempts; try again later\n")
}

// handleLogin serves GET and POST /login.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthOff {
		redirect(w, "/")
		return
	}
	if r.Method != http.MethodPost {
		// Only the cookie counts here: a Basic password is not evaluated on this open page.
		if c, err := r.Cookie(CookieName); err == nil && equal(c.Value, s.session) {
			redirect(w, "/")
			return
		}
		s.loginPage(w, http.StatusOK, false)
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
	wait, ok := s.limiter.attempt(limiterKey(client), equal(r.PostForm.Get("token"), s.cfg.Token))
	if wait > 0 {
		tooMany(w, wait)
		return
	}
	if !ok {
		s.log.Warn("login failed", "remote", client)
		s.loginPage(w, http.StatusUnauthorized, true)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: s.session, Path: "/", MaxAge: cookieMaxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	redirect(w, "/")
}

func (s *Server) loginPage(w http.ResponseWriter, status int, failed bool) {
	s.html(w, status, s.login, struct{ Error bool }{failed})
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
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
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
	now     func() time.Time
	mu      sync.Mutex
	clients map[string][]time.Time
	all     []time.Time
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, clients: map[string][]time.Time{}}
}

// recent drops the entries older than the window.
func recent(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// attempt judges one presented token in a single step under the lock, so
// concurrent requests cannot all pass a check before any failure is recorded.
// correct says whether the token matched (compared by the caller in constant
// time). If the client is over a limit, wait is how long it must stay away
// and the attempt is refused even when correct, so that a blocked guesser
// learns nothing. Otherwise ok is correct, and a wrong token is recorded.
func (l *loginLimiter) attempt(client string, correct bool) (wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-loginWindow)
	l.all = recent(l.all, cutoff)
	if len(l.all) >= loginOverall {
		wait = l.all[len(l.all)-loginOverall].Add(loginWindow).Sub(now)
	}
	ts := recent(l.clients[client], cutoff)
	if len(ts) >= loginPerClient {
		wait = max(wait, ts[len(ts)-loginPerClient].Add(loginWindow).Sub(now))
	}
	if wait > 0 || correct {
		if len(ts) == 0 {
			delete(l.clients, client)
		} else {
			l.clients[client] = ts
		}
		return wait, wait == 0
	}
	l.all = append(l.all, now)
	if len(l.clients) >= maxLimiterClient {
		// Bounded memory: forget everything but the overall count, which still limits attempts.
		l.clients = map[string][]time.Time{}
	}
	l.clients[client] = append(ts, now)
	return 0, false
}
