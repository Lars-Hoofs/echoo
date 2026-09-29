package threading

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"echoo/internal/mail"
)

var (
	now      = time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	mailboxA = ID{0xa}
	mailboxB = ID{0xb}
	convX    = ID{1}
	convY    = ID{2}
	convZ    = ID{3}
	newConv  = ID{}
	own      = []string{"support@echoo.test"}
)

func daysAgo(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }

type fakeConv struct {
	mailbox    ID
	normalized string
	status     string
	lastAt     time.Time
	resolvedAt time.Time
	people     []string
}

type fakeLookup struct {
	refs  map[[2]string]ID // mailbox + hash -> conversation
	convs map[ID]*fakeConv
	next  byte
	err   error
}

func newFake() *fakeLookup {
	return &fakeLookup{refs: map[[2]string]ID{}, convs: map[ID]*fakeConv{}, next: 100}
}

func (f *fakeLookup) addConv(id ID, c fakeConv) *fakeLookup {
	if c.status == "" {
		c.status = "open"
	}
	if c.mailbox == (ID{}) {
		c.mailbox = mailboxA
	}
	if c.lastAt.IsZero() {
		c.lastAt = daysAgo(1)
	}
	f.convs[id] = &c
	return f
}

func (f *fakeLookup) addRef(mailbox ID, messageID string, conv ID) *fakeLookup {
	f.refs[[2]string{string(mailbox[:]), string(mail.HashMessageID(messageID))}] = conv
	return f
}

func (f *fakeLookup) ConversationsByRefs(_ context.Context, mailbox ID, hashes [][]byte) (map[string]ID, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]ID{}
	for _, h := range hashes {
		if c, ok := f.refs[[2]string{string(mailbox[:]), string(h)}]; ok {
			out[string(h)] = c
		}
	}
	return out, nil
}

func (f *fakeLookup) ConversationInMailbox(_ context.Context, mailbox, conv ID) (bool, error) {
	c, ok := f.convs[conv]
	return ok && c.mailbox == mailbox, f.err
}

func (f *fakeLookup) SubjectCandidates(_ context.Context, mailbox ID, subject string, notBefore time.Time) ([]Candidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Candidate
	for id, c := range f.convs {
		if c.mailbox != mailbox || c.normalized != subject || c.lastAt.Before(notBefore) {
			continue
		}
		out = append(out, Candidate{ID: id, Status: c.status, LastMessageAt: c.lastAt, ResolvedAt: c.resolvedAt, Participants: c.people})
	}
	return out, nil
}

// apply stores a decision the way the ingest transaction would, so sequences of messages can be replayed.
func (f *fakeLookup) apply(mailbox ID, in Input, d Decision) ID {
	id := d.ConversationID
	if d.IsNew() {
		f.next++
		id = ID{f.next}
		f.convs[id] = &fakeConv{mailbox: mailbox, normalized: d.SubjectNormalized, status: "open"}
	}
	c := f.convs[id]
	c.lastAt = in.ReceivedAt
	c.people = append(c.people, in.Message.From.Address)
	for _, a := range in.Message.To {
		c.people = append(c.people, a.Address)
	}
	for _, h := range d.RefHashes {
		key := [2]string{string(mailbox[:]), string(h)}
		if _, ok := f.refs[key]; !ok {
			f.refs[key] = id
		}
	}
	return id
}

func addr(a string) mail.Address { return mail.Address{Address: a} }

func addrs(list ...string) []mail.Address {
	out := make([]mail.Address, len(list))
	for i, a := range list {
		out[i] = addr(a)
	}
	return out
}

func msg(id, subject, from string, mut ...func(*mail.Parsed)) *mail.Parsed {
	p := &mail.Parsed{MessageID: id, Subject: subject, From: addr(from), To: addrs(own[0])}
	for _, m := range mut {
		m(p)
	}
	return p
}

func irt(ids ...string) func(*mail.Parsed)  { return func(p *mail.Parsed) { p.InReplyTo = ids } }
func refs(ids ...string) func(*mail.Parsed) { return func(p *mail.Parsed) { p.References = ids } }
func to(a ...string) func(*mail.Parsed)     { return func(p *mail.Parsed) { p.To = addrs(a...) } }
func cc(a ...string) func(*mail.Parsed)     { return func(p *mail.Parsed) { p.Cc = addrs(a...) } }
func autoReply(p *mail.Parsed)              { p.AutoSubmitted = true }

func input(m *mail.Parsed) Input {
	return Input{MailboxID: mailboxA, OwnAddresses: own, Message: m, ReceivedAt: now}
}

func outboundID(t *testing.T, conv ID) string {
	t.Helper()
	id, err := mail.NewOutboundMessageID(conv, "echoo.test")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// forgedID has the shape of an outbound id for conv but a tag the sender cannot compute.
func forgedID(conv ID) string {
	return "echoo." + hex.EncodeToString(conv[:]) + ".0011223344556677.0000000000000000@echoo.test"
}

func TestResolve(t *testing.T) {
	forgedToken := outboundID(t, convX)
	ownToken := outboundID(t, convX)
	gmailRewritten := "CAM1x2y3z4=rewritten@mail.gmail.com"

	tests := []struct {
		name  string
		setup func(f *fakeLookup)
		msg   *mail.Parsed
		want  ID
		why   Reason
	}{
		{
			name:  "plain reply via In-Reply-To",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "root@cust.nl", convX) },
			msg:   msg("r1@cust.nl", "Re: Printer stuk", "klant@cust.nl", irt("root@cust.nl"), refs("root@cust.nl")),
			want:  convX, why: ReasonInReplyTo,
		},
		{
			name: "reply deep in chain resolves via In-Reply-To of the latest message",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{}).
					addRef(mailboxA, "root@cust.nl", convX).addRef(mailboxA, "r1@cust.nl", convX).addRef(mailboxA, "r2@cust.nl", convX)
			},
			msg:  msg("r3@cust.nl", "Re: Re: Re: Printer stuk", "klant@cust.nl", irt("r2@cust.nl"), refs("root@cust.nl", "r1@cust.nl", "r2@cust.nl")),
			want: convX, why: ReasonInReplyTo,
		},
		{
			name:  "Outlook reply with In-Reply-To but no References",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "r1@cust.nl", convX) },
			msg:   msg("<r2@Cust.NL>", "RE: Printer stuk", "klant@cust.nl", irt("<r1@Cust.NL>")),
			want:  convX, why: ReasonInReplyTo,
		},
		{
			name:  "In-Reply-To is normalized before matching",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "r1@cust.nl", convX) },
			msg:   msg("r2@cust.nl", "Printer stuk", "klant@cust.nl", irt(" <r1@CUST.nl> ")),
			want:  convX, why: ReasonInReplyTo,
		},
		{
			name:  "References only, no In-Reply-To",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "root@cust.nl", convX) },
			msg:   msg("r2@cust.nl", "Printer stuk", "klant@cust.nl", refs("root@cust.nl", "unknown@cust.nl")),
			want:  convX, why: ReasonReferences,
		},
		{
			name: "References are searched newest first",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{}).addConv(convY, fakeConv{}).
					addRef(mailboxA, "old@cust.nl", convX).addRef(mailboxA, "new@cust.nl", convY)
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl", refs("old@cust.nl", "new@cust.nl")),
			want: convY, why: ReasonReferences,
		},
		{
			name: "In-Reply-To wins over References",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{}).addConv(convY, fakeConv{}).
					addRef(mailboxA, "parent@cust.nl", convX).addRef(mailboxA, "other@cust.nl", convY)
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl", irt("parent@cust.nl"), refs("other@cust.nl")),
			want: convX, why: ReasonInReplyTo,
		},
		{
			name: "same Message-ID known only in another mailbox does not join",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{mailbox: mailboxB}).addRef(mailboxB, "root@cust.nl", convX)
			},
			msg:  msg("r@cust.nl", "Uniek onderwerp hier", "klant@cust.nl", irt("root@cust.nl")),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "client dropped both headers but Message-ID carries our token",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{})
			},
			msg:  msg("r@cust.nl", "Iets heel anders", "klant@cust.nl", irt(ownToken)),
			want: convX, why: ReasonOutboundToken,
		},
		{
			name:  "token found in References when In-Reply-To is missing",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}) },
			msg:   msg("r@cust.nl", "Iets heel anders", "klant@cust.nl", refs("junk@cust.nl", ownToken)),
			want:  convX, why: ReasonOutboundToken,
		},
		{
			name:  "forged token with a wrong tag for a conversation in the same mailbox",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}) },
			msg:   msg("r@evil.example", "Wire transfer details", "attacker@evil.example", irt(forgedID(convX))),
			want:  newConv, why: ReasonNewConversation,
		},
		{
			name:  "legacy untagged token is not trusted",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}) },
			msg: msg("r@evil.example", "Wire transfer details", "attacker@evil.example",
				irt("echoo."+hex.EncodeToString(convX[:])+".0011223344556677@echoo.test")),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name:  "customer forwarded the mail: reply from an unrelated sender still threads by our own id",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{people: []string{"klant@cust.nl", own[0]}}) },
			msg:   msg("c1@colleague.example", "Iets heel anders", "collega@colleague.example", irt(ownToken)),
			want:  convX, why: ReasonOutboundToken,
		},
		{
			name: "forged token pointing to a conversation in another mailbox",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{mailbox: mailboxB})
			},
			msg:  msg("r@evil.example", "Wire transfer details", "attacker@evil.example", irt(forgedToken)),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name:  "token for a conversation that does not exist",
			setup: func(f *fakeLookup) {},
			msg:   msg("r@evil.example", "Wire transfer details", "attacker@evil.example", irt(forgedToken)),
			want:  newConv, why: ReasonNewConversation,
		},
		{
			name: "Gmail send-as rewrote our Message-ID: subject and participant match",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", people: []string{"klant@gmail.com", own[0]}})
			},
			msg:  msg("gm1@mail.gmail.com", "Re: Printer stuk", "klant@gmail.com", irt(gmailRewritten), refs(gmailRewritten)),
			want: convX, why: ReasonSubject,
		},
		{
			name: "mailing list: subject tag and Reply-To munging",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "build failure on arm64", people: []string{"alice@one.example", "dev@lists.example"}})
			},
			msg: msg("l2@two.example", "Re: [dev] Build failure on ARM64", "bob@two.example",
				to("dev@lists.example"), func(p *mail.Parsed) {
					p.ListID = "dev.lists.example"
					p.Bulk = true
					p.ReplyTo = addrs("dev@lists.example")
				}),
			want: convX, why: ReasonSubject,
		},
		{
			name: "mailing list: Reply-To alone is not a participant",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "build failure on arm64", people: []string{"alice@one.example", "dev@lists.example"}})
			},
			msg: msg("l2@two.example", "Re: [dev] Build failure on ARM64", "bob@two.example",
				to("other@lists.example"), func(p *mail.Parsed) { p.ReplyTo = addrs("dev@lists.example") }),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name:  "list message joins by header even when the tag changes",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "l1@one.example", convX) },
			msg:   msg("l2@two.example", "Re: [dev-announce] Build failure", "bob@two.example", irt("l1@one.example")),
			want:  convX, why: ReasonInReplyTo,
		},
		{
			name: "inline forward starts a new conversation even with a matching candidate",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "offerte kantoorstoelen", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("f1@cust.nl", "Fwd: Offerte kantoorstoelen", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "forward of a forward starts a new conversation",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "offerte kantoorstoelen", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("f2@cust.nl", "FW: Fwd: Offerte kantoorstoelen", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "German forward WG does not join by subject",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "angebot heizung", people: []string{"kunde@firma.de"}})
			},
			msg:  msg("f3@firma.de", "WG: Angebot Heizung", "kunde@firma.de"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name:  "forward with headers still joins",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "orig@cust.nl", convX) },
			msg:   msg("f4@cust.nl", "Fwd: Offerte kantoorstoelen", "klant@cust.nl", irt("orig@cust.nl")),
			want:  convX, why: ReasonInReplyTo,
		},
		{
			name: "reply to a forward joins by subject",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "offerte kantoorstoelen", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Re: Fwd: Offerte kantoorstoelen", "klant@cust.nl"),
			want: convX, why: ReasonSubject,
		},
		{
			name: "subject changed mid-thread joins via headers",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk"}).addRef(mailboxA, "r1@cust.nl", convX)
			},
			msg:  msg("r2@cust.nl", "Nieuwe vraag: scanner ook stuk", "klant@cust.nl", irt("r1@cust.nl")),
			want: convX, why: ReasonInReplyTo,
		},
		{
			name: "generic subject Factuur from another customer does not merge",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "factuur", people: []string{"alice@one.example"}})
			},
			msg:  msg("b1@two.example", "Factuur", "bob@two.example"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "generic subject Factuur from the same customer does not merge either",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "factuur", people: []string{"alice@one.example"}})
			},
			msg:  msg("a2@one.example", "Re: Factuur", "alice@one.example"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "specific subject with a different customer does not merge",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "factuur 2026-113", people: []string{"alice@one.example"}})
			},
			msg:  msg("b1@two.example", "Factuur 2026-113", "bob@two.example"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "same subject same customer 40 days later starts a new conversation",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", lastAt: daysAgo(40), people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "same subject same customer 29 days later joins",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", lastAt: daysAgo(29), people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl"),
			want: convX, why: ReasonSubject,
		},
		{
			name: "candidate closed 10 days ago starts a new conversation",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", status: "closed", lastAt: daysAgo(12), resolvedAt: daysAgo(10), people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Re: Printer stuk", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "candidate closed 3 days ago is reopened by the reply",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", status: "closed", lastAt: daysAgo(4), resolvedAt: daysAgo(3), people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Re: Printer stuk", "klant@cust.nl"),
			want: convX, why: ReasonSubject,
		},
		{
			name: "closed without resolved_at falls back to last message time",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", status: "closed", lastAt: daysAgo(9), people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Re: Printer stuk", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "closed long ago but headers match still joins",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", status: "closed", lastAt: daysAgo(20), resolvedAt: daysAgo(15)}).
					addRef(mailboxA, "r1@cust.nl", convX)
			},
			msg:  msg("r2@cust.nl", "Re: Printer stuk", "klant@cust.nl", irt("r1@cust.nl")),
			want: convX, why: ReasonInReplyTo,
		},
		{
			name: "spam candidate is never joined by subject",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", status: "spam", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "out-of-office auto-reply threads into the original",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "offerte"})
			},
			msg:  msg("ooo@cust.nl", "Automatisch antwoord: Offerte", "klant@cust.nl", autoReply, irt(ownToken), refs(ownToken)),
			want: convX, why: ReasonOutboundToken,
		},
		{
			name:  "out-of-office auto-reply threads via In-Reply-To of an inbound message",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "q1@cust.nl", convX) },
			msg:   msg("ooo@cust.nl", "Out of office: Offerte", "klant@cust.nl", autoReply, irt("q1@cust.nl")),
			want:  convX, why: ReasonInReplyTo,
		},
		{
			name:  "duplicate Message-ID joins the existing conversation",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "dup@cust.nl", convX) },
			msg:   msg("dup@cust.nl", "Volstrekt ander onderwerp", "iemand@else.example"),
			want:  convX, why: ReasonOwnMessageID,
		},
		{
			name: "own Message-ID wins over a different In-Reply-To",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{}).addConv(convY, fakeConv{}).
					addRef(mailboxA, "me@cust.nl", convX).addRef(mailboxA, "other@cust.nl", convY)
			},
			msg:  msg("me@cust.nl", "Onderwerp", "klant@cust.nl", irt("other@cust.nl")),
			want: convX, why: ReasonOwnMessageID,
		},
		{
			name: "synthetic Message-ID of an identical re-fetch joins",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{}).addRef(mailboxA, "synthetic-abc123@echoo.invalid", convX)
			},
			msg:  msg("synthetic-abc123@echoo.invalid", "Onderwerp", "klant@cust.nl"),
			want: convX, why: ReasonOwnMessageID,
		},
		{
			name:  "synthetic Message-ID of a new message does not join",
			setup: func(f *fakeLookup) {},
			msg:   msg("synthetic-def456@echoo.invalid", "Onderwerp van deze mail", "klant@cust.nl"),
			want:  newConv, why: ReasonNewConversation,
		},
		{
			name:  "References containing garbage are skipped",
			setup: func(f *fakeLookup) { f.addConv(convX, fakeConv{}).addRef(mailboxA, "root@cust.nl", convX) },
			msg: msg("r@cust.nl", "Re: Printer stuk", "klant@cust.nl",
				refs("root@cust.nl", "", "not an id", "<a b@c.d>", "@", "x@", "<<>>", "\x00\xff@bad")),
			want: convX, why: ReasonReferences,
		},
		{
			name:  "only garbage in headers falls through to a new conversation",
			setup: func(f *fakeLookup) {},
			msg:   msg("r@cust.nl", "Iets unieks hier", "klant@cust.nl", irt("garbage"), refs("also garbage", "")),
			want:  newConv, why: ReasonNewConversation,
		},
		{
			name: "most recent matching candidate wins",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", lastAt: daysAgo(10), people: []string{"klant@cust.nl"}}).
					addConv(convY, fakeConv{normalized: "printer stuk", lastAt: daysAgo(2), people: []string{"klant@cust.nl"}}).
					addConv(convZ, fakeConv{normalized: "printer stuk", lastAt: daysAgo(5), people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl"),
			want: convY, why: ReasonSubject,
		},
		{
			name: "most recent candidate without overlap is skipped for an older one with overlap",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", lastAt: daysAgo(10), people: []string{"klant@cust.nl"}}).
					addConv(convY, fakeConv{normalized: "printer stuk", lastAt: daysAgo(2), people: []string{"ander@else.example"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl"),
			want: convX, why: ReasonSubject,
		},
		{
			name: "overlap on our own address only does not count",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", people: []string{"alice@one.example", own[0]}})
			},
			msg:  msg("r@two.example", "Printer stuk", "bob@two.example"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "overlap is case-insensitive",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "Klant@Cust.NL"),
			want: convX, why: ReasonSubject,
		},
		{
			name: "overlap via Cc of the new message",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "collega@cust.nl", cc("klant@cust.nl")),
			want: convX, why: ReasonSubject,
		},
		{
			name: "candidate in another mailbox is ignored",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{mailbox: mailboxB, normalized: "printer stuk", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "Printer stuk", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "empty subject never joins by subject",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@cust.nl", "", "klant@cust.nl"),
			want: newConv, why: ReasonNewConversation,
		},
		{
			name: "message from our own address without external participants is new",
			setup: func(f *fakeLookup) {
				f.addConv(convX, fakeConv{normalized: "printer stuk", people: []string{"klant@cust.nl"}})
			},
			msg:  msg("r@echoo.test", "Printer stuk", own[0], to(own[0])),
			want: newConv, why: ReasonNewConversation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake()
			tt.setup(f)
			got, err := Resolve(context.Background(), f, input(tt.msg))
			if err != nil {
				t.Fatal(err)
			}
			if got.ConversationID != tt.want || got.Reason != tt.why {
				t.Errorf("Resolve = conversation %v reason %q, want %v reason %q", got.ConversationID, got.Reason, tt.want, tt.why)
			}
			if got.IsNew() != (tt.want == newConv) {
				t.Errorf("IsNew = %v for conversation %v", got.IsNew(), got.ConversationID)
			}
			if want := NormalizeSubject(tt.msg.Subject); got.SubjectNormalized != want {
				t.Errorf("SubjectNormalized = %q, want %q", got.SubjectNormalized, want)
			}
		})
	}
}

func TestResolveRefHashes(t *testing.T) {
	h := func(ids ...string) [][]byte {
		out := make([][]byte, len(ids))
		for i, id := range ids {
			out[i] = mail.HashMessageID(id)
		}
		return out
	}
	tests := []struct {
		name string
		msg  *mail.Parsed
		want [][]byte
	}{
		{
			name: "own ID, In-Reply-To, then References newest first",
			msg:  msg("me@x.nl", "S", "a@x.nl", irt("p@x.nl"), refs("root@x.nl", "p@x.nl")),
			want: h("me@x.nl", "p@x.nl", "root@x.nl"),
		},
		{
			name: "duplicates removed",
			msg:  msg("me@x.nl", "S", "a@x.nl", irt("p@x.nl", "p@x.nl"), refs("p@x.nl", "me@x.nl")),
			want: h("me@x.nl", "p@x.nl"),
		},
		{
			name: "garbage dropped and IDs normalized",
			msg:  msg("<me@X.NL>", "S", "a@x.nl", irt("nope"), refs("<Root@X.NL>", "", "a b@c")),
			want: h("me@x.nl", "Root@x.nl"),
		},
		{
			name: "missing own ID",
			msg:  msg("", "S", "a@x.nl", irt("p@x.nl")),
			want: h("p@x.nl"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(context.Background(), newFake(), input(tt.msg))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.EqualFunc(got.RefHashes, tt.want, bytes.Equal) {
				t.Errorf("RefHashes = %x, want %x", got.RefHashes, tt.want)
			}
		})
	}
}

func TestResolveCapsReferences(t *testing.T) {
	ids := make([]string, 0, 200)
	for i := range 200 {
		ids = append(ids, string(rune('a'+i%26))+string(rune('a'+i/26))+"@x.nl")
	}
	got, err := Resolve(context.Background(), newFake(), input(msg("me@x.nl", "S", "a@x.nl", refs(ids...))))
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + maxHeaderRefs; len(got.RefHashes) != want {
		t.Fatalf("len(RefHashes) = %d, want %d", len(got.RefHashes), want)
	}
	if !bytes.Equal(got.RefHashes[1], mail.HashMessageID(ids[len(ids)-1])) {
		t.Error("newest reference is not first after the own ID")
	}
}

func TestResolveSequences(t *testing.T) {
	ctx := context.Background()
	step := func(t *testing.T, f *fakeLookup, m *mail.Parsed, want Reason) ID {
		t.Helper()
		in := input(m)
		d, err := Resolve(ctx, f, in)
		if err != nil {
			t.Fatal(err)
		}
		if d.Reason != want {
			t.Fatalf("message %s: reason %q, want %q", m.MessageID, d.Reason, want)
		}
		return f.apply(mailboxA, in, d)
	}

	t.Run("reply arriving before its parent", func(t *testing.T) {
		f := newFake()
		reply := msg("r1@cust.nl", "Re: Printer stuk", "klant@cust.nl", irt("q1@cust.nl"), refs("q1@cust.nl"))
		first := step(t, f, reply, ReasonNewConversation)
		parent := msg("q1@cust.nl", "Printer stuk", "klant@cust.nl")
		if got := step(t, f, parent, ReasonOwnMessageID); got != first {
			t.Errorf("parent joined %v, want %v", got, first)
		}
	})

	t.Run("reply, grandchild and parent arrive in reverse order", func(t *testing.T) {
		f := newFake()
		c3 := msg("c3@cust.nl", "Re: Re: Printer stuk", "klant@cust.nl", irt("c2@cust.nl"), refs("c1@cust.nl", "c2@cust.nl"))
		c2 := msg("c2@cust.nl", "Re: Printer stuk", "klant@cust.nl", irt("c1@cust.nl"), refs("c1@cust.nl"))
		c1 := msg("c1@cust.nl", "Printer stuk", "klant@cust.nl")
		id := step(t, f, c3, ReasonNewConversation)
		if got := step(t, f, c2, ReasonOwnMessageID); got != id {
			t.Fatalf("c2 joined %v, want %v", got, id)
		}
		if got := step(t, f, c1, ReasonOwnMessageID); got != id {
			t.Fatalf("c1 joined %v, want %v", got, id)
		}
	})

	t.Run("plain reply chain with a support answer in between", func(t *testing.T) {
		f := newFake()
		id := step(t, f, msg("q1@cust.nl", "Printer stuk", "klant@cust.nl"), ReasonNewConversation)
		f.addRef(mailboxA, "a1@echoo.test", id)
		step(t, f, msg("q2@cust.nl", "Re: Printer stuk", "klant@cust.nl", irt("a1@echoo.test"), refs("q1@cust.nl", "a1@echoo.test")), ReasonInReplyTo)
		other := step(t, f, msg("z1@else.example", "Iets anders", "ander@else.example"), ReasonNewConversation)
		if other == id {
			t.Error("unrelated message joined the conversation")
		}
	})

	t.Run("second message with same subject and customer joins by subject and is then reachable by header", func(t *testing.T) {
		f := newFake()
		id := step(t, f, msg("q1@cust.nl", "Printer stuk", "klant@cust.nl"), ReasonNewConversation)
		if got := step(t, f, msg("q2@cust.nl", "Re: Printer stuk", "klant@cust.nl"), ReasonSubject); got != id {
			t.Fatalf("joined %v, want %v", got, id)
		}
		if got := step(t, f, msg("q3@cust.nl", "Iets nieuws", "klant@cust.nl", irt("q2@cust.nl")), ReasonInReplyTo); got != id {
			t.Fatalf("joined %v, want %v", got, id)
		}
	})
}

func TestResolvePropagatesLookupErrors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("refs lookup", func(t *testing.T) {
		f := newFake()
		f.err = boom
		if _, err := Resolve(context.Background(), f, input(msg("r@cust.nl", "Printer stuk", "klant@cust.nl"))); !errors.Is(err, boom) {
			t.Errorf("err = %v, want wrapping %v", err, boom)
		}
	})

	t.Run("token ownership lookup", func(t *testing.T) {
		f := &erroringOwnership{fakeLookup: newFake(), err: boom}
		m := msg("r@cust.nl", "Iets", "klant@cust.nl", irt(outboundID(t, convX)))
		if _, err := Resolve(context.Background(), f, input(m)); !errors.Is(err, boom) {
			t.Errorf("err = %v, want wrapping %v", err, boom)
		}
	})

	t.Run("subject lookup", func(t *testing.T) {
		f := &erroringSubject{fakeLookup: newFake(), err: boom}
		if _, err := Resolve(context.Background(), f, input(msg("r@cust.nl", "Printer stuk", "klant@cust.nl"))); !errors.Is(err, boom) {
			t.Errorf("err = %v, want wrapping %v", err, boom)
		}
	})
}

type erroringOwnership struct {
	*fakeLookup
	err error
}

func (e *erroringOwnership) ConversationInMailbox(context.Context, ID, ID) (bool, error) {
	return false, e.err
}

type erroringSubject struct {
	*fakeLookup
	err error
}

func (e *erroringSubject) SubjectCandidates(context.Context, ID, string, time.Time) ([]Candidate, error) {
	return nil, e.err
}
