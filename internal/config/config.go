// Package config reads and validates the gallery's settings from the environment.
//
// Every value is checked at start-up. Load reports every problem at once, names
// the variable at fault and never includes the value of a secret.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Defaults and limits.
const (
	DefaultListen         = "127.0.0.1:8080"
	SourceDynamoDB        = "dynamodb" // GALLERY_SOURCE: the bot's DynamoDB table (the default)
	SourceSQLite          = "sqlite"   // GALLERY_SOURCE: the database file of the bot's self-hosted mode
	DefaultTable          = "dalibot"
	DefaultRegion         = "ap-southeast-1"
	DefaultTelegramAPI    = "https://api.telegram.org"
	DefaultStoreDays      = 90
	DefaultCommand        = "dream"
	DefaultRefresh        = 60 * time.Second
	DefaultCacheDir       = "./cache"
	DefaultCacheMaxMB     = 2048
	DefaultWebPQuality    = 76
	DefaultWarm           = 120
	MinTokenLength        = 16
	MinRefresh            = 5 * time.Second
	MaxRefresh            = 24 * time.Hour
	MinCacheMaxMB         = 16
	MaxImageWorkers       = 64
	MaxWarm               = 10000
	MaxStoreDays          = 3650
	redacted              = "[redacted]"
	envCacheDirectory     = "CACHE_DIRECTORY" // set by systemd's CacheDirectory=
	envStateDirectory     = "STATE_DIRECTORY" // set by systemd's StateDirectory=
	envRegionFallback     = "AWS_DEFAULT_REGION"
	maxChats              = 1000
	maxTrustedProxies     = 64
	maxDefaultCommandSize = 32
)

// Config is the validated configuration. Token, AdminToken and TelegramToken
// are secrets: print a Config only through String, GoString or LogValue,
// which redact them.
//
// Who may view: everyone when Public, otherwise whoever presents Token.
// Exactly one of the two is set. Who may administer: whoever presents
// AdminToken, which is optional and always differs from Token.
type Config struct {
	Listen          string        // GALLERY_LISTEN, host:port
	Public          bool          // GALLERY_PUBLIC=1: viewing needs no login
	Token           string        `json:"-"` // GALLERY_TOKEN (secret): viewing needs it; empty exactly when Public
	AdminToken      string        `json:"-"` // GALLERY_ADMIN_TOKEN (secret); empty means no admin features
	Source          string        // GALLERY_SOURCE: SourceDynamoDB or SourceSQLite
	Table           string        // DALIBOT_TABLE; empty when Source is SourceSQLite
	Region          string        // AWS_REGION; empty when Source is SourceSQLite
	SQLitePath      string        // GALLERY_SQLITE_PATH, absolute; empty when Source is SourceDynamoDB
	TelegramToken   string        `json:"-"` // TELEGRAM_BOT_TOKEN (secret)
	TelegramAPIBase string        // TELEGRAM_API_BASE, no trailing slash
	StoreDays       int           // GALLERY_STORE_DAYS
	DefaultCommand  string        // GALLERY_DEFAULT_COMMAND, without a leading slash
	Chats           []string      // GALLERY_CHATS; empty means all chats
	Refresh         time.Duration // GALLERY_REFRESH
	CacheDir        string        // GALLERY_CACHE_DIR, absolute
	CacheMaxMB      int           // GALLERY_CACHE_MAX_MB
	// StateDir is GALLERY_STATE_DIR, absolute: where the gallery keeps what
	// cannot be made again (the list of hidden images). StateInCache says
	// that neither GALLERY_STATE_DIR nor $STATE_DIRECTORY was set, so
	// StateDir is CacheDir and wiping the cache would lose the state.
	StateDir     string
	StateInCache bool
	WebPQuality  int // GALLERY_WEBP_QUALITY
	ImageWorkers int // GALLERY_IMAGE_WORKERS
	Warm         int // GALLERY_WARM
	// TrustedProxies is GALLERY_TRUSTED_PROXIES: the peers whose
	// X-Forwarded-For header is believed. Loopback when unset; empty for "none".
	TrustedProxies []netip.Prefix
}

var (
	reTable   = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)
	reRegion  = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`)
	reBotTok  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	reChat    = regexp.MustCompile(`^-?[0-9]{1,20}$`)
	reCommand = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// Load reads the configuration from the process environment.
func Load() (*Config, error) {
	return FromEnv(os.Getenv)
}

// FromEnv reads the configuration through getenv (os.Getenv in production, a map in tests).
// The returned error lists every invalid variable; it never contains a secret's value.
func FromEnv(getenv func(string) string) (*Config, error) {
	var errs []error
	bad := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", name, fmt.Sprintf(format, args...)))
	}
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	str := func(name, def string) string {
		if v := get(name); v != "" {
			return v
		}
		return def
	}
	integer := func(name string, def, lo, hi int) int {
		v := get(name)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			bad(name, "must be a whole number, not %q", v)
			return def
		}
		if n < lo || n > hi {
			bad(name, "must be between %d and %d, not %d", lo, hi, n)
			return def
		}
		return n
	}

	c := &Config{}

	// Listen address.
	c.Listen = str("GALLERY_LISTEN", DefaultListen)
	if host, port, err := net.SplitHostPort(c.Listen); err != nil {
		bad("GALLERY_LISTEN", "must be host:port (for example %s), not %q", DefaultListen, c.Listen)
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		bad("GALLERY_LISTEN", "port must be between 1 and 65535, not %q", port)
	} else if strings.ContainsAny(host, " /") {
		bad("GALLERY_LISTEN", "host %q is not valid", host)
	}

	// Reverse proxies whose X-Forwarded-For is believed.
	switch v := get("GALLERY_TRUSTED_PROXIES"); strings.ToLower(v) {
	case "":
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	case "none":
	default:
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			var p netip.Prefix
			if addr, err := netip.ParseAddr(part); err == nil {
				addr = addr.Unmap().WithZone("")
				p = netip.PrefixFrom(addr, addr.BitLen())
			} else if p, err = netip.ParsePrefix(part); err != nil {
				bad("GALLERY_TRUSTED_PROXIES", "%q is not an IP address or a CIDR range (for example 172.18.0.1 or 10.0.0.0/8)", part)
				continue
			} else if p.Bits() == 0 {
				bad("GALLERY_TRUSTED_PROXIES", "%q would trust every address; list the proxies' own addresses", part)
				continue
			}
			c.TrustedProxies = append(c.TrustedProxies, p.Masked())
		}
		if len(c.TrustedProxies) > maxTrustedProxies {
			bad("GALLERY_TRUSTED_PROXIES", "lists %d entries; the limit is %d", len(c.TrustedProxies), maxTrustedProxies)
		}
	}

	// Access. The tokens are secrets: messages must not echo them.
	c.Token = getenv("GALLERY_TOKEN")            // not trimmed: compared byte for byte
	c.AdminToken = getenv("GALLERY_ADMIN_TOKEN") // likewise
	switch v := get("GALLERY_PUBLIC"); v {
	case "1":
		c.Public = true
	case "", "0":
	default:
		bad("GALLERY_PUBLIC", "must be 1 to let anyone view the gallery, or unset; not %q", v)
	}
	if get("GALLERY_AUTH") != "" {
		// Removed. Its value is not echoed and not interpreted: a gallery that
		// used to rely on it must not start with a different meaning.
		bad("GALLERY_AUTH", "is no longer supported; remove it. To let anyone view the gallery without a login (what GALLERY_AUTH=off did), set GALLERY_PUBLIC=1 and leave GALLERY_TOKEN unset. To keep the login, GALLERY_TOKEN alone is enough")
	}
	switch {
	case c.Public && c.Token != "":
		bad("GALLERY_PUBLIC", "is 1 but GALLERY_TOKEN is also set, which contradicts it. Unset GALLERY_TOKEN for a gallery anyone can view, or unset GALLERY_PUBLIC to require the token (GALLERY_ADMIN_TOKEN is the separate, optional token for administering)")
	case !c.Public && c.Token == "":
		bad("GALLERY_TOKEN", "is required. Set it to a long random string (for example `openssl rand -base64 32`), or set GALLERY_PUBLIC=1 to let anyone who can reach the gallery view it without a login")
	}
	tokenShape := func(name, v string) {
		switch {
		case len(v) < MinTokenLength:
			bad(name, "must be at least %d characters (it has %d)", MinTokenLength, len(v))
		case strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\t\x00"):
			bad(name, "must not start or end with a space or contain control characters")
		}
	}
	if c.Token != "" {
		tokenShape("GALLERY_TOKEN", c.Token)
	}
	if c.AdminToken != "" {
		tokenShape("GALLERY_ADMIN_TOKEN", c.AdminToken)
		if c.AdminToken == c.Token {
			bad("GALLERY_ADMIN_TOKEN", "must differ from GALLERY_TOKEN: everyone who can view would be an admin")
		}
	}

	// Where the bot's records are read from. Only the chosen source's settings are read.
	c.Source = strings.ToLower(str("GALLERY_SOURCE", SourceDynamoDB))
	sqlitePath := get("GALLERY_SQLITE_PATH")
	switch c.Source {
	case SourceDynamoDB:
		c.Table = str("DALIBOT_TABLE", DefaultTable)
		if !reTable.MatchString(c.Table) {
			bad("DALIBOT_TABLE", "%q is not a DynamoDB table name (3-255 of letters, digits, '_', '-', '.')", c.Table)
		}
		c.Region = str("AWS_REGION", str(envRegionFallback, DefaultRegion))
		if !reRegion.MatchString(c.Region) {
			bad("AWS_REGION", "%q is not an AWS region (for example %s)", c.Region, DefaultRegion)
		}
		if sqlitePath != "" {
			// Most likely GALLERY_SOURCE was forgotten; reading DynamoDB instead would surprise.
			bad("GALLERY_SQLITE_PATH", "is set, but GALLERY_SOURCE is not %s. Set GALLERY_SOURCE=%s to read the bot's SQLite database, or remove GALLERY_SQLITE_PATH to read DynamoDB", SourceSQLite, SourceSQLite)
		}
	case SourceSQLite:
		switch abs, err := filepath.Abs(sqlitePath); {
		case sqlitePath == "":
			bad("GALLERY_SQLITE_PATH", "is required when GALLERY_SOURCE=%s: the path of the bot's database file (the bot's DALIBOT_DB, by default var/dalibot.db in the bot's directory)", SourceSQLite)
		case strings.ContainsRune(sqlitePath, 0):
			bad("GALLERY_SQLITE_PATH", "is not a valid path")
		case err != nil:
			bad("GALLERY_SQLITE_PATH", "cannot be made absolute: %v", err)
		case strings.HasSuffix(sqlitePath, string(filepath.Separator)):
			bad("GALLERY_SQLITE_PATH", "must be the database file, not a directory: %q", sqlitePath)
		default:
			c.SQLitePath = abs
		}
	default:
		bad("GALLERY_SOURCE", "must be %s or %s, not %q", SourceDynamoDB, SourceSQLite, c.Source)
	}

	// Telegram. The token is a secret.
	c.TelegramToken = get("TELEGRAM_BOT_TOKEN")
	switch {
	case c.TelegramToken == "":
		bad("TELEGRAM_BOT_TOKEN", "is required (the bot's token; the gallery only uses it for getFile and file downloads)")
	case !reBotTok.MatchString(c.TelegramToken):
		bad("TELEGRAM_BOT_TOKEN", "does not look like a bot token (expected <digits>:<letters, digits, '_' or '-'>)")
	}
	c.TelegramAPIBase = strings.TrimRight(str("TELEGRAM_API_BASE", DefaultTelegramAPI), "/")
	if u, err := url.Parse(c.TelegramAPIBase); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		bad("TELEGRAM_API_BASE", "must be an http(s) URL such as %s", DefaultTelegramAPI)
	} else if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		bad("TELEGRAM_API_BASE", "must not contain credentials, a query or a fragment")
	}

	// Listing.
	c.StoreDays = integer("GALLERY_STORE_DAYS", DefaultStoreDays, 1, MaxStoreDays)
	c.DefaultCommand = strings.TrimLeft(str("GALLERY_DEFAULT_COMMAND", DefaultCommand), "/")
	if !reCommand.MatchString(c.DefaultCommand) || len(c.DefaultCommand) > maxDefaultCommandSize {
		bad("GALLERY_DEFAULT_COMMAND", "must be 1-%d of a-z, 0-9 and '_', not %q", maxDefaultCommandSize, c.DefaultCommand)
	}
	seen := map[string]bool{}
	for chat := range strings.SplitSeq(get("GALLERY_CHATS"), ",") {
		chat = strings.TrimSpace(chat)
		if chat == "" {
			continue
		}
		if !reChat.MatchString(chat) {
			bad("GALLERY_CHATS", "%q is not a chat id (a whole number, negative for groups)", chat)
			continue
		}
		if !seen[chat] {
			seen[chat] = true
			c.Chats = append(c.Chats, chat)
		}
	}
	if len(c.Chats) > maxChats {
		bad("GALLERY_CHATS", "lists %d chats; the limit is %d", len(c.Chats), maxChats)
	}
	c.Refresh = DefaultRefresh
	if v := get("GALLERY_REFRESH"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			if n, err2 := strconv.Atoi(v); err2 == nil { // a bare number means seconds
				d, err = time.Duration(n)*time.Second, nil
			}
		}
		switch {
		case err != nil:
			bad("GALLERY_REFRESH", "must be a duration such as 60s or 2m, not %q", v)
		case d < MinRefresh || d > MaxRefresh:
			bad("GALLERY_REFRESH", "must be between %s and %s, not %s", MinRefresh, MaxRefresh, d)
		default:
			c.Refresh = d
		}
	}

	// Cache and images.
	dir := get("GALLERY_CACHE_DIR")
	if dir == "" {
		// systemd may list several directories separated by ':'; ours is the first.
		dir, _, _ = strings.Cut(get(envCacheDirectory), ":")
	}
	if dir == "" {
		dir = DefaultCacheDir
	}
	if strings.ContainsRune(dir, 0) {
		bad("GALLERY_CACHE_DIR", "is not a valid path")
	} else if abs, err := filepath.Abs(dir); err != nil {
		bad("GALLERY_CACHE_DIR", "cannot be made absolute: %v", err)
	} else if abs == string(filepath.Separator) {
		bad("GALLERY_CACHE_DIR", "must not be the filesystem root")
	} else {
		c.CacheDir = abs
	}
	// State: what cannot be fetched or made again. Without a directory of
	// its own it lives in the cache directory (main warns about that).
	state := get("GALLERY_STATE_DIR")
	if state == "" {
		state, _, _ = strings.Cut(get(envStateDirectory), ":")
	}
	switch {
	case state == "":
		c.StateDir, c.StateInCache = c.CacheDir, true
	case strings.ContainsRune(state, 0):
		bad("GALLERY_STATE_DIR", "is not a valid path")
	default:
		if abs, err := filepath.Abs(state); err != nil {
			bad("GALLERY_STATE_DIR", "cannot be made absolute: %v", err)
		} else if abs == string(filepath.Separator) {
			bad("GALLERY_STATE_DIR", "must not be the filesystem root")
		} else {
			c.StateDir = abs
		}
	}
	c.CacheMaxMB = integer("GALLERY_CACHE_MAX_MB", DefaultCacheMaxMB, MinCacheMaxMB, 1<<30)
	c.WebPQuality = integer("GALLERY_WEBP_QUALITY", DefaultWebPQuality, 1, 100)
	c.ImageWorkers = integer("GALLERY_IMAGE_WORKERS", min(runtime.GOMAXPROCS(0), MaxImageWorkers), 1, MaxImageWorkers)
	c.Warm = integer("GALLERY_WARM", DefaultWarm, 0, MaxWarm)

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  %w", joinLines(errs))
	}
	return c, nil
}

// joinLines is errors.Join with an indent, so each problem is on its own line.
func joinLines(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return errors.New(strings.Join(msgs, "\n  "))
}

// CacheMaxBytes is GALLERY_CACHE_MAX_MB in bytes.
func (c *Config) CacheMaxBytes() int64 { return int64(c.CacheMaxMB) << 20 }

// AdminEnabled reports whether an admin token is configured. Without one
// there are no admin sessions, features or routes.
func (c *Config) AdminEnabled() bool { return c.AdminToken != "" }

// LoginEnabled reports whether there is anything to sign in to: viewing
// needs the token, or an admin token is configured.
func (c *Config) LoginEnabled() bool { return !c.Public || c.AdminEnabled() }

// Secrets returns the secret values in the configuration, for log redaction.
func (c *Config) Secrets() []string {
	var s []string
	for _, v := range []string{c.Token, c.AdminToken, c.TelegramToken} {
		if v != "" {
			s = append(s, v)
		}
	}
	return s
}

func secretState(v string) string {
	if v == "" {
		return "(unset)"
	}
	return redacted
}

// LogValue makes slog print the configuration with secrets redacted.
func (c *Config) LogValue() slog.Value {
	chats := "all"
	if len(c.Chats) > 0 {
		chats = strings.Join(c.Chats, ",")
	}
	proxies := "none"
	if len(c.TrustedProxies) > 0 {
		parts := make([]string, len(c.TrustedProxies))
		for i, p := range c.TrustedProxies {
			parts[i] = p.String()
		}
		proxies = strings.Join(parts, ",")
	}
	view := "token"
	if c.Public {
		view = "public"
	}
	attrs := []slog.Attr{
		slog.String("listen", c.Listen),
		slog.String("view", view),
		slog.String("token", secretState(c.Token)),
		slog.String("admin_token", secretState(c.AdminToken)),
		slog.String("source", c.Source),
	}
	if c.Source == SourceSQLite {
		attrs = append(attrs, slog.String("sqlite_path", c.SQLitePath))
	} else {
		attrs = append(attrs, slog.String("table", c.Table), slog.String("region", c.Region))
	}
	attrs = append(attrs,
		slog.String("telegram_token", secretState(c.TelegramToken)),
		slog.String("telegram_api_base", c.TelegramAPIBase),
		slog.Int("store_days", c.StoreDays),
		slog.String("default_command", c.DefaultCommand),
		slog.String("chats", chats),
		slog.Duration("refresh", c.Refresh),
		slog.String("cache_dir", c.CacheDir),
		slog.Int("cache_max_mb", c.CacheMaxMB),
		slog.String("state_dir", c.StateDir),
		slog.Int("webp_quality", c.WebPQuality),
		slog.Int("image_workers", c.ImageWorkers),
		slog.Int("warm", c.Warm),
		slog.String("trusted_proxies", proxies),
	)
	return slog.GroupValue(attrs...)
}

// String prints the configuration with secrets redacted.
func (c *Config) String() string {
	var b strings.Builder
	for i, a := range c.LogValue().Group() {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%s", a.Key, a.Value)
	}
	return b.String()
}

// GoString keeps %#v from printing secrets.
func (c *Config) GoString() string { return "config.Config{" + c.String() + "}" }

// Format keeps every fmt verb (including %+v on a value, not just a pointer) from printing secrets.
func (c Config) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		fmt.Fprint(f, (&c).GoString())
		return
	}
	fmt.Fprint(f, (&c).String())
}
