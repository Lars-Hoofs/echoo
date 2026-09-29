package automation

import (
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Facts is what conditions are evaluated against: the conversation and, for the message
// fields, the inbound message that caused the trigger (or the latest one).
type Facts struct {
	ConversationSubject string
	MailboxID           pgtype.UUID
	Status              string
	Priority            string
	HasAssignee         bool
	HasTeam             bool
	Organization        string
	Labels              map[string]bool

	// HasMessage is false when the conversation has no inbound email yet.
	HasMessage    bool
	FromAddress   string
	Body          string
	HasAttachment bool
	AutoSubmitted bool
	ReceivedAt    time.Time

	// WithinHours reports whether t is inside the business hours that apply to the mailbox.
	WithinHours func(t time.Time) bool
	Now         time.Time
}

// Match reports whether the conditions hold. An empty tree matches.
func (c Conditions) Match(f *Facts) bool {
	if c.root.Match == "" {
		return true
	}
	return c.root.matchGroup(f)
}

func (n *Node) matchGroup(f *Facts) bool {
	if len(n.Items) == 0 {
		return true
	}
	all := n.Match == matchAll
	for i := range n.Items {
		item := &n.Items[i]
		var ok bool
		if item.isGroup() {
			ok = item.matchGroup(f)
		} else {
			ok = item.matchCondition(f)
		}
		if all && !ok {
			return false
		}
		if !all && ok {
			return true
		}
	}
	return all
}

func (n *Node) matchCondition(f *Facts) bool {
	return n.evaluate(f) != n.Negate
}

func (n *Node) evaluate(f *Facts) bool {
	switch n.Field {
	case "mailbox":
		return slices.Contains(n.ids, f.MailboxID.String())
	case "status":
		return slices.Contains(n.ids, f.Status)
	case "priority":
		return slices.Contains(n.ids, f.Priority)
	case fieldLabelName:
		for _, id := range n.ids {
			if f.Labels[id] {
				return true
			}
		}
		return false
	case "subject":
		return matchText(n.Op, f.ConversationSubject, n.text)
	case "organization":
		return f.Organization != "" && matchText(n.Op, f.Organization, n.text)
	case "from_address":
		return f.HasMessage && matchText(n.Op, f.FromAddress, n.text)
	case "from_domain":
		return f.HasMessage && matchText(n.Op, domainOf(f.FromAddress), n.text)
	case "body":
		return f.HasMessage && matchText(n.Op, f.Body, n.text)
	case "has_attachment":
		return (f.HasMessage && f.HasAttachment) == n.flag
	case "auto_submitted":
		return (f.HasMessage && f.AutoSubmitted) == n.flag
	case "has_assignee":
		return f.HasAssignee == n.flag
	case "has_team":
		return f.HasTeam == n.flag
	case "within_business_hours":
		at := f.Now
		if f.HasMessage && !f.ReceivedAt.IsZero() {
			at = f.ReceivedAt
		}
		return f.WithinHours(at) == n.flag
	}
	return false
}

// matchText compares case-insensitively; want is already lower case.
func matchText(op, have, want string) bool {
	have = strings.ToLower(have)
	switch op {
	case opEquals:
		return have == want
	case opContains:
		return strings.Contains(have, want)
	case opStartsWith:
		return strings.HasPrefix(have, want)
	case opEndsWith:
		return strings.HasSuffix(have, want)
	}
	return false
}

func domainOf(addr string) string {
	_, domain, ok := strings.Cut(strings.ToLower(addr), "@")
	if !ok {
		return ""
	}
	return domain
}
