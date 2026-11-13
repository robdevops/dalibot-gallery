package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/config"
	"github.com/robdevops/dalibot-gallery/internal/imaging"
	"github.com/robdevops/dalibot-gallery/internal/source"
	"github.com/robdevops/dalibot-gallery/web"
)

// The three set-ups the access model allows, as environment variables.
var (
	tokenAndAdmin  = map[string]string{"GALLERY_ADMIN_TOKEN": testAdminToken}
	publicOnly     = map[string]string{"GALLERY_TOKEN": "", "GALLERY_PUBLIC": "1"}
	publicAndAdmin = map[string]string{"GALLERY_TOKEN": "", "GALLERY_PUBLIC": "1", "GALLERY_ADMIN_TOKEN": testAdminToken}
)

func basicAdmin(r *http.Request) { r.SetBasicAuth("anyone", testAdminToken) }
func cookie(value string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: value}) }
}
func from(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }

// level is what the server makes of a request with these options.
func (e *env) level(opts ...reqOpt) access {
	e.t.Helper()
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "198.51.100.1:1"
	for _, o := range opts {
		o(r)
	}
	level, _ := e.srv.authorize(r)
	return level
}

func (e *env) login(token string, opts ...reqOpt) *http.Response {
	e.t.Helper()
	resp, _ := e.do("POST", "/login", strings.NewReader(url.Values{"token": {token}}.Encode()), opts...)
	return resp
}

const (
	adminFlag   = `data-admin`
	adminLink   = `<a href="/login">Admin</a>`
	signOutForm = `<form method="post" action="/logout">`
)

// page markers: is the page an admin's, does it link to the sign-in, does it offer to sign out.
func (e *env) index(opts ...reqOpt) (admin, link, signOut bool) {
	e.t.Helper()
	resp, body := e.get("/", opts...)
	if resp.StatusCode != 200 {
		e.t.Fatalf("GET /: %d", resp.StatusCode)
	}
	html := string(body)
	return strings.Contains(html, adminFlag), strings.Contains(html, adminLink), strings.Contains(html, signOutForm)
}

// A view session and an admin session are different cookie values, each an
// HMAC under its own token, and neither can stand in for the other.
func TestViewAndAdminSessions(t *testing.T) {
	e := newEnv(t, records(5), tokenAndAdmin)
	view, admin := sessionValue(testToken), adminSessionValue(testAdminToken)
	if view == admin || len(view) != 64 || len(admin) != 64 {
		t.Fatalf("session values: %q %q", view, admin)
	}

	// The one login form gives each token its own kind of session.
	resp := e.login(testToken)
	if resp.StatusCode != 303 || len(resp.Cookies()) != 1 || resp.Cookies()[0].Value != view {
		t.Fatalf("view token: %d %v", resp.StatusCode, resp.Cookies())
	}
	resp = e.login(testAdminToken)
	want := CookieName + "=" + admin + "; Path=/; Max-Age=31536000; HttpOnly; Secure; SameSite=Strict"
	if got := resp.Header.Get("Set-Cookie"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/" || got != want {
		t.Fatalf("admin token: %d, Set-Cookie %q", resp.StatusCode, got)
	}
	if resp := e.login("neither-token-0123456789"); resp.StatusCode != 401 || len(resp.Cookies()) != 0 {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}

	for name, c := range map[string]struct {
		opts []reqOpt
		want access
	}{
		"nothing":                    {nil, accessNone},
		"view cookie":                {[]reqOpt{cookie(view)}, accessView},
		"admin cookie":               {[]reqOpt{cookie(admin)}, accessAdmin},
		"view token as Basic":        {[]reqOpt{basic}, accessView},
		"admin token as Basic":       {[]reqOpt{basicAdmin}, accessAdmin},
		"view cookie + admin Basic":  {[]reqOpt{cookie(view), basicAdmin}, accessAdmin},
		"admin cookie + view Basic":  {[]reqOpt{cookie(admin), basic}, accessAdmin},
		"admin cookie + wrong Basic": {[]reqOpt{cookie(admin), func(r *http.Request) { r.SetBasicAuth("u", "wrong") }}, accessAdmin},
		"view cookie + wrong Basic":  {[]reqOpt{cookie(view), func(r *http.Request) { r.SetBasicAuth("u", "wrong") }}, accessView},
		// What someone who knows only the view token (or only a view cookie) can make.
		"admin message under the view token": {[]reqOpt{cookie(adminSessionValue(testToken))}, accessNone},
		"view message under the admin token": {[]reqOpt{cookie(sessionValue(testAdminToken))}, accessNone},
		"view cookie doubled":                {[]reqOpt{cookie(view + view)}, accessNone},
		"HMAC keyed with the view cookie":    {[]reqOpt{cookie(sessionMAC(view, adminSessionMessage))}, accessNone},
		"the tokens themselves as cookies":   {[]reqOpt{cookie(testAdminToken)}, accessNone},
		"view token as cookie":               {[]reqOpt{cookie(testToken)}, accessNone},
		"empty cookie":                       {[]reqOpt{cookie("")}, accessNone},
		"upper-case admin cookie":            {[]reqOpt{cookie(strings.ToUpper(admin))}, accessNone},
		"truncated admin cookie":             {[]reqOpt{cookie(admin[:63])}, accessNone},
		"session values as Basic passwords":  {[]reqOpt{func(r *http.Request) { r.SetBasicAuth("u", admin) }}, accessNone},
		"user name is not the password":      {[]reqOpt{func(r *http.Request) { r.SetBasicAuth(testAdminToken, "x") }}, accessNone},
	} {
		if got := e.level(append(c.opts, from("198.51.100."+fmt.Sprint(len(name))+":1"))...); got != c.want {
			t.Errorf("%s: access %d, want %d", name, got, c.want)
		}
	}

	// What the page shows each of them.
	for name, c := range map[string]struct {
		opt                  reqOpt
		admin, link, signOut bool
	}{
		"view cookie":  {cookie(view), false, true, true},
		"admin cookie": {cookie(admin), true, false, true},
		"view Basic":   {basic, false, true, false},
		"admin Basic":  {basicAdmin, true, false, false},
	} {
		if a, l, o := e.index(c.opt); a != c.admin || l != c.link || o != c.signOut {
			t.Errorf("%s: admin flag %v, admin link %v, sign out %v", name, a, l, o)
		}
	}
	// The flag is exactly the attribute the script will look for.
	if _, body := e.get("/", cookie(admin)); !strings.Contains(string(body), `<html lang="en" data-admin="1">`) {
		t.Error("the admin's page lacks <html data-admin>")
	}
	if _, body := e.get("/", cookie(view)); !strings.Contains(string(body), `<html lang="en">`) {
		t.Error("the viewer's page has a different <html> tag")
	}

	// Signing in again is how a view session becomes an admin one, so the
	// form stays reachable for a viewer; an admin has nothing more to gain.
	if resp, _ := e.get("/login", cookie(view)); resp.StatusCode != 200 {
		t.Errorf("GET /login with a view session: %d", resp.StatusCode)
	}
	if resp, _ := e.get("/login", cookie(admin)); resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Errorf("GET /login with an admin session: %d", resp.StatusCode)
	}
	// A Basic password is never evaluated on the open login page.
	if resp, _ := e.get("/login", basicAdmin); resp.StatusCode != 200 {
		t.Errorf("GET /login with Basic: %d", resp.StatusCode)
	}
	// Neither token nor either cookie value is logged.
	for _, s := range []string{view, admin, testToken, testAdminToken} {
		if strings.Contains(e.logs.String(), s) {
			t.Errorf("the log contains a token or a session value")
		}
	}
}

// Without an admin token nothing is an admin: not the view session, not a
// session made from an empty token, and the page does not mention admins.
func TestNoAdminTokenNoAdmin(t *testing.T) {
	for name, vars := range map[string]map[string]string{"token": nil, "public": publicOnly} {
		e := newEnv(t, records(3), vars)
		for what, opts := range map[string][]reqOpt{
			"view cookie":                     {cookie(sessionValue(testToken))},
			"admin session of the view token": {cookie(adminSessionValue(testToken))},
			"admin session of an empty token": {cookie(adminSessionValue(""))},
			"view session of an empty token":  {cookie(sessionValue(""))},
			"empty cookie":                    {cookie("")},
			"empty Basic password":            {func(r *http.Request) { r.SetBasicAuth("u", "") }},
			"the other test token as Basic":   {basicAdmin},
			"view token as Basic":             {basic},
		} {
			if got := e.level(opts...); got == accessAdmin {
				t.Errorf("%s gallery, %s: admin access without an admin token", name, what)
			}
		}
		opts := []reqOpt{basic}
		if name == "public" {
			opts = nil
		}
		if admin, link, _ := e.index(opts...); admin || link {
			t.Errorf("%s gallery: the page mentions admin (flag %v, link %v)", name, admin, link)
		}
	}
	// In public mode a made-up cookie neither helps nor hurts, and no sign-out is offered for it.
	e := newEnv(t, records(3), publicOnly)
	if _, _, signOut := e.index(cookie(sessionValue(""))); signOut {
		t.Error("public gallery: sign-out offered for a cookie that is no session")
	}
}

// One guess is a guess at both tokens: the limit is shared between them,
// between /login and Basic, and a blocked client is refused with either token.
func TestRateLimitCoversBothTokens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(2), tokenAndAdmin)
		const addr = "203.0.113.5:1000"
		wrongBasic := func(i int) reqOpt {
			return func(r *http.Request) { r.SetBasicAuth("u", fmt.Sprintf("a-wrong-guess-%d-0123456789", i)) }
		}
		// Five wrong tokens, some posted and some as Basic passwords.
		for i := range loginPerClient {
			if i%2 == 0 {
				if resp := e.login(fmt.Sprintf("wrong-guess-%d-0123456789", i), from(addr)); resp.StatusCode != 401 {
					t.Fatalf("wrong token %d: %d", i, resp.StatusCode)
				}
			} else if resp, _ := e.get("/api/tiles", from(addr), wrongBasic(i)); resp.StatusCode != 401 {
				t.Fatalf("wrong Basic password %d: %d", i, resp.StatusCode)
			}
		}
		for name, token := range map[string]string{"view": testToken, "admin": testAdminToken} {
			if resp := e.login(token, from(addr)); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || len(resp.Cookies()) != 0 {
				t.Errorf("blocked client, %s token at /login: %d", name, resp.StatusCode)
			}
			resp, _ := e.get("/api/tiles", from(addr), func(r *http.Request) { r.SetBasicAuth("u", token) })
			if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
				t.Errorf("blocked client, %s token as Basic: %d", name, resp.StatusCode)
			}
		}
		// A view session of the blocked client keeps viewing, but its admin
		// password is not evaluated: it stays a view session.
		view := cookie(sessionValue(testToken))
		if got := e.level(from(addr), view, basicAdmin); got != accessView {
			t.Errorf("blocked client with a view cookie and the admin password: access %d, want view", got)
		}
		if admin, _, _ := e.index(from(addr), view, basicAdmin); admin {
			t.Error("a blocked client became admin")
		}
		// Sessions are not guesses.
		if got := e.level(from(addr), cookie(adminSessionValue(testAdminToken))); got != accessAdmin {
			t.Errorf("admin cookie from a blocked address: access %d", got)
		}
		// Another client is unaffected; right tokens of either kind never count.
		for range 3 * loginPerClient {
			if e.level(from("203.0.113.6:1"), basicAdmin) != accessAdmin || e.level(from("203.0.113.6:1"), basic) != accessView {
				t.Fatal("right passwords were limited")
			}
		}
		// A wrong admin guess on top of a view session counts like any other.
		for i := range loginPerClient {
			e.level(from("203.0.113.7:1"), view, wrongBasic(i))
		}
		if resp := e.login(testAdminToken, from("203.0.113.7:2")); resp.StatusCode != 429 {
			t.Errorf("after %d wrong admin guesses behind a view cookie: %d", loginPerClient, resp.StatusCode)
		}
		time.Sleep(loginWindow + time.Second)
		if resp := e.login(testAdminToken, from(addr)); resp.StatusCode != 303 {
			t.Errorf("after the window: %d", resp.StatusCode)
		}
	})
}

// In a public gallery the admin token is still protected by the limiter, and
// a blocked guesser simply stays an ordinary visitor.
func TestRateLimitPublicAdmin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, records(2), publicAndAdmin)
		const addr = "203.0.113.5:1000"
		for i := range loginPerClient {
			resp, _ := e.get("/api/tiles", from(addr), func(r *http.Request) { r.SetBasicAuth("u", fmt.Sprintf("guess-%d", i)) })
			if resp.StatusCode != 200 {
				t.Fatalf("a wrong admin password must not stop viewing: %d", resp.StatusCode)
			}
		}
		if n := strings.Count(e.logs.String(), "basic auth failed"); n != loginPerClient {
			t.Errorf("%d failures logged, want %d", n, loginPerClient)
		}
		// Blocked: the right admin password is not evaluated. Viewing goes on.
		resp, body := e.get("/api/tiles", from(addr), basicAdmin)
		if resp.StatusCode != 200 || strings.Contains(string(body), `"id"`) {
			t.Errorf("blocked client with the admin password: %d, ids sent: %v", resp.StatusCode, strings.Contains(string(body), `"id"`))
		}
		if e.level(from(addr), basicAdmin) != accessView {
			t.Error("a blocked client became admin through Basic")
		}
		if resp := e.login(testAdminToken, from(addr)); resp.StatusCode != 429 || len(resp.Cookies()) != 0 {
			t.Errorf("a blocked client at /login: %d", resp.StatusCode)
		}
		// Blocked attempts are not recorded, so the block does not extend itself.
		if n := strings.Count(e.logs.String(), "basic auth failed"); n != loginPerClient {
			t.Errorf("%d failures logged after the block, want %d", n, loginPerClient)
		}
		time.Sleep(loginWindow + time.Second)
		if e.level(from(addr), basicAdmin) != accessAdmin {
			t.Error("after the window the admin password does not work")
		}
	})
}

// Public mode without an admin token: no login anywhere, images cacheable as public.
func TestPublic(t *testing.T) {
	e := newEnv(t, records(4), publicOnly)
	key := e.store.Snapshot().Keys()[0]
	for _, target := range []string{"/", "/api/tiles", "/api/tiles/new", "/app.js", "/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + imaging.Tag()} {
		resp, _ := e.get(target)
		if resp.StatusCode != 200 || len(resp.Cookies()) != 0 || resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s in a public gallery: %d", target, resp.StatusCode)
		}
	}
	// A Basic header is ignored, never counted as a guess: there is no token to guess.
	for range 3 * loginPerClient {
		if resp, _ := e.get("/api/tiles", func(r *http.Request) { r.SetBasicAuth("u", "whatever") }); resp.StatusCode != 200 {
			t.Fatalf("public with a Basic header: %d", resp.StatusCode)
		}
	}
	if strings.Contains(e.logs.String(), "basic auth failed") {
		t.Error("a Basic password was evaluated in a public gallery without an admin token")
	}
	// There is nothing to sign in to, so the routes do not exist.
	for _, target := range []string{"/login", "/logout"} {
		if resp, body := e.get(target); resp.StatusCode != 404 || strings.Contains(string(body), "<form") {
			t.Errorf("GET %s: %d, want 404", target, resp.StatusCode)
		}
		resp, _ := e.do("POST", target, strings.NewReader("token="+testToken))
		if resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD" || len(resp.Cookies()) != 0 {
			t.Errorf("POST %s: %d, Allow %q", target, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	_, body := e.get("/")
	for _, not := range []string{"/login", "/logout", "<footer", adminFlag, "Admin", "Sign out"} {
		if strings.Contains(string(body), not) {
			t.Errorf("the public page contains %q", not)
		}
	}
}

// Public mode with an admin token: anyone views, the admin signs in at
// /login, which the page links to, and can sign out again.
func TestPublicWithAdmin(t *testing.T) {
	e := newEnv(t, records(4), publicAndAdmin)
	admin := adminSessionValue(testAdminToken)

	if a, link, signOut := e.index(); a || !link || signOut {
		t.Errorf("anonymous visitor: admin flag %v, admin link %v, sign out %v", a, link, signOut)
	}
	resp, body := e.get("/login")
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Admin token") || !strings.Contains(string(body), `<a href="/">Back to the gallery</a>`) {
		t.Errorf("GET /login: %d", resp.StatusCode)
	}
	// There is no view token: nothing but the admin token signs in.
	for _, wrong := range []string{"", testToken, "wrong"} {
		if resp := e.login(wrong, from("198.51.100.9:1")); resp.StatusCode != 401 || len(resp.Cookies()) != 0 {
			t.Fatalf("token %q: %d %v", wrong, resp.StatusCode, resp.Cookies())
		}
	}
	resp = e.login(testAdminToken)
	if resp.StatusCode != 303 || len(resp.Cookies()) != 1 || resp.Cookies()[0].Value != admin {
		t.Fatalf("admin token: %d %v", resp.StatusCode, resp.Cookies())
	}
	if a, link, signOut := e.index(cookie(admin)); !a || link || !signOut {
		t.Errorf("admin: admin flag %v, admin link %v, sign out %v", a, link, signOut)
	}
	if a, _, _ := e.index(basicAdmin); !a {
		t.Error("the admin token as a Basic password is not admin")
	}
	for name, opt := range map[string]reqOpt{
		"a view session of an empty token":          cookie(sessionValue("")),
		"an admin session of an empty token":        cookie(adminSessionValue("")),
		"an empty cookie":                           cookie(""),
		"the view-style session of the admin token": cookie(sessionValue(testAdminToken)),
	} {
		if a, _, signOut := e.index(opt); a || signOut {
			t.Errorf("%s: admin flag %v, sign out %v", name, a, signOut)
		}
	}
}

func TestLogout(t *testing.T) {
	for name, vars := range map[string]map[string]string{"token": nil, "token and admin": tokenAndAdmin, "public with admin": publicAndAdmin} {
		e := newEnv(t, records(2), vars)
		session := adminSessionValue(testAdminToken)
		if name == "token" {
			session = sessionValue(testToken)
		}
		resp, _ := e.do("POST", "/logout", nil, cookie(session))
		want := CookieName + "=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict"
		if got := resp.Header.Get("Set-Cookie"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/" || got != want {
			t.Errorf("%s: POST /logout: %d to %q, Set-Cookie %q", name, resp.StatusCode, resp.Header.Get("Location"), got)
		}
		// It needs no session, and does nothing for a page of another site.
		if resp, _ := e.do("POST", "/logout", nil); resp.StatusCode != 303 {
			t.Errorf("%s: POST /logout without a session: %d", name, resp.StatusCode)
		}
		for _, opt := range []reqOpt{header("Sec-Fetch-Site", "cross-site"), header("Origin", "https://evil.example")} {
			if resp, _ := e.do("POST", "/logout", nil, cookie(session), opt); resp.StatusCode != 403 || resp.Header.Get("Set-Cookie") != "" {
				t.Errorf("%s: cross-origin POST /logout: %d", name, resp.StatusCode)
			}
		}
		if resp, _ := e.get("/logout", cookie(session)); resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" || resp.Header.Get("Set-Cookie") != "" {
			t.Errorf("%s: GET /logout: %d", name, resp.StatusCode)
		}
	}
}

// The cache header of an image in each mode: "public" only when anyone may view.
func TestImageCacheControl(t *testing.T) {
	for name, c := range map[string]struct {
		vars map[string]string
		opt  reqOpt
		want string
	}{
		"token":                    {nil, basic, immutablePrivate},
		"token, admin session":     {tokenAndAdmin, cookie(adminSessionValue(testAdminToken)), immutablePrivate},
		"token, view session":      {tokenAndAdmin, cookie(sessionValue(testToken)), immutablePrivate},
		"public":                   {publicOnly, func(*http.Request) {}, immutablePublic},
		"public with admin, anon":  {publicAndAdmin, func(*http.Request) {}, immutablePublic},
		"public with admin, admin": {publicAndAdmin, basicAdmin, immutablePublic},
	} {
		e := newEnv(t, records(2), c.vars)
		key := e.store.Snapshot().Keys()[0]
		for _, target := range []string{"/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + imaging.Tag()} {
			resp, _ := e.get(target, c.opt)
			if cc := resp.Header.Get("Cache-Control"); resp.StatusCode != 200 || cc != c.want {
				t.Errorf("%s: %s: %d, Cache-Control %q, want %q", name, target, resp.StatusCode, cc, c.want)
			}
			resp, _ = e.get(target, c.opt, header("If-None-Match", resp.Header.Get("ETag")))
			if cc := resp.Header.Get("Cache-Control"); resp.StatusCode != 304 || cc != c.want {
				t.Errorf("%s: %s revalidated: %d, Cache-Control %q", name, target, resp.StatusCode, cc)
			}
		}
		// Pages and the listing differ between visitors and are never stored.
		for _, target := range []string{"/", "/api/tiles", "/api/tiles/new"} {
			if resp, _ := e.get(target, c.opt); resp.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: %s: Cache-Control %q", name, target, resp.Header.Get("Cache-Control"))
			}
		}
	}
}

// GALLERY_TRUSTED_PROXIES says whose X-Forwarded-For to believe. Coming from
// a trusted proxy, or through one, grants nothing.
func TestTrustedProxyGrantsNothing(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"token":             {"GALLERY_TRUSTED_PROXIES": "10.0.0.0/8,127.0.0.1"},
		"token and admin":   {"GALLERY_TRUSTED_PROXIES": "10.0.0.0/8,127.0.0.1", "GALLERY_ADMIN_TOKEN": testAdminToken},
		"public with admin": {"GALLERY_TRUSTED_PROXIES": "10.0.0.0/8,127.0.0.1", "GALLERY_TOKEN": "", "GALLERY_PUBLIC": "1", "GALLERY_ADMIN_TOKEN": testAdminToken},
	} {
		e := newEnv(t, records(2), vars)
		want := accessNone
		if e.cfg.Public {
			want = accessView
		}
		for _, opts := range [][]reqOpt{
			{from("127.0.0.1:1")},
			{from("10.1.2.3:1")},
			{from("10.1.2.3:1"), header("X-Forwarded-For", "127.0.0.1")},
			{from("127.0.0.1:1"), header("X-Forwarded-For", "10.9.9.9, 127.0.0.1")},
			{from("10.1.2.3:1"), header("X-Forwarded-User", "admin"), header("X-Remote-User", "admin"), header("Remote-User", "admin")},
		} {
			if got := e.level(opts...); got != want {
				t.Errorf("%s: a request from a trusted proxy has access %d, want %d", name, got, want)
			}
		}
	}
}

// leakRecords have chat ids that cannot be mistaken for anything else, in
// every id shape the bot writes, with several records in the same second so
// that the id decides their order.
var leakChats = []string{"-1009876543210", "5550001234", "-4440009876"}

func leakRecords(n int) []source.Record {
	now := time.Now().Unix()
	out := make([]source.Record, 0, n)
	for i := range n {
		chat := leakChats[i%len(leakChats)]
		id := fmt.Sprintf("%s:%d", chat, 2000+i)
		if i%5 == 4 {
			id = fmt.Sprintf("%s:fAQADuniq%d", chat, i) // an imported picture
		}
		r := source.Record{
			ID: id, Prompt: fmt.Sprintf("prompt %d", i), Command: "dream", Current: "_",
			TTL:    now - int64(i/4) + 90*86400, // four records share each second
			Images: map[string]source.RawImage{"_": {FileID: fileID(i, "base"), Caption: fmt.Sprintf("caption %d", i)}},
		}
		if i%3 == 0 {
			r.Images["monet"] = source.RawImage{FileID: fileID(i, "monet")}
		}
		out = append(out, r)
	}
	return out
}

var reB64 = regexp.MustCompile(`[A-Za-z0-9_-]{16,}`)

// noChatID fails if text, or anything in it that decodes as base64, holds a chat id.
func noChatID(t *testing.T, what, text string) {
	t.Helper()
	check := func(s, how string) {
		for _, chat := range leakChats {
			digits := strings.TrimPrefix(chat, "-")
			if strings.Contains(s, digits) {
				t.Errorf("%s: chat id %s found%s", what, chat, how)
			}
		}
	}
	check(text, "")
	for _, word := range reB64.FindAllString(text, -1) {
		// Any alignment: a cursor could be embedded in a longer run.
		for off := range 4 {
			if off >= len(word) {
				break
			}
			w := word[off:]
			w = w[:len(w)-len(w)%4]
			if raw, err := base64.RawURLEncoding.DecodeString(w); err == nil {
				check(string(raw), " inside base64 "+word)
			}
		}
	}
}

// hasKey reports whether a decoded JSON value has an object key named key anywhere.
func hasKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == key || hasKey(val, key) {
				return true
			}
		}
	case []any:
		for _, val := range x {
			if hasKey(val, key) {
				return true
			}
		}
	}
	return false
}

// Nothing a public visitor receives holds a chat id: not the listing (no
// "id" at all), not a cursor, not the no-script page or its "Load more"
// link, not an error, not a header.
func TestPublicNeverLeaksChatID(t *testing.T) {
	for name, vars := range map[string]map[string]string{"public": publicOnly, "public with admin": publicAndAdmin} {
		recs := leakRecords(130)
		e := newEnv(t, recs, vars)
		snap := e.store.Snapshot()
		tag := imaging.Tag()

		// A cursor holds no chat id in any mode.
		raw, _ := base64.RawURLEncoding.DecodeString(snap.Newest(1)[0].Cursor())
		for _, chat := range []string{"9876543210", "5550001234", "4440009876"} {
			if strings.Contains(string(raw), chat) {
				t.Fatalf("a cursor holds a chat id: %q", raw)
			}
		}

		see := func(target string, opts ...reqOpt) (*http.Response, []byte) {
			t.Helper()
			resp, body := e.get(target, opts...)
			var hdr strings.Builder
			_ = resp.Header.Write(&hdr)
			noChatID(t, name+": "+target+" (headers)", hdr.String())
			noChatID(t, name+": "+target, string(body))
			if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
				var v any
				if err := json.Unmarshal(body, &v); err != nil {
					t.Fatalf("%s: %s: %v", name, target, err)
				}
				if hasKey(v, "id") {
					t.Errorf("%s: %s: the JSON has an \"id\"", name, target)
				}
			}
			return resp, body
		}

		// The JSON listing, page by page to the end, at several sizes.
		var since string
		for _, limit := range []int{1, 7, 60, 120} {
			target := fmt.Sprintf("/api/tiles?limit=%d", limit)
			seen := 0
			for pages := 0; ; pages++ {
				resp, body := see(target, gzipped)
				var p page
				if err := json.Unmarshal(body, &p); err != nil || resp.StatusCode != 200 {
					t.Fatalf("%s: %s: %d %v", name, target, resp.StatusCode, err)
				}
				for _, tile := range p.Tiles {
					if tile.ID != "" {
						t.Fatalf("%s: tile %s has an id", name, tile.Key)
					}
					if tile.Key == "" || tile.Label == "" || len(tile.Styles) == 0 || tile.Updated == 0 {
						t.Fatalf("%s: tile lost more than its id: %+v", name, tile)
					}
				}
				seen += len(p.Tiles)
				if p.Newest != nil {
					since = *p.Newest
				}
				if p.Next == nil || pages > 300 {
					break
				}
				target = fmt.Sprintf("/api/tiles?limit=%d&cursor=%s", limit, url.QueryEscape(*p.Next))
			}
			if seen != snap.Len() {
				t.Errorf("%s: limit %d: paged through %d of %d tiles", name, limit, seen, snap.Len())
			}
		}
		// The "new" poll and its tiles, with and without since.
		older := snap.Newest(30)[29].Cursor()
		for _, target := range []string{
			"/api/tiles/new", "/api/tiles/new?tiles=1", "/api/tiles/new?since=" + since, "/api/tiles/new?tiles=1&since=" + since,
			"/api/tiles/new?since=" + older, "/api/tiles/new?tiles=1&since=" + older,
		} {
			if resp, _ := see(target); resp.StatusCode != 200 {
				t.Errorf("%s: %s: %d", name, target, resp.StatusCode)
			}
		}
		_, body := see("/api/tiles/new?tiles=1&since=" + older)
		var p page
		if json.Unmarshal(body, &p); len(p.Tiles) == 0 {
			t.Errorf("%s: no new tiles to check", name)
		}

		// The page and its no-script "Load more" chain.
		target, pages := "/", 0
		for {
			_, body := see(target)
			m := regexp.MustCompile(`href="(/\?cursor=[^"]+)"`).FindSubmatch(body)
			if m == nil {
				break
			}
			target = string(m[1])
			pages++
		}
		if pages < 2 {
			t.Errorf("%s: the no-script chain has %d links", name, pages)
		}

		// Images, assets, the health check, errors and unknown paths.
		key := snap.Keys()[0]
		for _, target := range []string{
			"/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + tag, "/img/" + key + "-480.webp?v=" + tag,
			"/img/" + key + "-240.webp?v=0", "/img/AAAAAAAAAAAAAAAAAAAAAA.jpg",
			"/app.js", "/app.css", "/favicon.png", "/healthz", "/login", "/logout", "/nope", "/api/nope",
			"/api/tiles?cursor=AAAA", "/api/tiles/new?since=AAAA", "/?cursor=AAAA", "/api/tiles?limit=x",
		} {
			see(target)
		}

		// A cursor of the earlier format, which held the record id, is refused.
		old := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("1|%d|0|%s", snap.Newest(1)[0].Updated, "-1009876543210:1")))
		for _, target := range []string{"/api/tiles?cursor=", "/api/tiles/new?since=", "/api/tiles/new?tiles=1&since=", "/?cursor="} {
			if resp, _ := e.get(target + old); resp.StatusCode != 400 {
				t.Errorf("%s: %s<an old cursor>: %d, want 400", name, target, resp.StatusCode)
			}
		}

		// The same cursor names the same place every time (the page compares nothing, but links stay stable).
		_, b1 := e.get("/api/tiles?limit=7")
		_, b2 := e.get("/api/tiles?limit=7")
		if string(b1) != string(b2) {
			t.Errorf("%s: two identical requests gave different bodies", name)
		}
	}

	// An admin of a public gallery does get the id; so does everyone let into a token-protected one.
	withID := func(e *env, opts ...reqOpt) bool {
		_, body := e.get("/api/tiles?limit=3", opts...)
		var p page
		if err := json.Unmarshal(body, &p); err != nil || len(p.Tiles) == 0 {
			t.Fatalf("%v in %s", err, body)
		}
		return strings.HasPrefix(p.Tiles[0].ID, "-1009876543210:") || strings.HasPrefix(p.Tiles[0].ID, "5550001234:") || strings.HasPrefix(p.Tiles[0].ID, "-4440009876:")
	}
	pub := newEnv(t, leakRecords(20), publicAndAdmin)
	if withID(pub) || withID(pub, cookie(sessionValue(""))) || withID(pub, func(r *http.Request) { r.SetBasicAuth("u", "wrong-admin-guess") }) {
		t.Error("public gallery: a visitor got record ids")
	}
	if !withID(pub, basicAdmin) || !withID(pub, cookie(adminSessionValue(testAdminToken))) {
		t.Error("public gallery: the admin did not get record ids")
	}
	tok := newEnv(t, leakRecords(20), tokenAndAdmin)
	if !withID(tok, basic) || !withID(tok, basicAdmin) {
		t.Error("token-protected gallery: record ids are no longer sent")
	}
	// Cursors are sealed there too, so a shared "Load more" link carries no chat id.
	_, body := tok.get("/api/tiles?limit=3", basic)
	var p page
	_ = json.Unmarshal(body, &p)
	noChatID(t, "token-protected cursors", *p.Next+" "+*p.Newest)
	_, body = tok.get("/", basic)
	noChatID(t, "token-protected page", string(body))
}

func TestNewRejectsContradictoryAccess(t *testing.T) {
	e := newEnv(t, nil, nil)
	for name, change := range map[string]func(*config.Config){
		"neither public nor a token":   func(c *config.Config) { c.Token = "" },
		"public and a token":           func(c *config.Config) { c.Public = true },
		"admin token equals the token": func(c *config.Config) { c.AdminToken = c.Token },
	} {
		cfg := *e.cfg
		change(&cfg)
		if _, err := New(Deps{Config: &cfg, Store: e.store, Fetcher: e.fetcher, Cache: e.cache, Web: web.Files}); err == nil {
			t.Errorf("%s: New accepted the configuration", name)
		}
	}
}

// A proxy that does its own HTTP Basic sign-in forwards the visitor's
// Authorization header with every request. That password is a wrong guess
// at the gallery's tokens, but the same one each time: it is counted once,
// so browsing does not block the visitor, and the admin can still sign in
// at /login. Distinct wrong passwords are still each counted.
func TestProxyBasicPasswordDoesNotBlockAdminSignIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxyBasic := func(r *http.Request) { r.SetBasicAuth("family", "proxy-password-unrelated") }
		for name, vars := range map[string]map[string]string{"public": publicAndAdmin, "token": tokenAndAdmin} {
			e := newEnv(t, records(3), vars)
			key := e.store.Snapshot().Keys()[0]
			const addr = "203.0.113.20:1000"
			opts := []reqOpt{from(addr), proxyBasic}
			if name == "token" {
				opts = append(opts, cookie(sessionValue(testToken)))
			}
			targets := []string{"/", "/api/tiles", "/api/tiles/new", "/img/" + key + ".jpg", "/img/" + key + "-240.webp?v=" + imaging.Tag()}
			for i := range 4 * loginPerClient {
				target := targets[i%len(targets)]
				if resp, _ := e.get(target, opts...); resp.StatusCode != 200 {
					t.Fatalf("%s: request %d to %s with the proxy's password: %d", name, i, target, resp.StatusCode)
				}
			}
			if n := strings.Count(e.logs.String(), "basic auth failed"); n != 1 {
				t.Errorf("%s: %d failures logged for one repeated password, want 1", name, n)
			}
			resp := e.login(testAdminToken, opts...)
			if resp.StatusCode != 303 || len(resp.Cookies()) != 1 || resp.Cookies()[0].Value != adminSessionValue(testAdminToken) {
				t.Errorf("%s: admin sign-in behind a Basic proxy: %d", name, resp.StatusCode)
			}
			// Distinct wrong passwords from the same client still count, the
			// proxy's one included, and then block it.
			const guesser = "203.0.113.21:1"
			e.get("/api/tiles", from(guesser), proxyBasic)
			for i := range loginPerClient - 1 {
				e.get("/api/tiles", from(guesser), func(r *http.Request) { r.SetBasicAuth("u", fmt.Sprintf("guess-%d-0123456789", i)) })
			}
			if resp := e.login(testAdminToken, from(guesser)); resp.StatusCode != 429 {
				t.Errorf("%s: after %d distinct wrong passwords: %d, want 429", name, loginPerClient, resp.StatusCode)
			}
		}
	})
}
