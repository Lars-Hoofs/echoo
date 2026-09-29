// Package mailauth implements OAuth2 for mailboxes at Google and Microsoft: the provider
// definitions, the state that protects the authorization flow, and a token source that
// refreshes access tokens under a database lock.
package mailauth

import (
	"errors"
	"fmt"
	"regexp"

	"golang.org/x/oauth2"
)

const (
	ProviderGoogle    = "google"
	ProviderMicrosoft = "microsoft"

	AuthTypeGoogle    = "oauth_google"
	AuthTypeMicrosoft = "oauth_microsoft"

	DefaultMicrosoftTenant = "common"
)

// ServerSettings is a mail server endpoint; TLS matches the mailboxes.*_tls values.
type ServerSettings struct {
	Host string
	Port int
	TLS  string
}

// Provider is one configured OAuth2 identity provider.
type Provider struct {
	ID       string
	Name     string
	AuthType string

	ClientID     string
	ClientSecret string
	Endpoint     oauth2.Endpoint
	Scopes       []string
	// AuthParams are extra parameters of the authorization request.
	AuthParams []oauth2.AuthCodeOption

	IMAP ServerSettings
	SMTP ServerSettings
}

// RedirectURL is the callback the admin registers with the provider. baseURL is
// ECHOO_BASE_URL without a trailing slash.
func (p *Provider) RedirectURL(baseURL string) string {
	return baseURL + "/oauth/callback/" + p.ID
}

// Config returns the oauth2 client configuration for this provider.
func (p *Provider) Config(baseURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: p.ClientID, ClientSecret: p.ClientSecret,
		Endpoint: p.Endpoint, Scopes: p.Scopes, RedirectURL: p.RedirectURL(baseURL),
	}
}

// Credentials are the client credentials from the environment. A provider without a client
// ID and secret is not offered.
type Credentials struct {
	GoogleClientID, GoogleClientSecret       string
	MicrosoftClientID, MicrosoftClientSecret string
	MicrosoftTenant                          string
}

var tenantPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// ValidateTenant checks a Microsoft tenant: "common", "organizations", "consumers", a tenant
// ID or a verified domain. It ends up in a URL path, so nothing else is accepted.
func ValidateTenant(tenant string) error {
	if !tenantPattern.MatchString(tenant) {
		return errors.New("must be common, organizations, consumers, a tenant ID or a domain name")
	}
	return nil
}

// Providers holds the configured providers.
type Providers struct {
	list []*Provider
}

// NewProviders builds the providers that have credentials.
func NewProviders(c Credentials) (*Providers, error) {
	ps := &Providers{}
	if c.GoogleClientID != "" && c.GoogleClientSecret != "" {
		ps.list = append(ps.list, &Provider{
			ID: ProviderGoogle, Name: "Google", AuthType: AuthTypeGoogle,
			ClientID: c.GoogleClientID, ClientSecret: c.GoogleClientSecret,
			Endpoint: oauth2.Endpoint{ //nolint:gosec // public endpoint URLs, not credentials
				AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:  "https://oauth2.googleapis.com/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			Scopes: []string{"https://mail.google.com/", "openid", "email"},
			// Without consent Google only returns a refresh token on the very first grant.
			AuthParams: []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")},
			IMAP:       ServerSettings{Host: "imap.gmail.com", Port: 993, TLS: "implicit"},
			SMTP:       ServerSettings{Host: "smtp.gmail.com", Port: 465, TLS: "implicit"},
		})
	}
	if c.MicrosoftClientID != "" && c.MicrosoftClientSecret != "" {
		tenant := c.MicrosoftTenant
		if tenant == "" {
			tenant = DefaultMicrosoftTenant
		}
		if err := ValidateTenant(tenant); err != nil {
			return nil, fmt.Errorf("microsoft tenant: %w", err)
		}
		base := "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0"
		ps.list = append(ps.list, &Provider{
			ID: ProviderMicrosoft, Name: "Microsoft 365", AuthType: AuthTypeMicrosoft,
			ClientID: c.MicrosoftClientID, ClientSecret: c.MicrosoftClientSecret,
			Endpoint: oauth2.Endpoint{AuthURL: base + "/authorize", TokenURL: base + "/token", AuthStyle: oauth2.AuthStyleInParams},
			Scopes: []string{
				"https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send",
				"offline_access", "openid", "email",
			},
			AuthParams: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("prompt", "select_account")},
			IMAP:       ServerSettings{Host: "outlook.office365.com", Port: 993, TLS: "implicit"},
			SMTP:       ServerSettings{Host: "smtp.office365.com", Port: 587, TLS: "starttls"},
		})
	}
	return ps, nil
}

// NewProvidersFrom is for tests, which point providers at a fake server.
func NewProvidersFrom(list ...*Provider) *Providers { return &Providers{list: list} }

// List returns the configured providers in a stable order.
func (ps *Providers) List() []*Provider { return ps.list }

func (ps *Providers) ByID(id string) (*Provider, bool) {
	for _, p := range ps.list {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}

func (ps *Providers) ByAuthType(authType string) (*Provider, bool) {
	for _, p := range ps.list {
		if p.AuthType == authType {
			return p, true
		}
	}
	return nil, false
}
