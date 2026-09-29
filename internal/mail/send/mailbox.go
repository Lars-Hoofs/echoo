package send

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/mailauth"
)

// TokenSource returns a valid OAuth access token for a mailbox.
type TokenSource interface {
	AccessToken(ctx context.Context, mailboxID pgtype.UUID) (string, error)
}

// MailboxSMTP returns the SMTP settings of a mailbox with its credentials filled in: the
// decrypted password, or a fresh OAuth access token. Failures that no retry can fix wrap
// errUnsendable.
func MailboxSMTP(ctx context.Context, keys *keyring.Keyring, tokens TokenSource, tlsCfg *tls.Config, mb dbq.Mailbox) (SMTPConfig, error) {
	if mb.SmtpHost == "" {
		return SMTPConfig{}, fmt.Errorf("%w: mailbox has no SMTP server", errUnsendable)
	}
	cfg := SMTPConfig{
		Host: mb.SmtpHost, Port: int(mb.SmtpPort), TLS: TLSMode(mb.SmtpTls),
		Username: mb.SmtpUsername, AllowInternal: mb.AllowInternalHost, TLSConfig: tlsCfg,
	}
	if mb.AuthType != "password" {
		if tokens == nil {
			return SMTPConfig{}, fmt.Errorf("%w: OAuth mailboxes are not configured", errUnsendable)
		}
		token, err := tokens.AccessToken(ctx, mb.ID)
		if errors.Is(err, mailauth.ErrReauthRequired) {
			return SMTPConfig{}, fmt.Errorf("%w: mail account must be reconnected", errUnsendable)
		}
		if err != nil {
			return SMTPConfig{}, err
		}
		cfg.AccessToken = token
		return cfg, nil
	}
	if len(mb.SmtpSecretEnc) == 0 {
		return cfg, nil
	}
	plain, err := keys.Decrypt(mb.SmtpSecretEnc, keyring.AAD("mailboxes", "smtp_secret_enc", mb.ID.String()))
	if err != nil {
		return SMTPConfig{}, fmt.Errorf("%w: cannot decrypt smtp_secret", errUnsendable)
	}
	cfg.Password = string(plain)
	return cfg, nil
}

// IsUnsendable reports whether err says the mailbox can never send as configured, as opposed
// to a failure worth retrying.
func IsUnsendable(err error) bool { return errors.Is(err, errUnsendable) }
