package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
	"echoo/internal/policy"
	"echoo/internal/realtime"
	"echoo/internal/threading"
)

const (
	maxSubjectRunes   = 300
	maxAttachments    = 20
	maxMessageBytes   = 50 << 20
	maxBodyHTMLBytes  = 500 << 10
	settingEmailKey   = "email"
	statusAfterClosed = "closed"
)

var errIdempotencyConflict = &apiError{Status: http.StatusConflict, Code: "idempotency_conflict", Message: "this idempotency key belongs to another message"}

// composerRoutes registers everything the composer needs. All routes require a ready account;
// conversation access is decided per request from the user's mailbox scope.
func (s *Server) composerRoutes(r chi.Router) {
	r.Post("/conversations", s.createConversation)
	r.Get("/conversations/{id}/reply-defaults", s.replyDefaults)
	r.Get("/conversations/{id}/mentionable", s.mentionable)
	r.Post("/conversations/{id}/replies", s.sendReply)
	r.Post("/conversations/{id}/replies/{messageId}/cancel", s.cancelReply)
	r.Post("/conversations/{id}/forward", s.forwardMessage)
	r.Post("/conversations/{id}/notes", s.addNote)
	r.Get("/conversations/{id}/draft", s.getDraft)
	r.Put("/conversations/{id}/draft", s.putDraft)
	r.Delete("/conversations/{id}/draft", s.deleteDraft)

	r.Post("/uploads", s.createUpload)
	r.Delete("/uploads/{id}", s.deleteUpload)

	r.Get("/notifications", s.listNotifications)
	r.Post("/notifications/read", s.markNotificationsRead)

	r.Get("/templates", s.listTemplates)
	r.Post("/templates", s.createTemplate)
	r.Patch("/templates/{id}", s.updateTemplate)
	r.Delete("/templates/{id}", s.deleteTemplate)
	r.Get("/templates/{id}/render", s.renderTemplate)

	r.Get("/me/signatures", s.listMySignatures)
	r.Put("/me/signatures", s.putMySignature)
	r.Get("/signatures/effective", s.effectiveSignature)
}

// writableConversation loads a conversation the user may reply to. A conversation outside the
// user's read scope does not exist as far as they can tell; one they can read but not write is
// forbidden, which is what readonly users get.
func (s *Server) writableConversation(ctx context.Context, user dbq.User, idParam string) (dbq.ComposeGetConversationRow, error) {
	id, ok := parseUUID(idParam)
	if !ok {
		return dbq.ComposeGetConversationRow{}, errNotFound
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		return dbq.ComposeGetConversationRow{}, err
	}
	return s.conversationInScope(ctx, scope, id, true)
}

func (s *Server) readableConversation(ctx context.Context, user dbq.User, idParam string) (dbq.ComposeGetConversationRow, error) {
	id, ok := parseUUID(idParam)
	if !ok {
		return dbq.ComposeGetConversationRow{}, errNotFound
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		return dbq.ComposeGetConversationRow{}, err
	}
	return s.conversationInScope(ctx, scope, id, false)
}

func (s *Server) conversationInScope(ctx context.Context, scope policy.Scope, id pgtype.UUID, write bool) (dbq.ComposeGetConversationRow, error) {
	conv, err := s.q.ComposeGetConversation(ctx, dbq.ComposeGetConversationParams{ID: id, MailboxIds: scope.Read})
	if errors.Is(err, pgx.ErrNoRows) {
		return conv, errNotFound
	}
	if err != nil {
		return conv, fmt.Errorf("load conversation: %w", err)
	}
	if write && !slices.Contains(scope.Write, conv.MailboxID) {
		return conv, errForbidden
	}
	return conv, nil
}

type addressListJSON = []mail.Address

type replyDefaultsJSON struct {
	To addressListJSON `json:"to"`
	Cc addressListJSON `json:"cc"`
	// SuggestedCc lists senders who wrote into the thread without being a known participant.
	// They are never in To or Cc by default; the agent adds them on purpose.
	SuggestedCc      addressListJSON `json:"suggested_cc"`
	Subject          string          `json:"subject"`
	From             mail.Address    `json:"from"`
	SendDelaySeconds int32           `json:"send_delay_seconds"`
}

func (s *Server) replyDefaults(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conv, err := s.writableConversation(ctx, sessionFrom(ctx).User, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	to, cc, suggested, err := s.defaultRecipients(ctx, s.q, conv.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mb, err := s.q.GetMailbox(ctx, conv.MailboxID)
	if err != nil {
		writeError(w, r, fmt.Errorf("load mailbox: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, replyDefaultsJSON{
		To: to, Cc: cc, SuggestedCc: suggested, Subject: compose.ReplySubject(conv.Subject),
		From:             mail.Address{Name: mb.DisplayName, Address: mb.EmailAddress},
		SendDelaySeconds: mb.SendDelaySeconds,
	})
}

func (s *Server) defaultRecipients(ctx context.Context, q *dbq.Queries, conversationID pgtype.UUID) (to, cc, suggested []mail.Address, err error) {
	own, err := q.ComposeMailboxAddresses(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list mailbox addresses: %w", err)
	}
	ownSet := make(map[string]bool, len(own))
	for _, a := range own {
		ownSet[strings.ToLower(a)] = true
	}
	last, err := q.ComposeLastEmail(ctx, dbq.ComposeLastEmailParams{ConversationID: conversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return []mail.Address{}, []mail.Address{}, []mail.Address{}, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load last message: %w", err)
	}
	lastMsg, err := toComposeMessage(last.FromAddr, last.FromName, last.ToAddrs, last.CcAddrs, last.ReplyTo)
	if err != nil {
		return nil, nil, nil, err
	}
	known, err := q.ComposeKnownAddresses(ctx, conversationID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load participants: %w", err)
	}
	knownSet := make(map[string]bool, len(known))
	for _, a := range known {
		knownSet[a] = true
	}
	inbound, err := s.lastInbound(ctx, q, conversationID, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	var knownInbound *compose.Message
	if inbound != nil && inbound.HasUnknownSender(knownSet) {
		if knownInbound, err = s.lastInbound(ctx, q, conversationID, known); err != nil {
			return nil, nil, nil, err
		}
	}
	to, cc, suggested = compose.ReplyRecipients(lastMsg, inbound, knownInbound, knownSet, ownSet)
	return nonNilAddrs(to), nonNilAddrs(cc), nonNilAddrs(suggested), nil
}

// lastInbound loads the newest inbound message, restricted to customers in knownCustomers when
// that is not nil. It returns nil when there is none.
func (s *Server) lastInbound(ctx context.Context, q *dbq.Queries, conversationID pgtype.UUID, knownCustomers []string) (*compose.Message, error) {
	in, err := q.ComposeLastEmail(ctx, dbq.ComposeLastEmailParams{
		ConversationID: conversationID, Direction: pgtype.Text{String: "in", Valid: true}, KnownCustomers: knownCustomers,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load last inbound message: %w", err)
	}
	m, err := toComposeMessage(in.FromAddr, in.FromName, in.ToAddrs, in.CcAddrs, in.ReplyTo)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func toComposeMessage(fromAddr, fromName string, to, cc, replyTo []byte) (compose.Message, error) {
	m := compose.Message{From: mail.Address{Name: fromName, Address: fromAddr}}
	for _, f := range []struct {
		dst *[]mail.Address
		raw []byte
	}{{&m.To, to}, {&m.Cc, cc}, {&m.ReplyTo, replyTo}} {
		list, err := decodeAddresses(f.raw)
		if err != nil {
			return m, fmt.Errorf("decode addresses: %w", err)
		}
		*f.dst = list
	}
	return m, nil
}

func nonNilAddrs(l []mail.Address) []mail.Address {
	if l == nil {
		return []mail.Address{}
	}
	return l
}

type mentionableJSON struct {
	Users []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"users"`
}

func (s *Server) mentionable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conv, err := s.readableConversation(ctx, sessionFrom(ctx).User, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.ComposeListMentionable(ctx, conv.MailboxID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list mentionable users: %w", err))
		return
	}
	out := mentionableJSON{Users: make([]struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}, len(rows))}
	for i, u := range rows {
		out.Users[i].ID, out.Users[i].Name, out.Users[i].Email = uuidStr(u.ID), u.Name, u.Email
	}
	writeJSON(w, http.StatusOK, out)
}

type sendRequest struct {
	IdempotencyKey string         `json:"idempotency_key"`
	To             []mail.Address `json:"to"`
	Cc             []mail.Address `json:"cc"`
	Bcc            []mail.Address `json:"bcc"`
	Subject        *string        `json:"subject"`
	HTML           string         `json:"html"`
	Text           string         `json:"text"`
	AttachmentIDs  []string       `json:"attachment_ids"`
	StatusAfter    *string        `json:"status_after"`
	MessageID      string         `json:"message_id"`
	MailboxID      string         `json:"mailbox_id"`
}

// prepared is a validated send request, ready to be written in a transaction.
type prepared struct {
	key         pgtype.UUID
	to, cc, bcc []mail.Address
	subject     string
	html, text  string
	uploads     []dbq.Upload
	statusAfter string
}

func (s *Server) prepareSend(ctx context.Context, user dbq.User, req sendRequest, defaultSubject string, allowEmpty bool) (prepared, error) {
	fields := map[string]string{}
	p := prepared{}
	key, ok := parseUUID(req.IdempotencyKey)
	if !ok {
		fields["idempotency_key"] = "invalid"
	}
	p.key = key

	var err error
	for _, f := range []struct {
		name string
		src  []mail.Address
		dst  *[]mail.Address
	}{{"to", req.To, &p.to}, {"cc", req.Cc, &p.cc}, {"bcc", req.Bcc, &p.bcc}} {
		if *f.dst, err = compose.NormalizeAddresses(f.src); err != nil {
			fields[f.name] = "invalid"
		}
	}
	if len(p.to)+len(p.cc)+len(p.bcc) > compose.MaxRecipients {
		fields["to"] = "too_many"
	}

	p.subject = defaultSubject
	if req.Subject != nil {
		p.subject = strings.TrimSpace(*req.Subject)
	}
	if strings.ContainsAny(p.subject, "\r\n\x00") || utf8.RuneCountInString(p.subject) > maxSubjectRunes {
		fields["subject"] = "invalid"
	}

	if len(req.HTML) > maxBodyHTMLBytes {
		fields["html"] = "too_large"
	}
	if req.StatusAfter != nil {
		switch *req.StatusAfter {
		case "waiting", statusAfterClosed:
			p.statusAfter = *req.StatusAfter
		default:
			fields["status_after"] = "invalid"
		}
	}
	if p.uploads, err = s.resolveUploads(ctx, s.q, user, req.AttachmentIDs); err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			return p, err
		}
		fields["attachment_ids"] = ae.Message
	}
	if len(fields) > 0 {
		return p, errValidation(fields)
	}

	cids := make([]string, len(p.uploads))
	for i, u := range p.uploads {
		cids[i] = u.ContentID
	}
	p.html = compose.Sanitize(req.HTML, compose.SanitizeOptions{ContentIDs: cids})
	if !allowEmpty && compose.IsEmpty(p.html) {
		return p, errValidation(map[string]string{"html": "empty"})
	}
	p.text = req.Text
	if len(p.text) > maxBodyHTMLBytes {
		return p, errValidation(map[string]string{"text": "too_large"})
	}
	return p, nil
}

// resolveUploads returns the caller's own, unexpired uploads. Anything else, including another
// user's upload, is reported the same way as a missing one.
func (s *Server) resolveUploads(ctx context.Context, q *dbq.Queries, user dbq.User, raw []string) ([]dbq.Upload, error) {
	if len(raw) == 0 {
		return []dbq.Upload{}, nil
	}
	if len(raw) > maxAttachments {
		return nil, &apiError{Message: "too_many"}
	}
	ids := make([]pgtype.UUID, 0, len(raw))
	for _, v := range raw {
		id, ok := parseUUID(v)
		if !ok {
			return nil, &apiError{Message: "unknown"}
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	rows, err := q.ComposeListUploads(ctx, dbq.ComposeListUploadsParams{Ids: ids, UserID: user.ID})
	if err != nil {
		return nil, fmt.Errorf("list uploads: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, &apiError{Message: "unknown"}
	}
	var total int64
	for _, u := range rows {
		total += u.SizeBytes
	}
	if total > maxMessageBytes {
		return nil, &apiError{Message: "too_large"}
	}
	return rows, nil
}

// delivery is everything needed to queue one outgoing message.
type delivery struct {
	user       dbq.User
	conv       pgtype.UUID
	mailbox    dbq.Mailbox
	p          prepared
	inReplyTo  string
	references []string
	extra      []send.AttachmentRef
}

// errDuplicate aborts the transaction when an idempotency key was used before.
type errDuplicate struct{ messageID pgtype.UUID }

func (e errDuplicate) Error() string { return "duplicate idempotency key" }

// queue wraps the message in the branded layout and hands it to the send queue. Callers own
// everything else about the conversation.
func (s *Server) queue(ctx context.Context, tx pgx.Tx, d delivery) (pgtype.UUID, error) {
	if s.jobs == nil {
		return pgtype.UUID{}, errors.New("job queue is not configured")
	}
	q := dbq.New(tx)
	var signature string
	sig, err := q.ComposeResolveSignature(ctx, dbq.ComposeResolveSignatureParams{UserID: d.user.ID, MailboxID: d.mailbox.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, fmt.Errorf("resolve signature: %w", err)
	}
	if err == nil {
		signature = sig.BodyHtml
	}
	settings, err := loadEmailSettings(ctx, q)
	if err != nil {
		return pgtype.UUID{}, err
	}
	brand := d.mailbox.DisplayName
	if brand == "" {
		brand = d.mailbox.Name
	}
	htmlPart, textPart := compose.Layout{
		BrandName: brand, BodyHTML: d.p.html, BodyText: d.p.text, SignatureHTML: signature, FooterText: settings.FooterText,
	}.Render()

	referenced := map[string]bool{}
	for _, c := range compose.CIDs(d.p.html) {
		referenced[c] = true
	}
	refs := slices.Clone(d.extra)
	for _, u := range d.p.uploads {
		refs = append(refs, send.AttachmentRef{
			Filename: u.Filename, ContentType: u.ContentType, ContentID: u.ContentID,
			Inline: referenced[u.ContentID] && strings.HasPrefix(u.ContentType, "image/"),
			Size:   u.SizeBytes, SHA256: u.Sha256, BlobKey: u.BlobKey,
		})
	}
	res, err := send.Enqueue(ctx, tx, s.jobs, send.EnqueueParams{
		ConversationID: d.conv, MailboxID: d.mailbox.ID, IdempotencyKey: d.p.key, AuthorUserID: d.user.ID,
		To: d.p.to, Cc: d.p.cc, Bcc: d.p.bcc, Subject: d.p.subject, Text: textPart, HTML: htmlPart,
		InReplyTo: d.inReplyTo, References: d.references, Attachments: refs,
	})
	if err != nil {
		var ve *send.ValidationError
		if errors.As(err, &ve) {
			return pgtype.UUID{}, errValidation(map[string]string{strings.ToLower(ve.Field): "invalid"})
		}
		return pgtype.UUID{}, err
	}
	if res.Duplicate {
		return res.MessageID, errDuplicate{messageID: res.MessageID}
	}
	if len(d.p.uploads) > 0 {
		ids := make([]pgtype.UUID, len(d.p.uploads))
		for i, u := range d.p.uploads {
			ids[i] = u.ID
		}
		if err := q.ComposeDeleteUploads(ctx, dbq.ComposeDeleteUploadsParams{Ids: ids, UserID: d.user.ID}); err != nil {
			return pgtype.UUID{}, fmt.Errorf("release uploads: %w", err)
		}
	}
	return res.MessageID, nil
}

// afterQueued updates the conversation for a message that was just queued: version bump, the
// requested status and the realtime events. The status change goes through the inbox service, so
// its timeline events and reports are the same as for a change made from the header.
func (s *Server) afterQueued(ctx context.Context, tx pgx.Tx, actor inbox.Actor, conv pgtype.UUID, mailbox pgtype.UUID, messageID pgtype.UUID, p prepared) error {
	q := dbq.New(tx)
	locked, err := q.ComposeLockConversation(ctx, conv)
	if err != nil {
		return fmt.Errorf("lock conversation: %w", err)
	}
	version, err := q.ComposeTouchConversation(ctx, dbq.ComposeTouchConversationParams{ID: conv, HasAttachments: len(p.uploads) > 0})
	if err != nil {
		return fmt.Errorf("update conversation: %w", err)
	}
	if p.statusAfter != "" && p.statusAfter != locked.Status {
		if version, err = s.inbox.ApplyTx(ctx, tx, actor, conv, nil, inbox.Change{Status: &p.statusAfter}); err != nil {
			return fmt.Errorf("set status: %w", err)
		}
		if err := q.ComposeRecordStatusChange(ctx, dbq.ComposeRecordStatusChangeParams{MessageID: messageID, FromStatus: locked.Status, ToStatus: p.statusAfter}); err != nil {
			return fmt.Errorf("record status change: %w", err)
		}
	}
	// Answering a conversation implies having read it.
	if err := q.MarkConversationRead(ctx, dbq.MarkConversationReadParams{UserID: actor.UserID, ConversationID: conv}); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return notifyMessage(ctx, q, conv, mailbox, int64(version))
}

func notifyMessage(ctx context.Context, q *dbq.Queries, conv, mailbox pgtype.UUID, version int64) error {
	for _, typ := range []string{realtime.TypeMessageCreated, realtime.TypeConversationUpdated} {
		if err := realtime.Notify(ctx, q, realtime.Event{Type: typ, ConversationID: conv.String(), MailboxID: mailbox.String(), Version: version}); err != nil {
			return err
		}
	}
	return nil
}

type sentMessageJSON struct {
	Message   messageJSON `json:"message"`
	UndoUntil *time.Time  `json:"undo_until"`
}

func (s *Server) sentMessage(ctx context.Context, id pgtype.UUID) (sentMessageJSON, error) {
	row, err := s.q.ComposeGetMessageView(ctx, id)
	if err != nil {
		return sentMessageJSON{}, fmt.Errorf("load message: %w", err)
	}
	atts, err := s.q.ListMessageAttachments(ctx, []pgtype.UUID{id})
	if err != nil {
		return sentMessageJSON{}, fmt.Errorf("load attachments: %w", err)
	}
	to, err := decodeAddresses(row.ToAddrs)
	if err != nil {
		return sentMessageJSON{}, fmt.Errorf("decode to: %w", err)
	}
	cc, err := decodeAddresses(row.CcAddrs)
	if err != nil {
		return sentMessageJSON{}, fmt.Errorf("decode cc: %w", err)
	}
	msg := messageJSON{
		ID: uuidStr(row.ID), Kind: row.Kind, From: mail.Address{Name: row.FromName, Address: row.FromAddr},
		To: to, Cc: cc, Subject: row.Subject, BodyText: row.BodyText,
		SentAt: timeOrNil(row.SentAt), ReceivedAt: row.ReceivedAt.Time.UTC(), Attachments: []attachmentJSON{},
	}
	for _, a := range atts {
		msg.Attachments = append(msg.Attachments, attachmentJSON{ID: uuidStr(a.ID), Filename: a.Filename, Size: a.SizeBytes, SniffedType: a.SniffedType, Inline: a.Disposition == "inline"})
	}
	if row.Direction.Valid {
		msg.Direction = &row.Direction.String
	}
	if row.AuthorID.Valid {
		msg.Author = &refJSON{ID: uuidStr(row.AuthorID), Name: row.AuthorName.String}
	}
	out := sentMessageJSON{Message: msg}
	if row.OutboundStatus.Valid {
		msg.OutboundStatus = &row.OutboundStatus.String
		if row.OutboundError.String != "" {
			msg.OutboundError = &row.OutboundError.String
		}
		out.Message = msg
		out.UndoUntil = timeOrNil(row.UndoUntil)
	}
	return out, nil
}

// respondDuplicate answers a repeated request with the message the first one created, as long
// as it is the caller's own message.
func (s *Server) respondDuplicate(w http.ResponseWriter, r *http.Request, user dbq.User, dup errDuplicate, conv pgtype.UUID) {
	ctx := r.Context()
	own, err := s.q.ComposeGetOutgoingByID(ctx, dup.messageID)
	if err != nil {
		writeError(w, r, fmt.Errorf("load earlier message: %w", err))
		return
	}
	if own.CreatedBy != user.ID || (conv.Valid && own.ConversationID != conv) {
		writeError(w, r, errIdempotencyConflict)
		return
	}
	out, err := s.sentMessage(ctx, dup.messageID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sendReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor, err := s.inboxActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req sendRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.To) == 0 {
		defTo, defCc, _, err := s.defaultRecipients(ctx, s.q, conv.ID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		req.To = defTo
		if len(req.Cc) == 0 {
			req.Cc = defCc
		}
	}
	p, err := s.prepareSend(ctx, user, req, compose.ReplySubject(conv.Subject), false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(p.to)+len(p.cc)+len(p.bcc) == 0 {
		writeError(w, r, errValidation(map[string]string{"to": "required"}))
		return
	}
	mb, err := s.sendMailbox(ctx, conv.MailboxID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	last, err := s.q.ComposeLastEmail(ctx, dbq.ComposeLastEmailParams{ConversationID: conv.ID})
	var inReplyTo string
	var refs []string
	switch {
	case err == nil:
		inReplyTo = mail.NormalizeMessageID(last.MessageIDHeader)
		for _, ref := range last.ReferencesHdr {
			refs = append(refs, mail.NormalizeMessageID(ref))
		}
		if inReplyTo != "" {
			refs = append(refs, inReplyTo)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, fmt.Errorf("load last message: %w", err))
		return
	}

	var messageID pgtype.UUID
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := s.queue(ctx, tx, delivery{user: user, conv: conv.ID, mailbox: mb, p: p, inReplyTo: inReplyTo, references: refs})
		if err != nil {
			return err
		}
		messageID = id
		q := dbq.New(tx)
		if err := q.ComposeDeleteDraft(ctx, dbq.ComposeDeleteDraftParams{ConversationID: conv.ID, UserID: user.ID}); err != nil {
			return fmt.Errorf("delete draft: %w", err)
		}
		return s.afterQueued(ctx, tx, actor, conv.ID, conv.MailboxID, id, p)
	})
	var dup errDuplicate
	if errors.As(err, &dup) {
		s.respondDuplicate(w, r, user, dup, conv.ID)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.sentMessage(ctx, messageID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) sendMailbox(ctx context.Context, id pgtype.UUID) (dbq.Mailbox, error) {
	mb, err := s.q.GetMailbox(ctx, id)
	if err != nil {
		return mb, fmt.Errorf("load mailbox: %w", err)
	}
	if mb.DisabledAt.Valid {
		return mb, &apiError{Status: http.StatusUnprocessableEntity, Code: "mailbox_disabled", Message: "this mailbox is disabled"}
	}
	return mb, nil
}

func (s *Server) cancelReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor, err := s.inboxActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	msgID, ok := parseUUID(chi.URLParam(r, "messageId"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	out, err := s.q.ComposeGetOutgoing(ctx, dbq.ComposeGetOutgoingParams{ID: msgID, ConversationID: conv.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load message: %w", err))
		return
	}
	// Undo is for the author; other agents see the reply in the thread and can follow up.
	if out.CreatedBy != user.ID {
		writeError(w, r, errForbidden)
		return
	}
	var restored []dbq.Upload
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := send.Cancel(ctx, tx, msgID); err != nil {
			return err
		}
		q := dbq.New(tx)
		if err := q.ComposeSoftDeleteMessage(ctx, msgID); err != nil {
			return fmt.Errorf("hide cancelled message: %w", err)
		}
		var err error
		if restored, err = q.ComposeRestoreUploads(ctx, dbq.ComposeRestoreUploadsParams{UserID: user.ID, MessageID: msgID}); err != nil {
			return fmt.Errorf("restore attachments: %w", err)
		}
		return s.revertReplyStatus(ctx, tx, actor, conv, msgID)
	})
	if errors.Is(err, send.ErrNotCancellable) {
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "too_late", Message: "the message is already being sent"})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	attachments := make([]uploadJSON, len(restored))
	for i, u := range restored {
		attachments[i] = toUploadJSON(u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "attachments": attachments})
}

// revertReplyStatus undoes the status change a reply caused, unless someone changed the status
// again since.
func (s *Server) revertReplyStatus(ctx context.Context, tx pgx.Tx, actor inbox.Actor, conv dbq.ComposeGetConversationRow, messageID pgtype.UUID) error {
	q := dbq.New(tx)
	locked, err := q.ComposeLockConversation(ctx, conv.ID)
	if err != nil {
		return fmt.Errorf("lock conversation: %w", err)
	}
	version, err := q.ComposeTouchConversation(ctx, dbq.ComposeTouchConversationParams{ID: conv.ID})
	if err != nil {
		return fmt.Errorf("update conversation: %w", err)
	}
	change, err := q.ComposeTakeStatusChange(ctx, messageID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("find status change: %w", err)
	case locked.Status == change.ToStatus:
		if version, err = s.inbox.ApplyTx(ctx, tx, actor, conv.ID, nil, inbox.Change{Status: &change.FromStatus}); err != nil {
			return fmt.Errorf("revert status: %w", err)
		}
	}
	return realtime.Notify(ctx, q, realtime.Event{Type: realtime.TypeConversationUpdated, ConversationID: conv.ID.String(), MailboxID: conv.MailboxID.String(), Version: int64(version)})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	var req sendRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	mailboxID, ok := parseUUID(req.MailboxID)
	if !ok {
		writeError(w, r, errValidation(map[string]string{"mailbox_id": "invalid"}))
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !slices.Contains(scope.Read, mailboxID) {
		writeError(w, r, errNotFound)
		return
	}
	if !slices.Contains(scope.Write, mailboxID) {
		writeError(w, r, errForbidden)
		return
	}
	actor := inbox.Actor{UserID: user.ID, Read: scope.Read, Write: scope.Write}
	if req.Subject == nil || strings.TrimSpace(*req.Subject) == "" {
		writeError(w, r, errValidation(map[string]string{"subject": "required"}))
		return
	}
	if len(req.To) == 0 {
		writeError(w, r, errValidation(map[string]string{"to": "required"}))
		return
	}
	p, err := s.prepareSend(ctx, user, req, "", false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mb, err := s.sendMailbox(ctx, mailboxID)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var convID, messageID pgtype.UUID
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		contactID, err := ensureContact(ctx, q, p.to[0])
		if err != nil {
			return err
		}
		created, err := q.ComposeCreateConversation(ctx, dbq.ComposeCreateConversationParams{
			MailboxID: mailboxID, Subject: p.subject, SubjectNormalized: threading.NormalizeSubject(p.subject), ContactID: contactID,
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		convID = created.ID
		data, err := json.Marshal(map[string]string{"reason": "outbound"})
		if err != nil {
			return fmt.Errorf("encode created event: %w", err)
		}
		if err := q.ComposeInsertEvent(ctx, dbq.ComposeInsertEventParams{ConversationID: convID, MailboxID: mailboxID, ActorUserID: user.ID, Type: "created", Data: data}); err != nil {
			return fmt.Errorf("insert created event: %w", err)
		}
		id, err := s.queue(ctx, tx, delivery{user: user, conv: convID, mailbox: mb, p: p})
		if err != nil {
			return err
		}
		messageID = id
		return s.afterQueued(ctx, tx, actor, convID, mailboxID, id, p)
	})
	var dup errDuplicate
	if errors.As(err, &dup) {
		s.respondDuplicate(w, r, user, dup, pgtype.UUID{})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.sentMessage(ctx, messageID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		sentMessageJSON
		ConversationID string `json:"conversation_id"`
	}{out, convID.String()})
}

// ensureContact finds or creates the contact for an address we write to first. Ingest fills
// in the organization when the person writes back.
func ensureContact(ctx context.Context, q *dbq.Queries, to mail.Address) (pgtype.UUID, error) {
	if err := q.IngestAdvisoryLock(ctx, "contact:"+to.Address); err != nil {
		return pgtype.UUID{}, fmt.Errorf("lock contact: %w", err)
	}
	id, err := q.IngestGetContactByEmail(ctx, to.Address)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, fmt.Errorf("find contact: %w", err)
	}
	if id, err = q.IngestCreateContact(ctx, dbq.IngestCreateContactParams{Name: to.Name}); err != nil {
		return pgtype.UUID{}, fmt.Errorf("create contact: %w", err)
	}
	if err := q.IngestCreateContactAddress(ctx, dbq.IngestCreateContactAddressParams{ContactID: id, Email: to.Address}); err != nil {
		return pgtype.UUID{}, fmt.Errorf("create contact address: %w", err)
	}
	return id, nil
}

func (s *Server) forwardMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor, err := s.inboxActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req sendRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	origID, ok := parseUUID(req.MessageID)
	if !ok {
		writeError(w, r, errValidation(map[string]string{"message_id": "invalid"}))
		return
	}
	orig, err := s.q.ComposeGetEmail(ctx, dbq.ComposeGetEmailParams{ID: origID, ConversationID: conv.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load message: %w", err))
		return
	}
	if len(req.To) == 0 {
		writeError(w, r, errValidation(map[string]string{"to": "required"}))
		return
	}
	p, err := s.prepareSend(ctx, user, req, compose.ForwardSubject(orig.Subject), true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mb, err := s.sendMailbox(ctx, conv.MailboxID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	p.html += forwardQuote(orig)
	p.text = ""

	var extra []send.AttachmentRef
	if orig.RawMessageID.Valid {
		raw, err := s.q.ComposeGetRawBlob(ctx, orig.RawMessageID)
		if err != nil {
			writeError(w, r, fmt.Errorf("load original message blob: %w", err))
			return
		}
		extra = append(extra, send.AttachmentRef{
			Filename: "doorgestuurd-bericht.eml", ContentType: "message/rfc822",
			Size: int64(raw.SizeBytes), SHA256: raw.Sha256, BlobKey: raw.BlobKey,
		})
	}

	var messageID pgtype.UUID
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := s.queue(ctx, tx, delivery{user: user, conv: conv.ID, mailbox: mb, p: p, extra: extra})
		if err != nil {
			return err
		}
		messageID = id
		return s.afterQueued(ctx, tx, actor, conv.ID, conv.MailboxID, id, p)
	})
	var dup errDuplicate
	if errors.As(err, &dup) {
		s.respondDuplicate(w, r, user, dup, conv.ID)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.sentMessage(ctx, messageID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// forwardQuote renders the forwarded message as an escaped, quoted block. Only the plain text
// is quoted: the original HTML is attacker-controlled and stays in the attached .eml.
func forwardQuote(m dbq.ComposeGetEmailRow) string {
	var b strings.Builder
	b.WriteString("<blockquote><p><strong>Doorgestuurd bericht</strong><br>")
	from := m.FromAddr
	if m.FromName != "" {
		from = m.FromName + " <" + m.FromAddr + ">"
	}
	b.WriteString("Van: " + html.EscapeString(from) + "<br>")
	b.WriteString("Datum: " + m.ReceivedAt.Time.UTC().Format("2006-01-02 15:04") + " UTC<br>")
	b.WriteString("Onderwerp: " + html.EscapeString(m.Subject) + "</p>")
	for _, para := range strings.Split(strings.ReplaceAll(strings.TrimSpace(m.BodyText), "\r\n", "\n"), "\n\n") {
		b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(strings.TrimSpace(para)), "\n", "<br>") + "</p>")
	}
	b.WriteString("</blockquote>")
	return b.String()
}
