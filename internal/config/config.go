// Package config reads and validates ECHOO_* environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"echoo/internal/mailauth"
	"echoo/internal/scan"
)

type Config struct {
	DatabaseURL    string
	BaseURL        *url.URL
	ListenAddr     string
	EncryptionKeys string
	TrustedProxies []netip.Prefix
	LogLevel       slog.Level
	// Storage is a storage.FromURL location; S3 credentials only apply to s3:// locations.
	Storage     string
	S3AccessKey string
	S3SecretKey string
	// MetricsAddr is where the Prometheus endpoint listens, on a listener of its own; empty
	// turns metrics off.
	MetricsAddr string
	// MaxAttachmentMB is the size limit of one uploaded attachment, in MiB.
	MaxAttachmentMB int
	// ClamAVAddr is the host:port of a clamd daemon from ECHOO_CLAMAV_ADDR; empty turns virus
	// scanning off. It is operator-set, so internal addresses are allowed.
	ClamAVAddr string
	// CampaignMaxRate is the highest sending rate, in messages per minute and per mailbox, a
	// campaign may ask for. Providers cap what a mailbox may send; set this to their limit.
	CampaignMaxRate int

	// OAuth client credentials for Google and Microsoft mailboxes. A provider is offered in
	// the UI only when both its client ID and secret are set.
	OAuthGoogleClientID        string
	OAuthGoogleClientSecret    string
	OAuthMicrosoftClientID     string
	OAuthMicrosoftClientSecret string
	OAuthMicrosoftTenant       string

	// SystemMailbox is the address of an existing mailbox that sends system mail (invitations,
	// password resets, notifications). It is mutually exclusive with SystemSMTP.
	SystemMailbox string
	// SystemSMTP is a dedicated relay for system mail, or nil.
	SystemSMTP *SMTPRelay
}

// SMTPRelay is a dedicated SMTP server for system mail, from ECHOO_SMTP_URL.
type SMTPRelay struct {
	Host string
	Port int
	// TLS is "implicit" for smtps:// and "starttls" for smtp://; plaintext is never used.
	TLS      string
	Username string
	Password string
	// From is the sender address of system mail.
	From string
	// AllowInternal lifts the block on internal addresses (?allow_internal=true), for a relay
	// on the local network.
	AllowInternal bool
}

// DefaultMaxAttachmentMB is used when ECHOO_MAX_ATTACHMENT_MB is not set.
const DefaultMaxAttachmentMB = 25

// DefaultCampaignMaxRate is used when ECHOO_CAMPAIGN_MAX_RATE is not set.
const DefaultCampaignMaxRate = 120

// MaxAttachmentBytes is the size limit of one uploaded attachment.
func (c *Config) MaxAttachmentBytes() int64 {
	mb := c.MaxAttachmentMB
	if mb <= 0 {
		mb = DefaultMaxAttachmentMB
	}
	return int64(mb) << 20
}

// CampaignRateLimit is the highest campaign rate per mailbox, in messages per minute.
func (c *Config) CampaignRateLimit() int {
	if c.CampaignMaxRate <= 0 {
		return DefaultCampaignMaxRate
	}
	return c.CampaignMaxRate
}

// SecureCookies reports whether cookies must carry the Secure attribute. Plain HTTP is only
// accepted for loopback hosts during development.
func (c *Config) SecureCookies() bool { return c.BaseURL.Scheme == "https" }

// Origin is the scheme://host[:port] that browsers send in the Origin header.
func (c *Config) Origin() string { return c.BaseURL.Scheme + "://" + c.BaseURL.Host }

// Load reads configuration from the given environment lookup (os.LookupEnv in production).
func Load(lookup func(string) (string, bool)) (*Config, error) {
	var errs []error
	get := func(name string, required bool) string {
		v, err := value(lookup, name)
		if err != nil {
			errs = append(errs, err)
			return ""
		}
		if v == "" && required {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return v
	}

	c := &Config{
		DatabaseURL:    get("ECHOO_DATABASE_URL", true),
		ListenAddr:     get("ECHOO_LISTEN_ADDR", false),
		EncryptionKeys: get("ECHOO_ENCRYPTION_KEYS", true),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	c.Storage = get("ECHOO_STORAGE", false)
	if c.Storage == "" {
		c.Storage = "fs:///data/blobs"
	}
	c.MaxAttachmentMB = DefaultMaxAttachmentMB
	if raw := get("ECHOO_MAX_ATTACHMENT_MB", false); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			errs = append(errs, errors.New("ECHOO_MAX_ATTACHMENT_MB must be a whole number between 1 and 100"))
		}
		c.MaxAttachmentMB = n
	}
	if raw := get("ECHOO_CLAMAV_ADDR", false); raw != "" {
		addr, err := scan.ParseAddr(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("ECHOO_CLAMAV_ADDR: %w", err))
		}
		c.ClamAVAddr = addr
	}
	c.CampaignMaxRate = DefaultCampaignMaxRate
	if raw := get("ECHOO_CAMPAIGN_MAX_RATE", false); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			errs = append(errs, errors.New("ECHOO_CAMPAIGN_MAX_RATE must be a whole number between 1 and 1000"))
		}
		c.CampaignMaxRate = n
	}
	c.MetricsAddr = get("ECHOO_METRICS_ADDR", false)
	if c.MetricsAddr != "" {
		if _, port, err := net.SplitHostPort(c.MetricsAddr); err != nil || port == "" || port == "0" {
			errs = append(errs, errors.New("ECHOO_METRICS_ADDR must look like 127.0.0.1:9090"))
		} else if c.MetricsAddr == c.ListenAddr {
			errs = append(errs, errors.New("ECHOO_METRICS_ADDR must differ from ECHOO_LISTEN_ADDR: metrics never share the public listener"))
		}
	}
	c.S3AccessKey = get("ECHOO_S3_ACCESS_KEY", false)
	c.S3SecretKey = get("ECHOO_S3_SECRET_KEY", false)

	c.OAuthGoogleClientID = get("ECHOO_OAUTH_GOOGLE_CLIENT_ID", false)
	c.OAuthGoogleClientSecret = get("ECHOO_OAUTH_GOOGLE_CLIENT_SECRET", false)
	c.OAuthMicrosoftClientID = get("ECHOO_OAUTH_MICROSOFT_CLIENT_ID", false)
	c.OAuthMicrosoftClientSecret = get("ECHOO_OAUTH_MICROSOFT_CLIENT_SECRET", false)
	c.OAuthMicrosoftTenant = get("ECHOO_OAUTH_MICROSOFT_TENANT", false)
	if c.OAuthMicrosoftTenant == "" {
		c.OAuthMicrosoftTenant = "common"
	}
	if (c.OAuthGoogleClientID == "") != (c.OAuthGoogleClientSecret == "") {
		errs = append(errs, errors.New("ECHOO_OAUTH_GOOGLE_CLIENT_ID and ECHOO_OAUTH_GOOGLE_CLIENT_SECRET must be set together"))
	}
	if (c.OAuthMicrosoftClientID == "") != (c.OAuthMicrosoftClientSecret == "") {
		errs = append(errs, errors.New("ECHOO_OAUTH_MICROSOFT_CLIENT_ID and ECHOO_OAUTH_MICROSOFT_CLIENT_SECRET must be set together"))
	}
	if err := mailauth.ValidateTenant(c.OAuthMicrosoftTenant); err != nil {
		errs = append(errs, fmt.Errorf("ECHOO_OAUTH_MICROSOFT_TENANT %w", err))
	}

	c.SystemMailbox = strings.ToLower(get("ECHOO_SYSTEM_MAILBOX", false))
	if raw := get("ECHOO_SMTP_URL", false); raw != "" {
		relay, err := parseSMTPURL(raw)
		if err != nil {
			errs = append(errs, err)
		}
		c.SystemSMTP = relay
	}
	if c.SystemMailbox != "" && c.SystemSMTP != nil {
		errs = append(errs, errors.New("set either ECHOO_SYSTEM_MAILBOX or ECHOO_SMTP_URL, not both"))
	}

	if raw := get("ECHOO_BASE_URL", true); raw != "" {
		u, err := parseBaseURL(raw)
		if err != nil {
			errs = append(errs, err)
		}
		c.BaseURL = u
	}

	for _, s := range splitList(get("ECHOO_TRUSTED_PROXIES", false)) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("ECHOO_TRUSTED_PROXIES: %q is not a CIDR", s))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, p.Masked())
	}

	if lvl := get("ECHOO_LOG_LEVEL", false); lvl != "" {
		if err := c.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			errs = append(errs, fmt.Errorf("ECHOO_LOG_LEVEL: %w", err))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadDatabaseURL reads only ECHOO_DATABASE_URL, for commands that must not need the rest of
// the configuration (such as migrate, which runs without the encryption keys).
func LoadDatabaseURL(lookup func(string) (string, bool)) (string, error) {
	v, err := value(lookup, "ECHOO_DATABASE_URL")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", errors.New("ECHOO_DATABASE_URL is required")
	}
	return v, nil
}

// value returns NAME, or the contents of the file named by NAME_FILE. Setting both is an error
// because it is ambiguous which one wins.
func value(lookup func(string) (string, bool), name string) (string, error) {
	v, hasValue := lookup(name)
	path, hasFile := lookup(name + "_FILE")
	switch {
	case hasValue && hasFile:
		return "", fmt.Errorf("set either %s or %s_FILE, not both", name, name)
	case hasFile:
		b, err := os.ReadFile(path) //nolint:gosec // reading an operator-chosen secret file is the point
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	default:
		return strings.TrimSpace(v), nil
	}
}

// Warnings lists configuration that is valid but probably not what the operator wants.
func (c *Config) Warnings() []string {
	var out []string
	// Echoo speaks plain HTTP, so an https base URL means a reverse proxy terminates TLS in
	// front of it. Without trusted proxies every client then appears to come from the proxy.
	if c.SecureCookies() && len(c.TrustedProxies) == 0 {
		out = append(out, "ECHOO_BASE_URL is https but ECHOO_TRUSTED_PROXIES is empty: X-Forwarded-For is ignored, "+
			"so behind a reverse proxy all users share one login rate-limit bucket and audit entries record the proxy's address; "+
			"set ECHOO_TRUSTED_PROXIES to the proxy's address range")
	}
	return out
}

// parseSMTPURL reads smtp(s)://user:pass@host:port?from=addr[&allow_internal=true]. Errors never
// repeat the URL, which holds a password.
func parseSMTPURL(raw string) (*SMTPRelay, error) {
	bad := func(why string) error { return fmt.Errorf("ECHOO_SMTP_URL %s", why) }
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.Fragment != "" {
		return nil, bad("must look like smtps://user:password@smtp.example.com:465?from=noreply@example.com")
	}
	relay := &SMTPRelay{Host: strings.ToLower(u.Hostname())}
	switch u.Scheme {
	case "smtps":
		relay.TLS, relay.Port = "implicit", 465
	case "smtp":
		relay.TLS, relay.Port = "starttls", 587
	default:
		return nil, bad("must start with smtps:// (implicit TLS) or smtp:// (STARTTLS)")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, bad("has an invalid port")
		}
		relay.Port = n
	}
	relay.Username = u.User.Username()
	relay.Password, _ = u.User.Password()
	q := u.Query()
	relay.From = strings.ToLower(strings.TrimSpace(q.Get("from")))
	if relay.From == "" && strings.Contains(relay.Username, "@") {
		relay.From = strings.ToLower(relay.Username)
	}
	if addr, err := mail.ParseAddress(relay.From); err != nil || addr.Address != relay.From {
		return nil, bad("needs a sender address: add ?from=noreply@example.com")
	}
	relay.AllowInternal = q.Get("allow_internal") == "true"
	return relay, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil || strings.Contains(raw, "#") {
		return nil, fmt.Errorf("ECHOO_BASE_URL must look like https://support.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, fmt.Errorf("ECHOO_BASE_URL must use https (http is only allowed for localhost)")
		}
	default:
		return nil, fmt.Errorf("ECHOO_BASE_URL must use https")
	}
	u.Path = ""
	u.Host = normalizeHost(u.Scheme, u.Hostname(), u.Port())
	return u, nil
}

// normalizeHost lower-cases the host and drops the scheme's default port, so the result
// compares equal to the Origin header browsers send.
func normalizeHost(scheme, host, port string) string {
	host = strings.ToLower(host)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return host
	}
	return host + ":" + port
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
