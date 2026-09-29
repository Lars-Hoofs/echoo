package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/webhooks"
)

const previewRunes = 200

// freeMailDomains never identify an organization.
var freeMailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true,
	"live.com": true, "icloud.com": true, "me.com": true, "yahoo.com": true,
	"ziggo.nl": true, "kpnmail.nl": true, "planet.nl": true, "hetnet.nl": true,
	"home.nl": true, "xs4all.nl": true, "telenet.be": true, "skynet.be": true,
	"gmx.de": true, "gmx.net": true, "web.de": true, "proton.me": true, "protonmail.com": true,
}

// IsFreeMailDomain reports whether domain is a public mailbox provider, which never identifies
// an organization.
func IsFreeMailDomain(domain string) bool { return freeMailDomains[domain] }

// senderOf picks the address a conversation is attributed to when From is unusable.
func senderOf(p *mail.Parsed) mail.Address {
	if p.From.Address != "" {
		return p.From
	}
	if p.Sender != nil && p.Sender.Address != "" {
		return *p.Sender
	}
	for _, a := range p.ReplyTo {
		if a.Address != "" {
			return a
		}
	}
	return mail.Address{}
}

// upsertSender returns the contact for the sender, or an invalid UUID when there is none or
// the sender is the mailbox itself. Advisory locks serialize concurrent jobs that would
// otherwise both create the same contact or organization; the locks are always taken in the
// same order (contact, then organization).
func (w *Worker) upsertSender(ctx context.Context, q *dbq.Queries, from mail.Address, ownAddress string) (pgtype.UUID, error) {
	email := strings.ToLower(strings.TrimSpace(from.Address))
	if email == "" || email == strings.ToLower(ownAddress) {
		return pgtype.UUID{}, nil
	}
	if err := q.IngestAdvisoryLock(ctx, "contact:"+email); err != nil {
		return pgtype.UUID{}, fmt.Errorf("lock contact: %w", err)
	}
	org, err := w.organizationFor(ctx, q, email)
	if err != nil {
		return pgtype.UUID{}, err
	}
	name := strings.TrimSpace(from.Name)

	id, err := q.IngestGetContactByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = q.IngestCreateContact(ctx, dbq.IngestCreateContactParams{Name: name, OrganizationID: org})
		if err != nil {
			return pgtype.UUID{}, fmt.Errorf("create contact: %w", err)
		}
		if err := q.IngestCreateContactAddress(ctx, dbq.IngestCreateContactAddressParams{ContactID: id, Email: email}); err != nil {
			return pgtype.UUID{}, fmt.Errorf("create contact address: %w", err)
		}
		if err := webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.ContactCreated, ContactID: id}); err != nil {
			return pgtype.UUID{}, err
		}
		return id, nil
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("find contact: %w", err)
	}
	if err := q.IngestFillContact(ctx, dbq.IngestFillContactParams{ID: id, Name: name, OrganizationID: org}); err != nil {
		return pgtype.UUID{}, fmt.Errorf("update contact: %w", err)
	}
	return id, nil
}

func (w *Worker) organizationFor(ctx context.Context, q *dbq.Queries, email string) (pgtype.UUID, error) {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return pgtype.UUID{}, nil
	}
	domain := email[at+1:]
	if domain == "" || freeMailDomains[domain] {
		return pgtype.UUID{}, nil
	}
	if err := q.IngestAdvisoryLock(ctx, "organization:"+domain); err != nil {
		return pgtype.UUID{}, fmt.Errorf("lock organization: %w", err)
	}
	id, err := q.IngestFindOrganizationByDomain(ctx, domain)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = q.IngestCreateOrganization(ctx, domain)
		if err != nil {
			return pgtype.UUID{}, fmt.Errorf("create organization: %w", err)
		}
		return id, nil
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("find organization: %w", err)
	}
	return id, nil
}

// preview is the start of the message text without quoted lines, on one line.
func preview(text string) string {
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ">") {
			continue
		}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(strings.Join(strings.Fields(line), " "))
		if utf8.RuneCountInString(out.String()) >= previewRunes {
			break
		}
	}
	s := out.String()
	if utf8.RuneCountInString(s) > previewRunes {
		s = string([]rune(s)[:previewRunes])
	}
	return strings.TrimSpace(s)
}
