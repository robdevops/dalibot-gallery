package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	testToken    = "correct-horse-battery-staple"
	testBotToken = "123456789:AAH-secret_bot_token_value_0123456789"
)

func env(kv ...string) func(string) string {
	m := map[string]string{"GALLERY_TOKEN": testToken, "TELEGRAM_BOT_TOKEN": testBotToken}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env())
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("./cache")
	want := Config{
		Listen: "127.0.0.1:8080", Token: testToken, Table: "dalibot", Region: "ap-southeast-1",
		TelegramToken: testBotToken, TelegramAPIBase: "https://api.telegram.org", StoreDays: 90,
		DefaultCommand: "dream", Refresh: 60 * time.Second, CacheDir: abs, CacheMaxMB: 2048,
		WebPQuality: 76, ImageWorkers: runtime.GOMAXPROCS(0), Warm: 120,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	}
	if fmt.Sprintf("%#v|%q|%q", *c, c.Token, c.TelegramToken) != fmt.Sprintf("%#v|%q|%q", want, want.Token, want.TelegramToken) {
		t.Errorf("defaults:\n got %#v\nwant %#v", *c, want)
	}
	if c.Chats != nil || c.Public || c.AdminToken != "" || c.AdminEnabled() || !c.LoginEnabled() {
		t.Errorf("chats/access defaults wrong: %v public=%v admin=%v", c.Chats, c.Public, c.AdminEnabled())
	}
	if c.CacheMaxBytes() != 2048<<20 {
		t.Errorf("CacheMaxBytes = %d", c.CacheMaxBytes())
	}
}

func TestEveryVariable(t *testing.T) {
	c, err := FromEnv(env(
		"GALLERY_LISTEN", "0.0.0.0:9000",
		"DALIBOT_TABLE", "my.table_1",
		"AWS_REGION", "ap-southeast-2",
		"TELEGRAM_API_BASE", "http://127.0.0.1:8081/",
		"GALLERY_STORE_DAYS", "30",
		"GALLERY_DEFAULT_COMMAND", "/flux",
		"GALLERY_CHATS", " -1001234567890, 42 ,,42",
		"GALLERY_REFRESH", "2m",
		"GALLERY_CACHE_DIR", "/var/cache/x",
		"GALLERY_CACHE_MAX_MB", "100",
		"GALLERY_WEBP_QUALITY", "80",
		"GALLERY_IMAGE_WORKERS", "2",
		"GALLERY_WARM", "0",
	))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:9000" || c.Table != "my.table_1" || c.Region != "ap-southeast-2" ||
		c.TelegramAPIBase != "http://127.0.0.1:8081" || c.StoreDays != 30 || c.DefaultCommand != "flux" ||
		strings.Join(c.Chats, "|") != "-1001234567890|42" || c.Refresh != 2*time.Minute ||
		c.CacheDir != "/var/cache/x" || c.CacheMaxMB != 100 || c.WebPQuality != 80 || c.ImageWorkers != 2 || c.Warm != 0 {
		t.Errorf("unexpected config: %v chats=%v", c, c.Chats)
	}
}

func TestRegionAndCacheFallbacks(t *testing.T) {
	c, err := FromEnv(env("AWS_DEFAULT_REGION", "eu-west-1", "CACHE_DIRECTORY", "/var/cache/dalibot-gallery:/var/cache/other", "GALLERY_REFRESH", "90"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Region != "eu-west-1" || c.CacheDir != "/var/cache/dalibot-gallery" || c.Refresh != 90*time.Second {
		t.Errorf("fallbacks wrong: %v", c)
	}
	c, err = FromEnv(env("AWS_DEFAULT_REGION", "eu-west-1", "AWS_REGION", "us-west-2", "CACHE_DIRECTORY", "/a", "GALLERY_CACHE_DIR", "/b"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Region != "us-west-2" || c.CacheDir != "/b" {
		t.Errorf("explicit values must win: %v", c)
	}
}

const testAdminToken = "unit-test-admin-token-0123456789"

// Who may view and who may administer: every combination of GALLERY_PUBLIC,
// GALLERY_TOKEN and GALLERY_ADMIN_TOKEN.
func TestAccessRules(t *testing.T) {
	for _, c := range []struct {
		name                 string
		public, token, admin string
		ok                   bool
		wantPublic           bool
		wantLogin, wantAdmin bool
		mentions             []string // in the error
	}{
		{name: "token only", token: testToken, ok: true, wantLogin: true},
		{name: "token and admin token", token: testToken, admin: testAdminToken, ok: true, wantLogin: true, wantAdmin: true},
		{name: "public only", public: "1", ok: true, wantPublic: true},
		{name: "public with an admin token", public: "1", admin: testAdminToken, ok: true, wantPublic: true, wantLogin: true, wantAdmin: true},
		{name: "GALLERY_PUBLIC=0 is unset", public: "0", token: testToken, ok: true, wantLogin: true},
		// Never open by accident.
		{name: "nothing", mentions: []string{"GALLERY_TOKEN", "is required", "GALLERY_PUBLIC=1"}},
		{name: "admin token alone", admin: testAdminToken, mentions: []string{"GALLERY_TOKEN", "is required", "GALLERY_PUBLIC=1"}},
		{name: "GALLERY_PUBLIC=0 alone", public: "0", mentions: []string{"GALLERY_TOKEN", "is required"}},
		// Contradictory.
		{name: "public and token", public: "1", token: testToken, mentions: []string{"GALLERY_PUBLIC", "GALLERY_TOKEN", "contradicts"}},
		{name: "public, token and admin token", public: "1", token: testToken, admin: testAdminToken, mentions: []string{"GALLERY_PUBLIC", "contradicts"}},
		// The admin token is a different, equally long token.
		{name: "admin token equal to the token", token: testToken, admin: testToken, mentions: []string{"GALLERY_ADMIN_TOKEN", "must differ"}},
		{name: "short admin token", token: testToken, admin: "hunter2-admin", mentions: []string{"GALLERY_ADMIN_TOKEN", "at least 16"}},
		{name: "short admin token, public", public: "1", admin: "hunter2-admin", mentions: []string{"GALLERY_ADMIN_TOKEN", "at least 16"}},
		{name: "padded admin token", token: testToken, admin: " " + testAdminToken, mentions: []string{"GALLERY_ADMIN_TOKEN", "space"}},
		{name: "admin token with a line break", token: testToken, admin: testAdminToken + "\nmore", mentions: []string{"GALLERY_ADMIN_TOKEN", "control"}},
		{name: "short token", token: "hunter2", mentions: []string{"GALLERY_TOKEN", "at least 16"}},
		{name: "padded token", token: " padded-token-0123456789 ", mentions: []string{"GALLERY_TOKEN", "space"}},
		// Only 1 turns public viewing on; nothing else is guessed at.
		{name: "GALLERY_PUBLIC=true", public: "true", token: testToken, mentions: []string{"GALLERY_PUBLIC", "must be 1"}},
		{name: "GALLERY_PUBLIC=yes without a token", public: "yes", mentions: []string{"GALLERY_PUBLIC", "must be 1", "GALLERY_TOKEN"}},
	} {
		cfg, err := FromEnv(env("GALLERY_PUBLIC", c.public, "GALLERY_TOKEN", c.token, "GALLERY_ADMIN_TOKEN", c.admin))
		if !c.ok {
			if err == nil {
				t.Errorf("%s: accepted", c.name)
				continue
			}
			for _, m := range c.mentions {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("%s: the error does not mention %q: %v", c.name, m, err)
				}
			}
			for _, secret := range []string{testToken, testAdminToken, "hunter2", "padded-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("%s: the error echoes a token: %v", c.name, err)
				}
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if cfg.Public != c.wantPublic || cfg.LoginEnabled() != c.wantLogin || cfg.AdminEnabled() != c.wantAdmin ||
			cfg.Token != c.token || cfg.AdminToken != c.admin || cfg.Public == (cfg.Token != "") {
			t.Errorf("%s: public=%v login=%v admin=%v", c.name, cfg.Public, cfg.LoginEnabled(), cfg.AdminEnabled())
		}
	}
}

// GALLERY_AUTH is gone. Whatever it is set to, and whatever else is set, the
// gallery refuses to start and says what replaces it, so that a deployment
// that relied on it never comes up with a different meaning.
func TestGalleryAuthRefused(t *testing.T) {
	for _, v := range []string{"off", "OFF", "token", "none", "false", "0", "no", "1", "x"} {
		for name, vars := range map[string][]string{
			"old proxy setup":       {"GALLERY_TOKEN", ""},
			"with the token":        nil,
			"with public":           {"GALLERY_TOKEN", "", "GALLERY_PUBLIC", "1"},
			"with public and admin": {"GALLERY_TOKEN", "", "GALLERY_PUBLIC", "1", "GALLERY_ADMIN_TOKEN", testAdminToken},
		} {
			_, err := FromEnv(env(append([]string{"GALLERY_AUTH", v}, vars...)...))
			if err == nil {
				t.Errorf("GALLERY_AUTH=%s (%s) was accepted", v, name)
				continue
			}
			for _, want := range []string{"GALLERY_AUTH: is no longer supported", "GALLERY_PUBLIC=1"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("GALLERY_AUTH=%s (%s): the error lacks %q: %v", v, name, want, err)
				}
			}
		}
	}
	// Empty or blank counts as unset.
	for _, v := range []string{"", "  "} {
		if _, err := FromEnv(env("GALLERY_AUTH", v)); err != nil {
			t.Errorf("GALLERY_AUTH=%q: %v", v, err)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	cases := []struct{ name, value string }{
		{"GALLERY_LISTEN", "8080"},
		{"GALLERY_LISTEN", "127.0.0.1:0"},
		{"GALLERY_LISTEN", "127.0.0.1:http"},
		{"GALLERY_LISTEN", "127.0.0.1:99999"},
		{"DALIBOT_TABLE", "a"},
		{"DALIBOT_TABLE", "bad table"},
		{"AWS_REGION", "Sydney"},
		{"AWS_REGION", "us-east-2; rm"},
		{"TELEGRAM_BOT_TOKEN", ""},
		{"TELEGRAM_BOT_TOKEN", "not a token"},
		{"TELEGRAM_BOT_TOKEN", "123:abc/../x"},
		{"TELEGRAM_API_BASE", "api.telegram.org"},
		{"TELEGRAM_API_BASE", "ftp://api.telegram.org"},
		{"TELEGRAM_API_BASE", "https://user:pw@api.telegram.org"},
		{"TELEGRAM_API_BASE", "https://api.telegram.org/?x=1"},
		{"GALLERY_STORE_DAYS", "0"},
		{"GALLERY_STORE_DAYS", "ninety"},
		{"GALLERY_STORE_DAYS", "90.5"},
		{"GALLERY_DEFAULT_COMMAND", "Dream!"},
		{"GALLERY_DEFAULT_COMMAND", "/"},
		{"GALLERY_CHATS", "-100123,abc"},
		{"GALLERY_CHATS", "@channel"},
		{"GALLERY_REFRESH", "1s"},
		{"GALLERY_REFRESH", "soon"},
		{"GALLERY_REFRESH", "48h"},
		{"GALLERY_REFRESH", "-60s"},
		{"GALLERY_CACHE_DIR", "/"},
		{"GALLERY_CACHE_MAX_MB", "1"},
		{"GALLERY_CACHE_MAX_MB", "lots"},
		{"GALLERY_WEBP_QUALITY", "0"},
		{"GALLERY_WEBP_QUALITY", "101"},
		{"GALLERY_IMAGE_WORKERS", "0"},
		{"GALLERY_IMAGE_WORKERS", "1000"},
		{"GALLERY_WARM", "-1"},
		{"GALLERY_TRUSTED_PROXIES", "proxy.example.com"},
		{"GALLERY_TRUSTED_PROXIES", "10.0.0.0/33"},
		{"GALLERY_TRUSTED_PROXIES", "0.0.0.0/0"},
		{"GALLERY_TRUSTED_PROXIES", "::/0"},
		{"GALLERY_TRUSTED_PROXIES", "172.18.0.1, yes"},
	}
	for _, tc := range cases {
		c, err := FromEnv(env(tc.name, tc.value))
		if err == nil {
			t.Errorf("%s=%q: expected an error, got %v", tc.name, tc.value, c)
			continue
		}
		if c != nil {
			t.Errorf("%s=%q: a config was returned with an error", tc.name, tc.value)
		}
		if !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error does not name the variable: %v", tc.name, tc.value, err)
		}
	}
}

func TestAllProblemsReportedAtOnce(t *testing.T) {
	_, err := FromEnv(env("GALLERY_LISTEN", "nope", "GALLERY_WARM", "x", "GALLERY_REFRESH", "x", "TELEGRAM_BOT_TOKEN", "bad token "+testToken))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"GALLERY_LISTEN", "GALLERY_WARM", "GALLERY_REFRESH", "TELEGRAM_BOT_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

// Secrets must not appear in error messages, even when the secret itself is what is wrong.
func TestErrorsNeverContainSecrets(t *testing.T) {
	badBot := "999:has spaces and $ecrets"
	cases := [][]string{
		{"TELEGRAM_BOT_TOKEN", badBot},
		{"GALLERY_TOKEN", "short-secret"},
		{"GALLERY_TOKEN", "line\nbreak-secret-0123456789"},
		{"GALLERY_AUTH", "off"},
		{"GALLERY_PUBLIC", "1"},
		{"GALLERY_ADMIN_TOKEN", "short-admin"},
		{"GALLERY_ADMIN_TOKEN", " padded-admin-token-0123456789"},
		{"GALLERY_ADMIN_TOKEN", testToken},
		{"GALLERY_LISTEN", "bad"},
	}
	for _, kv := range cases {
		_, err := FromEnv(env(kv...))
		if err == nil {
			t.Errorf("%v: expected an error", kv[0])
			continue
		}
		for _, secret := range []string{testToken, testBotToken, badBot, "short-secret", "break-secret", "short-admin", "padded-admin"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error leaks a secret: %v", kv[0], err)
			}
		}
	}
}

func TestPrintingNeverShowsSecrets(t *testing.T) {
	c, err := FromEnv(env("GALLERY_CHATS", "-1001,7", "GALLERY_ADMIN_TOKEN", testAdminToken))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	text := slog.New(slog.NewTextHandler(&logs, nil))
	js := slog.New(slog.NewJSONHandler(&logs, nil))
	text.Info("config", "config", c)
	text.Info("config", "config", *c)
	js.Info("config", "config", c)
	js.Info("config", "config", *c)
	marshalled, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{
		logs.String(), string(marshalled),
		c.String(), c.GoString(),
		fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c),
		fmt.Sprint(*c), fmt.Sprintf("%v", *c), fmt.Sprintf("%+v", *c), fmt.Sprintf("%#v", *c), fmt.Sprintf("%s", *c),
		fmt.Sprintf("%v", []*Config{c}), fmt.Sprintf("%+v", struct{ C Config }{*c}),
	}
	for i, out := range outputs {
		if strings.Contains(out, testToken) || strings.Contains(out, testAdminToken) || strings.Contains(out, testBotToken) || strings.Contains(out, "AAH-secret") {
			t.Errorf("output %d leaks a secret: %s", i, out)
		}
	}
	if !strings.Contains(c.String(), "view=token token=[redacted] admin_token=[redacted]") || !strings.Contains(c.String(), "chats=-1001,7") || !strings.Contains(c.String(), "listen=127.0.0.1:8080") {
		t.Errorf("String() = %s", c.String())
	}
	if got := c.Secrets(); len(got) != 3 || got[0] != testToken || got[1] != testAdminToken || got[2] != testBotToken {
		t.Errorf("Secrets() = %d values", len(got))
	}

	public, err := FromEnv(env("GALLERY_TOKEN", "", "GALLERY_PUBLIC", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(public.String(), "view=public token=(unset) admin_token=(unset)") || len(public.Secrets()) != 1 {
		t.Errorf("public String() = %s", public.String())
	}
}

func TestTrustedProxies(t *testing.T) {
	for value, want := range map[string]string{
		"":                               "127.0.0.0/8,::1/128", // loopback unless told otherwise
		"none":                           "none",
		"NONE":                           "none",
		"172.18.0.1":                     "172.18.0.1/32",
		" 172.18.0.1 , 10.1.2.3/8,, ":    "172.18.0.1/32,10.0.0.0/8",
		"fd00::1,2001:db8::/32":          "fd00::1/128,2001:db8::/32",
		"::ffff:10.0.0.1":                "10.0.0.1/32",
		"127.0.0.1,::1,192.168.0.0/16":   "127.0.0.1/32,::1/128,192.168.0.0/16",
		"172.16.0.0/12,172.16.0.0/12,,,": "172.16.0.0/12,172.16.0.0/12",
	} {
		c, err := FromEnv(env("GALLERY_TRUSTED_PROXIES", value))
		if err != nil {
			t.Errorf("%q: %v", value, err)
			continue
		}
		var got []string
		for _, p := range c.TrustedProxies {
			got = append(got, p.String())
		}
		text := strings.Join(got, ",")
		if text == "" {
			text = "none"
		}
		if text != want {
			t.Errorf("%q: trusted proxies %s, want %s", value, text, want)
		}
		if !strings.Contains(c.String(), "trusted_proxies="+want) {
			t.Errorf("%q: String() does not show the proxies: %s", value, c.String())
		}
	}
}
