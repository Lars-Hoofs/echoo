package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

// Impact is what a purge of one kind would remove right now.
type Impact struct {
	Conversations   int64 `json:"conversations"`
	Messages        int64 `json:"messages"`
	Attachments     int64 `json:"attachments"`
	AttachmentBytes int64 `json:"attachment_bytes"`
}

// Preview is the dry run of Run for a set of settings that may not be saved yet.
type Preview struct {
	ClosedConversations Impact `json:"closed_conversations"`
	SpamConversations   Impact `json:"spam_conversations"`
	// Attachments are files removed while their message stays. A file that is also inside a
	// conversation above is counted in both places.
	Attachments  Impact `json:"attachments"`
	AuditEntries int64  `json:"audit_entries"`
}

// Preview counts what Run would delete with the given settings, without deleting anything.
func (s *Service) Preview(ctx context.Context, set Settings) (Preview, error) {
	var out Preview
	mailboxes, err := s.q.RetentionListMailboxIDs(ctx)
	if err != nil {
		return out, fmt.Errorf("list mailboxes: %w", err)
	}
	for _, mb := range mailboxes {
		p := set.effective(mb)
		if p.ClosedConversationMonths != nil {
			if err := s.previewConversations(ctx, &out.ClosedConversations, mb, "closed", s.months(*p.ClosedConversationMonths)); err != nil {
				return out, err
			}
		}
		if p.SpamDays != nil {
			if err := s.previewConversations(ctx, &out.SpamConversations, mb, "spam", s.days(*p.SpamDays)); err != nil {
				return out, err
			}
		}
		if p.AttachmentMonths != nil {
			row, err := s.q.RetentionPreviewAttachments(ctx, dbq.RetentionPreviewAttachmentsParams{MailboxID: mb, Before: ts(s.months(*p.AttachmentMonths))})
			if err != nil {
				return out, fmt.Errorf("preview attachments: %w", err)
			}
			out.Attachments.Attachments += row.Attachments
			out.Attachments.AttachmentBytes += row.AttachmentBytes
		}
	}
	if set.AuditMonths != nil {
		n, err := s.q.RetentionPreviewAudit(ctx, ts(s.months(*set.AuditMonths)))
		if err != nil {
			return out, fmt.Errorf("preview audit log: %w", err)
		}
		out.AuditEntries = n
	}
	return out, nil
}

func (s *Service) previewConversations(ctx context.Context, into *Impact, mailbox pgtype.UUID, status string, before time.Time) error {
	row, err := s.q.RetentionPreviewConversations(ctx, dbq.RetentionPreviewConversationsParams{MailboxID: mailbox, Status: status, Before: ts(before)})
	if err != nil {
		return fmt.Errorf("preview %s conversations: %w", status, err)
	}
	into.Conversations += row.Conversations
	into.Messages += row.Messages
	into.Attachments += row.Attachments
	into.AttachmentBytes += row.AttachmentBytes
	return nil
}
