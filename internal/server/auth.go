package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Session cookie and login limits.
const (
	CookieName       = "gallery_session"
	sessionMessage   = "dalibot-gallery session v1"
	cookieMaxAge     = 31536000
	maxLoginBody     = 4 << 10
	loginWindow      = time.Minute
	loginPerClient   = 5
	loginOverall     = 30
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

// authenticated reports whether the request may use the protected routes.
func (s *Server) authenticated(r *http.Request) bool {
	if s.cfg.AuthOff {
		return true
	}
	if c, err := r.Cookie(CookieName); err == nil && equal(c.Value, s.session) {
		return true
	}
	if _, pass, ok := r.BasicAuth(); ok && equal(pass, s.cfg.Token) {
		return true
	}
	return false
}

// handleLogin serves GET and POST /login.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthOff {
		redirect(w, "/")
		return
	}
	if r.Method != http.MethodPost {
		if s.authenticated(r) {
			redirect(w, "/")
			return
		}
		s.loginPage(w, http.StatusOK, false)
		return
	}
	client := clientAddr(r)
	if wait := s.limiter.blocked(client); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait/time.Second)+1))
		plain(w, http.StatusTooManyRequests, "too many attempts; try again later\n")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		plain(w, http.StatusBadRequest, "bad request\n")
		return
	}
	if !equal(r.PostForm.Get("token"), s.cfg.Token) {
		s.limiter.fail(client)
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

// clientAddr is the address a request is counted under by the login limiter
// and shown in the access log. It is the peer address, except that when the
// peer is on a loopback or private network (a reverse proxy) the last entry of
// X-Forwarded-For is used: that entry is the one the proxy itself appended, so
// a client cannot choose it.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return host
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if fwd := net.ParseIP(last); fwd != nil {
		return fwd.String()
	}
	return host
}

// loginLimiter counts failed logins over a sliding minute, per client and overall.
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

// blocked returns how long the client must wait before another attempt, or 0.
func (l *loginLimiter) blocked(client string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-loginWindow)
	l.all = recent(l.all, cutoff)
	var wait time.Duration
	if len(l.all) >= loginOverall {
		wait = l.all[len(l.all)-loginOverall].Add(loginWindow).Sub(now)
	}
	if ts, ok := l.clients[client]; ok {
		ts = recent(ts, cutoff)
		if len(ts) == 0 {
			delete(l.clients, client)
		} else {
			l.clients[client] = ts
		}
		if len(ts) >= loginPerClient {
			wait = max(wait, ts[len(ts)-loginPerClient].Add(loginWindow).Sub(now))
		}
	}
	return wait
}

// fail records one failed attempt.
func (l *loginLimiter) fail(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.all = append(recent(l.all, now.Add(-loginWindow)), now)
	if len(l.clients) >= maxLimiterClient {
		// Bounded memory: forget everything but the overall count, which still limits attempts.
		l.clients = map[string][]time.Time{}
	}
	l.clients[client] = append(l.clients[client], now)
}
