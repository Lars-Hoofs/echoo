package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/realtime"
)

const (
	claimBatch = 100
	// sweep is how often the queue is checked without a wake-up, for notifications whose
	// NOTIFY was missed while the listener reconnected.
	sweep = 30 * time.Second
	// parallel bounds the requests to push services in flight.
	parallel = 8
	// maxSubject is how much of a subject a Web Push shows.
	maxSubject = 120
)

// Sender delivers one message to one device of its kind.
type Sender interface {
	Send(ctx context.Context, d Device, web, native Message) error
}

// Device is a registration as the dispatcher sees it.
type Device struct {
	ID       pgtype.UUID
	Kind     string
	Endpoint string
	P256dh   []byte
	Auth     []byte
}

// Senders routes a device to the service of its kind; a nil service is not configured.
type Senders struct {
	Web  *WebPush
	APNs *APNs
	FCM  *FCM
}

func (s Senders) Send(ctx context.Context, d Device, web, native Message) error {
	switch d.Kind {
	case KindWebPush:
		if s.Web == nil {
			return errNotConfigured
		}
		payload, err := json.Marshal(web)
		if err != nil {
			return err
		}
		return s.Web.Send(ctx, d.Endpoint, d.P256dh, d.Auth, payload)
	case KindAPNs:
		if s.APNs == nil {
			return errNotConfigured
		}
		return s.APNs.Send(ctx, d.Endpoint, native)
	case KindFCM:
		if s.FCM == nil {
			return errNotConfigured
		}
		return s.FCM.Send(ctx, d.Endpoint, native)
	}
	return fmt.Errorf("unknown device kind %q", d.Kind)
}

var errNotConfigured = errors.New("push service not configured on this server")

// Dispatcher turns notification rows into pushes. It claims rows the way the notification
// mail does: marked handled in the claiming transaction, so a row is pushed at most once even
// with several instances. Sending happens after the commit; a failed push is not retried,
// because the notification is still in the app.
type Dispatcher struct {
	pool   *pgxpool.Pool
	sender Sender
	log    *slog.Logger
}

func NewDispatcher(pool *pgxpool.Pool, sender Sender, log *slog.Logger) *Dispatcher {
	return &Dispatcher{pool: pool, sender: sender, log: log}
}

// Run pushes whenever a notification is stored, until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		connected, err := d.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			delay = time.Second
		}
		d.log.WarnContext(ctx, "push listener disconnected", "err", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 30*time.Second)
	}
}

// listen reports whether it got as far as listening, so Run can reset its backoff.
func (d *Dispatcher) listen(ctx context.Context) (connected bool, err error) {
	conn, err := pgx.ConnectConfig(ctx, d.pool.Config().ConnConfig)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+realtime.Channel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}
	for {
		if _, err := d.Dispatch(ctx); err != nil && ctx.Err() == nil {
			d.log.ErrorContext(ctx, "push dispatch failed", "err", err)
		}
		// Wait for the next notification event, or sweep when none comes.
		for {
			wait, cancel := context.WithTimeout(ctx, sweep)
			n, err := conn.WaitForNotification(wait)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return true, ctx.Err()
				}
				if errors.Is(err, context.DeadlineExceeded) {
					break
				}
				return true, fmt.Errorf("wait for notification: %w", err)
			}
			if strings.Contains(n.Payload, `"type":"`+realtime.TypeNotification+`"`) {
				break
			}
		}
	}
}

type job struct {
	device      Device
	web, native Message
}

// Dispatch pushes everything in the queue and reports how many pushes were attempted.
func (d *Dispatcher) Dispatch(ctx context.Context) (int, error) {
	total := 0
	for {
		jobs, claimed, err := d.claim(ctx)
		if err != nil {
			return total, err
		}
		d.send(ctx, jobs)
		total += len(jobs)
		if claimed < claimBatch {
			return total, nil
		}
	}
}

func (d *Dispatcher) claim(ctx context.Context) ([]job, int, error) {
	var jobs []job
	var claimed int
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if _, err := q.PushExpireStale(ctx); err != nil {
			return fmt.Errorf("expire stale pushes: %w", err)
		}
		rows, err := q.PushClaimNotifications(ctx)
		if err != nil {
			return fmt.Errorf("claim notifications: %w", err)
		}
		claimed = len(rows)
		if claimed == 0 {
			return nil
		}
		ids := make([]pgtype.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if err := q.PushMarkHandled(ctx, ids); err != nil {
			return fmt.Errorf("mark pushes handled: %w", err)
		}
		readable, err := readableMailboxes(ctx, q, rows)
		if err != nil {
			return err
		}
		var users []pgtype.UUID
		wanted := map[pgtype.UUID][2]Message{}
		for _, r := range rows {
			// Like the notification list: a user who lost access to the mailbox hears nothing.
			if !readable[r.UserID][r.ConversationMailboxID] {
				continue
			}
			web, native, ok := messageFor(r)
			if !ok {
				continue
			}
			wanted[r.ID] = [2]Message{web, native}
			users = append(users, r.UserID)
		}
		if len(users) == 0 {
			return nil
		}
		devices, err := q.PushDevicesOfUsers(ctx, users)
		if err != nil {
			return fmt.Errorf("load push devices: %w", err)
		}
		byUser := map[pgtype.UUID][]Device{}
		for _, dv := range devices {
			byUser[dv.UserID] = append(byUser[dv.UserID], Device{ID: dv.ID, Kind: dv.Kind, Endpoint: dv.Endpoint, P256dh: dv.P256dh, Auth: dv.Auth})
		}
		for _, r := range rows {
			m, ok := wanted[r.ID]
			if !ok {
				continue
			}
			for _, dv := range byUser[r.UserID] {
				jobs = append(jobs, job{device: dv, web: m[0], native: m[1]})
			}
		}
		return nil
	})
	return jobs, claimed, err
}

// readableMailboxes is, per recipient, the set of mailboxes they may read now.
func readableMailboxes(ctx context.Context, q *dbq.Queries, rows []dbq.PushClaimNotificationsRow) (map[pgtype.UUID]map[pgtype.UUID]bool, error) {
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	users, err := q.PushUsers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load recipients: %w", err)
	}
	out := make(map[pgtype.UUID]map[pgtype.UUID]bool, len(users))
	for _, u := range users {
		scope, err := policy.MailboxScope(ctx, q, u)
		if err != nil {
			return nil, err
		}
		set := make(map[pgtype.UUID]bool, len(scope.Read))
		for _, m := range scope.Read {
			set[m] = true
		}
		out[u.ID] = set
	}
	return out, nil
}

func (d *Dispatcher) send(ctx context.Context, jobs []job) {
	if len(jobs) == 0 {
		return
	}
	q := dbq.New(d.pool)
	var mu sync.Mutex
	var delivered []pgtype.UUID
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, j := range jobs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			err := d.sender.Send(ctx, j.device, j.web, j.native)
			switch {
			case err == nil:
				mu.Lock()
				delivered = append(delivered, j.device.ID)
				mu.Unlock()
			case errors.Is(err, ErrGone):
				if err := q.PushDropDevice(ctx, j.device.ID); err != nil {
					d.log.ErrorContext(ctx, "drop push device", "err", err)
				}
			default:
				d.log.WarnContext(ctx, "push not delivered", "kind", j.device.Kind, "device", j.device.ID.String(), "err", err)
			}
		})
	}
	wg.Wait()
	if len(delivered) > 0 {
		if err := q.PushTouchDevices(ctx, delivered); err != nil {
			d.log.ErrorContext(ctx, "record push delivery", "err", err)
		}
	}
}

// messageFor builds the texts for one notification, or reports that it is not pushed: the
// user turned the kind off, the user is deactivated, or the conversation is in the trash.
// Web Push is end-to-end encrypted to the browser and carries the subject; APNs and FCM see
// their payload, so native pushes stop at the conversation number, like notification mail.
func messageFor(r dbq.PushClaimNotificationsRow) (web, native Message, ok bool) {
	if r.RecipientDeactivatedAt.Valid || r.ConversationDeleted {
		return web, native, false
	}
	var title string
	switch r.Kind {
	case compose.KindMention:
		if !r.PushNotifyMentions {
			return web, native, false
		}
		title = "Je bent genoemd"
		if r.ActorName.Valid {
			title = r.ActorName.String + " noemde je"
		}
	case compose.KindAssigned:
		if !r.PushNotifyAssignments {
			return web, native, false
		}
		title = "Aan jou toegewezen"
		if r.ActorName.Valid {
			title = r.ActorName.String + " wees een gesprek aan je toe"
		}
	case compose.KindReply:
		if !r.PushNotifyReplies {
			return web, native, false
		}
		title = "Nieuw antwoord van de klant"
	case compose.KindSLA:
		if !r.PushNotifySla {
			return web, native, false
		}
		title = "SLA-termijn in gevaar"
	default:
		return web, native, false
	}
	number := fmt.Sprintf("Gesprek #%d", r.ConversationNumber)
	url := "/inbox/alle/" + r.ConversationID.String()
	tag := "conversation-" + r.ConversationID.String()
	native = Message{Title: title, Body: number, URL: url, Tag: tag}
	web = native
	if subject := strings.TrimSpace(r.ConversationSubject); subject != "" {
		// The subject comes from the customer; a push service takes at most 4 KB in all.
		if runes := []rune(subject); len(runes) > maxSubject {
			subject = string(runes[:maxSubject]) + "…"
		}
		web.Body = number + " · " + subject
	}
	return web, native, true
}
