package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/sanitize"
)

type warningJSON struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// messageRenderInfo is what the conversation detail says about how a message will render.
type messageRenderInfo struct {
	hasHTML       bool
	blockedImages int
	warnings      []warningJSON
}

func (m messageRenderInfo) renderURL(id pgtype.UUID) string {
	if !m.hasHTML {
		return ""
	}
	return "/render/messages/" + uuidStr(id)
}

// messageRenderInfos sanitizes each HTML body once more, only to count what the render route
// will block and to collect link warnings; the output itself is not kept.
func (s *Server) messageRenderInfos(ctx context.Context, conv dbq.ListConversationsByIDRow) (map[pgtype.UUID]messageRenderInfo, error) {
	rows, err := s.q.ListConversationRenderInfo(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("load render info: %w", err)
	}
	out := make(map[pgtype.UUID]messageRenderInfo, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	patterns, err := s.q.ListSenderImagePatterns(ctx, conv.MailboxID)
	if err != nil {
		return nil, fmt.Errorf("list image allowlist: %w", err)
	}
	mailbox, err := s.q.GetMailboxIdentity(ctx, conv.MailboxID)
	if err != nil {
		return nil, fmt.Errorf("load mailbox identity: %w", err)
	}

	for _, m := range rows {
		info := messageRenderInfo{hasHTML: m.BodyHtml != "", warnings: []warningJSON{}}
		var linkWarnings []sanitize.Warning
		if info.hasHTML {
			var opts sanitize.Options
			if senderAllowed(patterns, m.FromAddr) {
				opts.ProxyURL = func(remote string) string { return remote }
			}
			res, err := sanitize.HTML(m.BodyHtml, opts)
			switch {
			case errors.Is(err, sanitize.ErrUnparseable):
				// Rendered as text; there is nothing to count.
			case err != nil:
				return nil, fmt.Errorf("sanitize message %s: %w", uuidStr(m.ID), err)
			default:
				info.blockedImages = res.BlockedImages
				linkWarnings = sanitize.LinkWarnings(res.Mismatches)
			}
		}
		var header []sanitize.Warning
		if m.Direction.String == "in" {
			replyTo, err := decodeAddresses(m.ReplyTo)
			if err != nil {
				return nil, fmt.Errorf("decode reply_to of message %s: %w", uuidStr(m.ID), err)
			}
			sender := sanitize.Sender{
				FromName: m.FromName, FromAddr: m.FromAddr, AuthResults: m.AuthResults,
				MailboxAddr: mailbox.EmailAddress, MailboxName: mailbox.DisplayName,
			}
			for _, a := range replyTo {
				sender.ReplyTo = append(sender.ReplyTo, a.Address)
			}
			header = sanitize.Analyze(sender)
		}
		for _, w := range append(header, linkWarnings...) {
			info.warnings = append(info.warnings, warningJSON{Kind: w.Kind, Detail: w.Detail})
		}
		out[m.ID] = info
	}
	return out, nil
}
