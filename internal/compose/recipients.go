package compose

import (
	"errors"
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"

	"echoo/internal/mail"
)

// MaxRecipients caps To, Cc and Bcc together, so one request cannot turn into a mass mailing.
const MaxRecipients = 50

// NormalizeAddresses trims and lower-cases addresses, validates that each is a plain address
// and removes duplicates. The send package repeats the header-injection checks; this gives the
// caller an error naming the field instead of a failed transaction.
func NormalizeAddresses(list []mail.Address) ([]mail.Address, error) {
	out := make([]mail.Address, 0, len(list))
	seen := map[string]bool{}
	for _, a := range list {
		addr := strings.ToLower(strings.TrimSpace(a.Address))
		parsed, err := netmail.ParseAddress(addr)
		if err != nil || parsed.Name != "" || parsed.Address != addr {
			return nil, fmt.Errorf("%q is not an email address", a.Address)
		}
		name := strings.TrimSpace(a.Name)
		if strings.ContainsAny(name, "\r\n\x00") {
			return nil, errors.New("name contains a line break")
		}
		if seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, mail.Address{Name: name, Address: addr})
	}
	return out, nil
}

// Message is the part of a stored message that decides who a reply goes to.
type Message struct {
	From            mail.Address
	To, Cc, ReplyTo []mail.Address
}

// Customer is who a reply to m goes to: the Reply-To, else the From.
func (m Message) Customer() mail.Address {
	if len(m.ReplyTo) > 0 {
		return m.ReplyTo[0]
	}
	return m.From
}

// HasUnknownSender reports whether the From or any Reply-To of m is not in known. A message
// counts as written by a stranger when either is, so a stranger cannot pass as a customer by
// naming the customer in Reply-To.
func (m Message) HasUnknownSender(known map[string]bool) bool {
	for _, a := range append([]mail.Address{m.From}, m.ReplyTo...) {
		if !known[strings.ToLower(a.Address)] {
			return true
		}
	}
	return false
}

// ReplyRecipients picks the default To and Cc. The customer is the Reply-To, else the From, of
// the last inbound message; in a conversation with no inbound mail yet it is whoever we wrote
// to last. Cc carries the other participants of the latest message. Our own addresses never
// appear, so a reply does not go to a mailbox of ours and loop.
//
// known holds the lower-cased addresses that are participants of the conversation. When the
// latest inbound message comes from someone else, the reply is never redirected to that
// sender: To stays with the customer of knownInbound (the latest inbound message from a known
// participant, nil if there is none, in which case we fall back to whoever we wrote to), and
// the newcomer and any unknown addresses on the message come back as suggested Cc, for the
// agent to add on purpose.
func ReplyRecipients(last Message, lastInbound, knownInbound *Message, known, own map[string]bool) (to, cc, suggested []mail.Address) {
	unknown := func(a mail.Address) bool { return !known[strings.ToLower(a.Address)] }
	strangerWrote := lastInbound != nil && lastInbound.HasUnknownSender(known)

	switch {
	case strangerWrote && knownInbound != nil:
		to = []mail.Address{knownInbound.Customer()}
	case strangerWrote:
		to = last.To
	case lastInbound != nil:
		to = []mail.Address{lastInbound.Customer()}
	default:
		to = last.To
	}
	taken := map[string]bool{}
	pick := func(list []mail.Address) []mail.Address {
		var out []mail.Address
		for _, a := range list {
			key := strings.ToLower(a.Address)
			if a.Address == "" || own[key] || taken[key] {
				continue
			}
			taken[key] = true
			out = append(out, a)
		}
		return out
	}
	to = pick(to)
	parties := append(append([]mail.Address{}, last.To...), last.Cc...)
	if !strangerWrote {
		return to, pick(parties), nil
	}
	var kept, extra []mail.Address
	for _, a := range parties {
		if unknown(a) {
			extra = append(extra, a)
		} else {
			kept = append(kept, a)
		}
	}
	cc = pick(kept)
	senders := append(append([]mail.Address{lastInbound.From}, lastInbound.ReplyTo...), extra...)
	suggested = pick(senders)
	return to, cc, suggested
}

var replyPrefix = regexp.MustCompile(`(?i)^\s*(re|aw|antw)\s*:`)
var forwardPrefix = regexp.MustCompile(`(?i)^\s*(fwd?|doorst|doorgestuurd|wg)\s*:`)

// ReplySubject prefixes "Re: " unless the subject already is a reply.
func ReplySubject(subject string) string {
	if replyPrefix.MatchString(subject) {
		return strings.TrimSpace(subject)
	}
	return strings.TrimSpace("Re: " + strings.TrimSpace(subject))
}

// ForwardSubject prefixes "Fwd: " unless the subject already is a forward.
func ForwardSubject(subject string) string {
	if forwardPrefix.MatchString(subject) {
		return strings.TrimSpace(subject)
	}
	return strings.TrimSpace("Fwd: " + strings.TrimSpace(subject))
}
