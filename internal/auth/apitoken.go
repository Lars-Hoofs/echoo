package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"echoo/internal/db/dbq"
)

const (
	// APITokenPrefix makes tokens recognizable in leaked logs and secret scanners.
	APITokenPrefix = "ech_"
	// apiTokenPrefixLen is how much of the token is stored in clear for the UI.
	apiTokenPrefixLen = 8

	ScopeRead  = "read"
	ScopeWrite = "write"
)

var ErrInvalidAPIToken = errors.New("invalid api token")

// NewAPIToken returns a token to hand to the user once, the prefix to show later and the hash
// to store. Like session tokens it holds 256 random bits, so a plain SHA-256 is enough.
func NewAPIToken() (token, prefix string, hash []byte, err error) {
	raw, _, err := NewToken()
	if err != nil {
		return "", "", nil, err
	}
	token = APITokenPrefix + raw
	return token, token[:apiTokenPrefixLen], HashToken(token), nil
}

// APIPrincipal is a request authenticated with an API token: it acts as User, limited by the
// token's scopes.
type APIPrincipal struct {
	Token dbq.ApiToken
	User  dbq.User
}

func (p *APIPrincipal) CanWrite() bool { return slices.Contains(p.Token.Scopes, ScopeWrite) }

// AuthenticateAPIToken resolves a bearer token. Revoked and expired tokens and tokens of
// deactivated users are all reported as ErrInvalidAPIToken.
func (s *Service) AuthenticateAPIToken(ctx context.Context, token string) (*APIPrincipal, error) {
	if !strings.HasPrefix(token, APITokenPrefix) {
		return nil, ErrInvalidAPIToken
	}
	row, err := s.q.GetActiveAPIToken(ctx, HashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidAPIToken
	}
	if err != nil {
		return nil, fmt.Errorf("load api token: %w", err)
	}
	now := s.now()
	if !row.ApiToken.LastUsedAt.Valid || now.Sub(row.ApiToken.LastUsedAt.Time) > touchInterval {
		if err := s.q.TouchAPIToken(ctx, row.ApiToken.ID); err != nil {
			return nil, fmt.Errorf("touch api token: %w", err)
		}
	}
	return &APIPrincipal{Token: row.ApiToken, User: row.User}, nil
}
