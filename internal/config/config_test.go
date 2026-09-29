package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func valid() map[string]string {
	return map[string]string{
		"ECHOO_DATABASE_URL":    "postgres://echoo@db/echoo",
		"ECHOO_BASE_URL":        "https://support.example.com",
		"ECHOO_ENCRYPTION_KEYS": "k1:AAAA",
	}
}

func TestLoadValid(t *testing.T) {
	m := valid()
	m["ECHOO_TRUSTED_PROXIES"] = "10.0.0.0/8, 172.16.0.1/32"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || !c.SecureCookies() || c.Origin() != "https://support.example.com" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.Storage != "fs:///data/blobs" {
		t.Fatalf("default storage: %q", c.Storage)
	}
	if len(c.TrustedProxies) != 2 {
		t.Fatalf("trusted proxies: %v", c.TrustedProxies)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing database":      func(m map[string]string) { delete(m, "ECHOO_DATABASE_URL") },
		"http non-loopback":     func(m map[string]string) { m["ECHOO_BASE_URL"] = "http://support.example.com" },
		"base url with path":    func(m map[string]string) { m["ECHOO_BASE_URL"] = "https://example.com/echoo" },
		"base url fragment":     func(m map[string]string) { m["ECHOO_BASE_URL"] = "https://example.com/#app" },
		"bad proxy":             func(m map[string]string) { m["ECHOO_TRUSTED_PROXIES"] = "10.0.0.1" },
		"value and file":        func(m map[string]string) { m["ECHOO_ENCRYPTION_KEYS_FILE"] = "/x" },
		"attachment limit text": func(m map[string]string) { m["ECHOO_MAX_ATTACHMENT_MB"] = "big" },
		"attachment limit zero": func(m map[string]string) { m["ECHOO_MAX_ATTACHMENT_MB"] = "0" },
		"attachment limit huge": func(m map[string]string) { m["ECHOO_MAX_ATTACHMENT_MB"] = "1000" },
		"campaign rate text":    func(m map[string]string) { m["ECHOO_CAMPAIGN_MAX_RATE"] = "fast" },
		"campaign rate zero":    func(m map[string]string) { m["ECHOO_CAMPAIGN_MAX_RATE"] = "0" },
		"campaign rate huge":    func(m map[string]string) { m["ECHOO_CAMPAIGN_MAX_RATE"] = "1001" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			mutate(m)
			if _, err := Load(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadHTTPLocalhostAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("k9:BBBB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := valid()
	m["ECHOO_BASE_URL"] = "http://localhost:5173"
	delete(m, "ECHOO_ENCRYPTION_KEYS")
	m["ECHOO_ENCRYPTION_KEYS_FILE"] = path
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.SecureCookies() || !strings.HasPrefix(c.EncryptionKeys, "k9:") {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestBaseURLIsNormalized(t *testing.T) {
	cases := map[string]string{
		"https://Support.Example.COM":      "https://support.example.com",
		"https://support.example.com:443/": "https://support.example.com",
		"https://support.example.com:8443": "https://support.example.com:8443",
		"HTTP://LocalHost:80":              "http://localhost",
		"http://localhost:8080":            "http://localhost:8080",
		"http://[::1]:8080":                "http://[::1]:8080",
	}
	for raw, want := range cases {
		m := valid()
		m["ECHOO_BASE_URL"] = raw
		c, err := Load(env(m))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if c.Origin() != want || c.BaseURL.String() != want {
			t.Errorf("%s: origin %q, url %q, want %q", raw, c.Origin(), c.BaseURL, want)
		}
	}
}

func TestWarnsAboutMissingTrustedProxies(t *testing.T) {
	m := valid()
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "ECHOO_TRUSTED_PROXIES") {
		t.Fatalf("https without trusted proxies must warn, got %v", w)
	}

	m["ECHOO_TRUSTED_PROXIES"] = "172.16.0.0/12"
	if c, err = Load(env(m)); err != nil {
		t.Fatal(err)
	}
	if w := c.Warnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings %v", w)
	}

	m = valid()
	m["ECHOO_BASE_URL"] = "http://localhost:8080"
	if c, err = Load(env(m)); err != nil {
		t.Fatal(err)
	}
	if w := c.Warnings(); len(w) != 0 {
		t.Fatalf("local development has no proxy: %v", w)
	}
}

func TestMaxAttachmentSize(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MaxAttachmentBytes(); got != 25<<20 {
		t.Fatalf("default = %d, want 25 MiB", got)
	}
	m := valid()
	m["ECHOO_MAX_ATTACHMENT_MB"] = "10"
	if c, err = Load(env(m)); err != nil || c.MaxAttachmentBytes() != 10<<20 {
		t.Fatalf("configured limit: %v %v", c, err)
	}
}

func TestCampaignRateLimit(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil || c.CampaignRateLimit() != DefaultCampaignMaxRate {
		t.Fatalf("default: %v %v", c, err)
	}
	m := valid()
	m["ECHOO_CAMPAIGN_MAX_RATE"] = "30"
	if c, err = Load(env(m)); err != nil || c.CampaignRateLimit() != 30 {
		t.Fatalf("configured limit: %v %v", c, err)
	}
	if got := (&Config{}).CampaignRateLimit(); got != DefaultCampaignMaxRate {
		t.Errorf("zero value = %d, want the default", got)
	}
}

func TestOAuthProvidersAreOptionalAndComeInPairs(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.OAuthGoogleClientID != "" || c.OAuthMicrosoftTenant != "common" {
		t.Fatalf("defaults: %+v", c)
	}

	m := valid()
	m["ECHOO_OAUTH_GOOGLE_CLIENT_ID"], m["ECHOO_OAUTH_GOOGLE_CLIENT_SECRET"] = "gid", "gsecret"
	m["ECHOO_OAUTH_MICROSOFT_CLIENT_ID"], m["ECHOO_OAUTH_MICROSOFT_CLIENT_SECRET"] = "mid", "msecret"
	m["ECHOO_OAUTH_MICROSOFT_TENANT"] = "contoso.onmicrosoft.com"
	if c, err = Load(env(m)); err != nil || c.OAuthGoogleClientSecret != "gsecret" || c.OAuthMicrosoftTenant != "contoso.onmicrosoft.com" {
		t.Fatalf("got %+v, %v", c, err)
	}

	for name, set := range map[string]map[string]string{
		"google id without secret": {"ECHOO_OAUTH_GOOGLE_CLIENT_ID": "gid"},
		"microsoft secret only":    {"ECHOO_OAUTH_MICROSOFT_CLIENT_SECRET": "s"},
		"tenant with a path":       {"ECHOO_OAUTH_MICROSOFT_TENANT": "a/b"},
		"secret in value and file": {"ECHOO_OAUTH_GOOGLE_CLIENT_ID": "gid", "ECHOO_OAUTH_GOOGLE_CLIENT_SECRET": "s", "ECHOO_OAUTH_GOOGLE_CLIENT_SECRET_FILE": "/x"},
	} {
		t.Run(name, func(t *testing.T) {
			m := valid()
			for k, v := range set {
				m[k] = v
			}
			if _, err := Load(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = valid()
	m["ECHOO_OAUTH_GOOGLE_CLIENT_ID"], m["ECHOO_OAUTH_GOOGLE_CLIENT_SECRET_FILE"] = "gid", secret
	if c, err = Load(env(m)); err != nil || c.OAuthGoogleClientSecret != "from-file" {
		t.Fatalf("file secret: %+v, %v", c, err)
	}
}

func TestSystemMailConfiguration(t *testing.T) {
	m := valid()
	m["ECHOO_SYSTEM_MAILBOX"] = "Help@Example.com"
	c, err := Load(env(m))
	if err != nil || c.SystemMailbox != "help@example.com" || c.SystemSMTP != nil {
		t.Fatalf("mailbox: %+v, %v", c, err)
	}

	tests := []struct {
		url  string
		want SMTPRelay
	}{
		{"smtps://noreply%40example.com:p%40ss%3Aword@smtp.example.com?from=Help@example.com",
			SMTPRelay{Host: "smtp.example.com", Port: 465, TLS: "implicit", Username: "noreply@example.com", Password: "p@ss:word", From: "help@example.com"}},
		{"smtp://relay.internal:2525?from=noreply@example.com&allow_internal=true",
			SMTPRelay{Host: "relay.internal", Port: 2525, TLS: "starttls", From: "noreply@example.com", AllowInternal: true}},
		{"smtp://sender@example.com:pw@smtp.example.com",
			SMTPRelay{Host: "smtp.example.com", Port: 587, TLS: "starttls", Username: "sender@example.com", Password: "pw", From: "sender@example.com"}},
	}
	for _, tc := range tests {
		m := valid()
		m["ECHOO_SMTP_URL"] = tc.url
		c, err := Load(env(m))
		if err != nil || c.SystemSMTP == nil || *c.SystemSMTP != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.url, c.SystemSMTP, err, tc.want)
		}
	}

	for name, url := range map[string]string{
		"plain scheme": "http://smtp.example.com?from=a@example.com",
		"no sender":    "smtps://user:secret-pw@smtp.example.com",
		"bad sender":   "smtps://user:secret-pw@smtp.example.com?from=nope",
		"bad port":     "smtps://user:secret-pw@smtp.example.com:99999?from=a@example.com",
		"path":         "smtps://user:secret-pw@smtp.example.com/x?from=a@example.com",
		"no host":      "smtps://?from=a@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			m := valid()
			m["ECHOO_SMTP_URL"] = url
			_, err := Load(env(m))
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret-pw") {
				t.Fatalf("the error leaks the password: %v", err)
			}
		})
	}

	m = valid()
	m["ECHOO_SYSTEM_MAILBOX"] = "help@example.com"
	m["ECHOO_SMTP_URL"] = "smtps://smtp.example.com?from=a@example.com"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("both sources must be refused")
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "smtp_url")
	if err := os.WriteFile(file, []byte("smtps://u:p@smtp.example.com?from=a@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = valid()
	m["ECHOO_SMTP_URL_FILE"] = file
	if c, err = Load(env(m)); err != nil || c.SystemSMTP == nil || c.SystemSMTP.Password != "p" {
		t.Fatalf("file: %+v, %v", c, err)
	}
}

func TestMetricsAddress(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil || c.MetricsAddr != "" {
		t.Fatalf("metrics are off by default: %q %v", c.MetricsAddr, err)
	}
	m := valid()
	m["ECHOO_METRICS_ADDR"] = "127.0.0.1:9090"
	if c, err = Load(env(m)); err != nil || c.MetricsAddr != "127.0.0.1:9090" {
		t.Fatalf("configured address: %v %v", c, err)
	}
	for _, bad := range []string{"9090", "localhost", "127.0.0.1:0", "127.0.0.1:"} {
		m["ECHOO_METRICS_ADDR"] = bad
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "ECHOO_METRICS_ADDR") {
			t.Errorf("%q should be rejected, got %v", bad, err)
		}
	}
	m["ECHOO_LISTEN_ADDR"] = ":8080"
	m["ECHOO_METRICS_ADDR"] = ":8080"
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "public listener") {
		t.Errorf("the metrics listener must not be the public one: %v", err)
	}
}

func TestClamAVAddr(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil || c.ClamAVAddr != "" {
		t.Fatalf("scanning is off by default: %q %v", c.ClamAVAddr, err)
	}
	m := valid()
	m["ECHOO_CLAMAV_ADDR"] = "tcp://clamav:3310"
	if c, err = Load(env(m)); err != nil || c.ClamAVAddr != "clamav:3310" {
		t.Fatalf("tcp address: %q %v", c.ClamAVAddr, err)
	}
	m["ECHOO_CLAMAV_ADDR"] = "unix:///run/clamd.sock"
	if _, err = Load(env(m)); err == nil || !strings.Contains(err.Error(), "ECHOO_CLAMAV_ADDR") {
		t.Fatalf("a socket path must be refused: %v", err)
	}
}
