// Package retention deletes data once its retention period is over: closed conversations,
// attachments, spam, the audit log and abandoned composer uploads. Every deletion is batched,
// audited with counts, and removes files only through the reference-checked deletion queue.
package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

const (
	settingsKey = "retention"
	lastRunKey  = "retention_last_run"

	// DefaultAuditMonths is how long the audit log is kept until an admin says otherwise.
	DefaultAuditMonths = 12
	// DefaultTrashDays is how long the trash keeps conversations until an admin says otherwise.
	DefaultTrashDays = 30
	// MinAuditMonths keeps the API above the 60-day floor that audit_log_purge enforces.
	MinAuditMonths = 3
	maxMonths      = 120
	maxSpamDays    = 3650
)

// Periods are the retention periods of one scope. A nil period keeps that kind of data forever.
type Periods struct {
	ClosedConversationMonths *int `json:"closed_conversation_months"`
	AttachmentMonths         *int `json:"attachment_months"`
	SpamDays                 *int `json:"spam_days"`
	TrashDays                *int `json:"trash_days"`
}

// MailboxPeriods overrides the workspace periods for one mailbox, as a whole: a nil period
// here keeps the data forever even when the workspace period is set.
type MailboxPeriods struct {
	MailboxID pgtype.UUID `json:"mailbox_id"`
	Periods
}

// Settings is what the retention page edits.
type Settings struct {
	Global Periods `json:"global"`
	// AuditMonths applies to the whole audit log; nil keeps it forever.
	AuditMonths *int             `json:"audit_months"`
	Mailboxes   []MailboxPeriods `json:"mailboxes"`
}

// LastRun is the outcome of the previous daily purge.
type LastRun struct {
	At                 time.Time `json:"at"`
	ClosedConversation int64     `json:"closed_conversations"`
	SpamConversations  int64     `json:"spam_conversations"`
	TrashConversations int64     `json:"trash_conversations"`
	Attachments        int64     `json:"attachments"`
	AuditEntries       int64     `json:"audit_entries"`
}

// stored is the part of Settings that lives in the settings table; mailbox overrides have
// their own table so they go away with their mailbox.
type stored struct {
	Global      Periods `json:"global"`
	AuditMonths *int    `json:"audit_months"`
}

// Load returns the stored settings, or the defaults for a workspace that never saved any.
func Load(ctx context.Context, q *dbq.Queries) (Settings, error) {
	s := Settings{Global: Periods{TrashDays: new(DefaultTrashDays)}, AuditMonths: new(DefaultAuditMonths), Mailboxes: []MailboxPeriods{}}
	raw, err := q.GetSetting(ctx, settingsKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Settings{}, fmt.Errorf("load retention settings: %w", err)
	default:
		// Settings saved before the trash existed have no trash_days key and get the default;
		// an explicit null keeps the trash forever.
		st := stored{Global: Periods{TrashDays: new(DefaultTrashDays)}}
		if err := json.Unmarshal(raw, &st); err != nil {
			return Settings{}, fmt.Errorf("decode retention settings: %w", err)
		}
		s.Global, s.AuditMonths = st.Global, st.AuditMonths
	}
	rows, err := q.RetentionListPolicies(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("load retention policies: %w", err)
	}
	for _, r := range rows {
		s.Mailboxes = append(s.Mailboxes, MailboxPeriods{MailboxID: r.MailboxID, Periods: Periods{
			ClosedConversationMonths: intPtr(r.ClosedConversationMonths),
			AttachmentMonths:         intPtr(r.AttachmentMonths),
			SpamDays:                 intPtr(r.SpamDays),
			TrashDays:                intPtr(r.TrashDays),
		}})
	}
	slices.SortFunc(s.Mailboxes, func(a, b MailboxPeriods) int { return slices.Compare(a.MailboxID.Bytes[:], b.MailboxID.Bytes[:]) })
	return s, nil
}

// LoadLastRun returns the previous purge's outcome, or nil when none ran yet.
func LoadLastRun(ctx context.Context, q *dbq.Queries) (*LastRun, error) {
	raw, err := q.GetSetting(ctx, lastRunKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load last retention run: %w", err)
	}
	var lr LastRun
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, fmt.Errorf("decode last retention run: %w", err)
	}
	return &lr, nil
}

// Save replaces the stored settings. Call it inside the transaction that also writes the audit
// entry, after Validate.
func Save(ctx context.Context, q *dbq.Queries, s Settings, actor pgtype.UUID) error {
	raw, err := json.Marshal(stored{Global: s.Global, AuditMonths: s.AuditMonths})
	if err != nil {
		return err
	}
	if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: settingsKey, Value: raw, UpdatedBy: actor}); err != nil {
		return fmt.Errorf("save retention settings: %w", err)
	}
	if err := q.RetentionDeletePolicies(ctx); err != nil {
		return fmt.Errorf("replace retention policies: %w", err)
	}
	for _, m := range s.Mailboxes {
		err := q.RetentionInsertPolicy(ctx, dbq.RetentionInsertPolicyParams{
			MailboxID:                m.MailboxID,
			ClosedConversationMonths: int4(m.ClosedConversationMonths),
			AttachmentMonths:         int4(m.AttachmentMonths),
			SpamDays:                 int4(m.SpamDays),
			TrashDays:                int4(m.TrashDays),
			UpdatedBy:                actor,
		})
		if err != nil {
			return fmt.Errorf("save retention policy: %w", err)
		}
	}
	return nil
}

// Validate returns field errors keyed by JSON path; the map is empty when s is acceptable.
// mailboxIDs are the mailboxes that exist.
func Validate(s Settings, mailboxIDs []pgtype.UUID) map[string]string {
	problems := map[string]string{}
	checkPeriods(problems, "global", s.Global)
	if s.AuditMonths != nil && (*s.AuditMonths < MinAuditMonths || *s.AuditMonths > maxMonths) {
		problems["audit_months"] = fmt.Sprintf("must be between %d and %d", MinAuditMonths, maxMonths)
	}
	seen := map[pgtype.UUID]bool{}
	for i, m := range s.Mailboxes {
		field := fmt.Sprintf("mailboxes[%d]", i)
		switch {
		case !slices.Contains(mailboxIDs, m.MailboxID):
			problems[field+".mailbox_id"] = "unknown mailbox"
		case seen[m.MailboxID]:
			problems[field+".mailbox_id"] = "listed twice"
		}
		seen[m.MailboxID] = true
		checkPeriods(problems, field, m.Periods)
	}
	return problems
}

func checkPeriods(problems map[string]string, prefix string, p Periods) {
	inRange := func(v *int, name string, hi int) {
		if v != nil && (*v < 1 || *v > hi) {
			problems[prefix+"."+name] = fmt.Sprintf("must be between 1 and %d", hi)
		}
	}
	inRange(p.ClosedConversationMonths, "closed_conversation_months", maxMonths)
	inRange(p.AttachmentMonths, "attachment_months", maxMonths)
	inRange(p.SpamDays, "spam_days", maxSpamDays)
	inRange(p.TrashDays, "trash_days", maxSpamDays)
}

// effective returns the periods that apply to a mailbox.
func (s Settings) effective(mailbox pgtype.UUID) Periods {
	for _, m := range s.Mailboxes {
		if m.MailboxID == mailbox {
			return m.Periods
		}
	}
	return s.Global
}

func intPtr(v pgtype.Int4) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

func int4(v *int) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true} //nolint:gosec // Validate bounds every period far below int32
}
