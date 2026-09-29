package threading

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"echoo/internal/mail"
)

const (
	subjectWindow = 30 * 24 * time.Hour
	closedGrace   = 7 * 24 * time.Hour
	maxHeaderRefs = 50
	statusClosed  = "closed"
	statusSpam    = "spam"
)

// ID is a UUID in the byte form used by package mail.
type ID = [16]byte

// Reason says why Resolve chose its outcome. It is meant for logs and tests.
type Reason string

const (
	ReasonOwnMessageID    Reason = "own_message_id"
	ReasonInReplyTo       Reason = "in_reply_to"
	ReasonReferences      Reason = "references"
	ReasonOutboundToken   Reason = "outbound_token"
	ReasonSubject         Reason = "subject"
	ReasonNewConversation Reason = "new_conversation"
)

// Candidate is an existing conversation that may match by subject.
type Candidate struct {
	ID            ID
	Status        string
	LastMessageAt time.Time
	// ResolvedAt is when the conversation was closed; zero when unknown.
	ResolvedAt time.Time
	// Participants are the lower-cased addresses that appear in the conversation, own
	// addresses included.
	Participants []string
}

// Lookup is the read access Resolve needs. Implementations must scope every call to the mailbox.
type Lookup interface {
	// ConversationsByRefs maps string(hash) to the conversation for every hash present in thread_refs.
	ConversationsByRefs(ctx context.Context, mailbox ID, hashes [][]byte) (map[string]ID, error)
	// ConversationInMailbox reports whether the conversation exists and belongs to the mailbox.
	ConversationInMailbox(ctx context.Context, mailbox, conversation ID) (bool, error)
	// SubjectCandidates returns conversations of the mailbox with this normalized subject whose
	// last message is not older than notBefore. Further filtering is done by Resolve.
	SubjectCandidates(ctx context.Context, mailbox ID, normalizedSubject string, notBefore time.Time) ([]Candidate, error)
}

type Input struct {
	MailboxID    ID
	OwnAddresses []string
	Message      *mail.Parsed
	ReceivedAt   time.Time
}

type Decision struct {
	// ConversationID is the zero value when a new conversation must be created.
	ConversationID ID
	Reason         Reason
	// RefHashes are the message-id hashes (own ID, In-Reply-To, References) to store in
	// thread_refs for the conversation, so replies that arrive before their parent thread.
	RefHashes [][]byte
	// SubjectNormalized is the value for conversations.subject_normalized of a new conversation.
	SubjectNormalized string
}

// IsNew reports whether a new conversation must be created.
func (d Decision) IsNew() bool { return d.Reason == ReasonNewConversation }

// Resolve implements the order of checks in docs/architecture.md section 3.2.
func Resolve(ctx context.Context, lookup Lookup, in Input) (Decision, error) {
	msg := in.Message
	normalized, firstPrefix := normalizeSubject(msg.Subject)
	dec := Decision{SubjectNormalized: normalized}

	ownID := mail.NormalizeMessageID(msg.MessageID)
	replyIDs := cleanIDs(msg.InReplyTo)
	refIDs := cleanIDs(msg.References)
	if len(refIDs) > maxHeaderRefs {
		refIDs = refIDs[len(refIDs)-maxHeaderRefs:]
	}
	// Newest reference first: the closest ancestor is the best evidence.
	slices.Reverse(refIDs)

	ordered := make([]string, 0, 1+len(replyIDs)+len(refIDs))
	if ownID != "" {
		ordered = append(ordered, ownID)
	}
	ordered = append(ordered, replyIDs...)
	ordered = append(ordered, refIDs...)
	dec.RefHashes = uniqueHashes(ordered)

	known, err := lookup.ConversationsByRefs(ctx, in.MailboxID, dec.RefHashes)
	if err != nil {
		return Decision{}, fmt.Errorf("look up thread refs: %w", err)
	}
	join := func(id string, reason Reason) (Decision, bool) {
		conv, ok := known[string(mail.HashMessageID(id))]
		if !ok {
			return Decision{}, false
		}
		dec.ConversationID, dec.Reason = conv, reason
		return dec, true
	}
	if ownID != "" {
		if d, ok := join(ownID, ReasonOwnMessageID); ok {
			return d, nil
		}
	}
	for _, id := range replyIDs {
		if d, ok := join(id, ReasonInReplyTo); ok {
			return d, nil
		}
	}
	for _, id := range refIDs {
		if d, ok := join(id, ReasonReferences); ok {
			return d, nil
		}
	}

	tokenIDs := append(append(slices.Clone(replyIDs), refIDs...), ownID)
	for _, id := range tokenIDs {
		conv, ok := mail.ConversationFromMessageID(id)
		if !ok {
			continue
		}
		owned, err := lookup.ConversationInMailbox(ctx, in.MailboxID, conv)
		if err != nil {
			return Decision{}, fmt.Errorf("check outbound token conversation: %w", err)
		}
		if owned {
			dec.ConversationID, dec.Reason = conv, ReasonOutboundToken
			return dec, nil
		}
	}

	dec.Reason = ReasonNewConversation
	if forwardPrefixes[firstPrefix] || IsGenericSubject(normalized) {
		return dec, nil
	}
	conv, ok, err := subjectMatch(ctx, lookup, in, normalized)
	if err != nil {
		return Decision{}, err
	}
	if ok {
		dec.ConversationID, dec.Reason = conv, ReasonSubject
	}
	return dec, nil
}

func subjectMatch(ctx context.Context, lookup Lookup, in Input, normalized string) (ID, bool, error) {
	own := make(map[string]bool, len(in.OwnAddresses))
	for _, a := range in.OwnAddresses {
		own[strings.ToLower(a)] = true
	}
	external := func(addrs []string) map[string]bool {
		set := make(map[string]bool, len(addrs))
		for _, a := range addrs {
			if a = strings.ToLower(a); a != "" && !own[a] {
				set[a] = true
			}
		}
		return set
	}
	msg := in.Message
	mine := []string{msg.From.Address}
	for _, group := range [][]mail.Address{msg.To, msg.Cc} {
		for _, a := range group {
			mine = append(mine, a.Address)
		}
	}
	senders := external(mine)
	if len(senders) == 0 {
		return ID{}, false, nil
	}

	cands, err := lookup.SubjectCandidates(ctx, in.MailboxID, normalized, in.ReceivedAt.Add(-subjectWindow))
	if err != nil {
		return ID{}, false, fmt.Errorf("look up subject candidates: %w", err)
	}
	var best *Candidate
	for i := range cands {
		c := &cands[i]
		if c.Status == statusSpam || c.LastMessageAt.Before(in.ReceivedAt.Add(-subjectWindow)) {
			continue
		}
		if c.Status == statusClosed {
			closedAt := c.ResolvedAt
			if closedAt.IsZero() {
				closedAt = c.LastMessageAt
			}
			if closedAt.Before(in.ReceivedAt.Add(-closedGrace)) {
				continue
			}
		}
		if !overlaps(senders, external(c.Participants)) {
			continue
		}
		if best != nil && !c.LastMessageAt.After(best.LastMessageAt) {
			continue
		}
		best = c
	}
	if best == nil {
		return ID{}, false, nil
	}
	return best.ID, true, nil
}

func overlaps(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// cleanIDs drops values that are not plausible Message-IDs, so garbage in References is ignored.
func cleanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if n := mail.NormalizeMessageID(id); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func uniqueHashes(ids []string) [][]byte {
	seen := make(map[string]bool, len(ids))
	out := make([][]byte, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, mail.HashMessageID(id))
	}
	return out
}
