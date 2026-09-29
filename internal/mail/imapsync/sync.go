package imapsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

// inboxSync fetches new INBOX messages over one connection. folder.LastUid is the in-memory
// copy of the persisted cursor.
type inboxSync struct {
	s        *Supervisor
	c        *imapclient.Client
	folder   dbq.MailboxFolder
	markSeen bool
}

// pass fetches everything above the cursor in batches.
func (y *inboxSync) pass(ctx context.Context) error {
	uids, err := y.newUIDs()
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(uids, y.s.cfg.BatchSize) {
		if err := y.fetchBatch(ctx, batch); err != nil {
			return err
		}
	}
	if err := dbq.New(y.s.deps.Pool).TouchMailboxSynced(ctx, y.s.id); err != nil {
		return fmt.Errorf("record sync time: %w", err)
	}
	if len(uids) > 0 {
		y.s.log.Info("fetched new messages", "count", len(uids))
	}
	return nil
}

func (y *inboxSync) newUIDs() ([]imap.UID, error) {
	cursor := imap.UID(y.folder.LastUid) //nolint:gosec // UIDs are 32-bit
	var set imap.UIDSet
	// "N:*" always matches the highest message, even one at or below the cursor.
	set.AddRange(cursor+1, 0)
	data, err := y.c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search new messages: %w", err)
	}
	uids := slices.DeleteFunc(data.AllUIDs(), func(u imap.UID) bool { return u <= cursor })
	slices.Sort(uids)
	return uids, nil
}

// maxHeaderBytes caps the headers kept for a message that is too large to fetch.
const maxHeaderBytes = 256 << 10

// fetchBatch stores every message of the batch and only then advances the cursor, so the
// cursor never passes a message that is not stored. A UID the server no longer returns was
// expunged in the meantime and is skipped by the cursor as well.
func (y *inboxSync) fetchBatch(ctx context.Context, uids []imap.UID) error {
	var set imap.UIDSet
	set.AddNum(uids...)

	sizes, err := y.sizes(set)
	if err != nil {
		return err
	}
	oversized := map[imap.UID]int64{}
	var normal []imap.UID
	for _, uid := range uids {
		if size, ok := sizes[uid]; ok && size > y.s.cfg.MaxMessageBytes {
			oversized[uid] = size
		} else {
			normal = append(normal, uid)
		}
	}

	if len(normal) > 0 {
		if err := y.fetchBodies(ctx, normal, oversized); err != nil {
			return err
		}
	}
	if len(oversized) > 0 {
		if err := y.skipOversized(ctx, oversized); err != nil {
			return err
		}
	}

	last := uids[len(uids)-1]
	err = dbq.New(y.s.deps.Pool).AdvanceFolderCursor(ctx, dbq.AdvanceFolderCursorParams{ID: y.folder.ID, LastUid: int64(last)})
	if err != nil {
		return fmt.Errorf("advance cursor: %w", err)
	}
	y.folder.LastUid = int64(last)

	if y.markSeen {
		flags := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
		if err := y.c.Store(set, flags, nil).Close(); err != nil {
			return fmt.Errorf("mark messages seen: %w", err)
		}
	}
	return nil
}

func (y *inboxSync) sizes(set imap.UIDSet) (map[imap.UID]int64, error) {
	bufs, err := y.c.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message sizes: %w", err)
	}
	sizes := make(map[imap.UID]int64, len(bufs))
	for _, b := range bufs {
		sizes[b.UID] = b.RFC822Size
	}
	return sizes, nil
}

// fetchBodies stores the messages in uids. Bodies are read through a limit, so a server that
// understates RFC822.SIZE cannot make us buffer more than the maximum; such messages are
// added to oversized instead.
func (y *inboxSync) fetchBodies(ctx context.Context, uids []imap.UID, oversized map[imap.UID]int64) error {
	var set imap.UIDSet
	set.AddNum(uids...)
	limit := y.s.cfg.MaxMessageBytes
	// PEEK keeps the fetch from setting \Seen.
	section := &imap.FetchItemBodySection{Peek: true}
	cmd := y.c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	drained := false
	defer func() {
		if !drained {
			abandonFetch(cmd)
		}
	}()
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		var (
			uid      imap.UID
			body     []byte
			gotBody  bool
			overflow bool
		)
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch it := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = it.UID
			case imapclient.FetchItemDataBodySection:
				gotBody = true
				if it.Literal == nil {
					continue
				}
				b, err := io.ReadAll(io.LimitReader(it.Literal, limit+1))
				if err != nil {
					return fmt.Errorf("read message body: %w", err)
				}
				if int64(len(b)) > limit {
					overflow = true
				} else {
					body = b
				}
			}
		}
		switch {
		case !gotBody:
			return fmt.Errorf("fetch message %d: server sent no body", uid)
		case overflow:
			oversized[uid] = limit + 1
		default:
			if err := y.storeMessage(ctx, uid, body); err != nil {
				return err
			}
		}
	}
	drained = true
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("fetch messages: %w", err)
	}
	return nil
}

// skipOversized records each message as skipped with only its headers as the stored blob and
// without a parse job.
func (y *inboxSync) skipOversized(ctx context.Context, oversized map[imap.UID]int64) error {
	uids := slices.Sorted(maps.Keys(oversized))
	for _, uid := range uids {
		var set imap.UIDSet
		set.AddNum(uid)
		section := &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierHeader,
			Peek:      true,
			Partial:   &imap.SectionPartial{Offset: 0, Size: maxHeaderBytes},
		}
		bufs, err := y.c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		if err != nil {
			return fmt.Errorf("fetch headers of message %d: %w", uid, err)
		}
		var headers []byte
		if len(bufs) > 0 && len(bufs[0].BodySection) > 0 {
			headers = bufs[0].BodySection[0].Bytes
		}
		if len(headers) > maxHeaderBytes {
			headers = headers[:maxHeaderBytes]
		}
		size := oversized[uid]
		reason := fmt.Sprintf("message too large: %d bytes", size)
		if err := y.storeRaw(ctx, uid, headers, min(size, math.MaxInt32), reason); err != nil {
			return err
		}
		y.s.log.Warn("skipped oversized message", "size_bytes", size)
	}
	return nil
}

func (y *inboxSync) storeMessage(ctx context.Context, uid imap.UID, body []byte) error {
	if len(body) > math.MaxInt32 {
		return fmt.Errorf("message %d is too large (%d bytes)", uid, len(body))
	}
	return y.storeRaw(ctx, uid, body, int64(len(body)), "")
}

// storeRaw writes the blob, then inserts the row and, unless skipReason is set, its parse job
// in one transaction. A conflict on the IMAP unique index means the message is already
// stored: no job is queued.
func (y *inboxSync) storeRaw(ctx context.Context, uid imap.UID, data []byte, size int64, skipReason string) (err error) {
	key, sum, err := y.s.deps.Store.Put(ctx, data)
	if err != nil {
		return fmt.Errorf("store message %d: %w", uid, err)
	}
	if y.s.afterPut != nil {
		if err := y.s.afterPut(uid); err != nil {
			return err
		}
	}

	tx, err := y.s.deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rbErr)
		}
	}()

	q := dbq.New(tx)
	var id pgtype.UUID
	if skipReason != "" {
		id, err = q.InsertSkippedImapRawMessage(ctx, dbq.InsertSkippedImapRawMessageParams{
			MailboxID:   y.s.id,
			FolderID:    y.folder.ID,
			Uidvalidity: pgtype.Int8{Int64: y.folder.Uidvalidity, Valid: true},
			Uid:         pgtype.Int8{Int64: int64(uid), Valid: true},
			Sha256:      sum,
			SizeBytes:   int32(size), //nolint:gosec // callers bound size by MaxInt32
			BlobKey:     key,
			ParseError:  skipReason,
		})
	} else {
		id, err = q.InsertImapRawMessage(ctx, dbq.InsertImapRawMessageParams{
			MailboxID:   y.s.id,
			FolderID:    y.folder.ID,
			Uidvalidity: pgtype.Int8{Int64: y.folder.Uidvalidity, Valid: true},
			Uid:         pgtype.Int8{Int64: int64(uid), Valid: true},
			Sha256:      sum,
			SizeBytes:   int32(size), //nolint:gosec // callers bound size by MaxInt32
			BlobKey:     key,
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("insert raw message %d: %w", uid, err)
	}
	if skipReason == "" {
		if _, err := y.s.deps.River.InsertTx(ctx, tx, jobs.ParseRaw{RawMessageID: id.String()}, nil); err != nil {
			return fmt.Errorf("enqueue parse job for message %d: %w", uid, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit message %d: %w", uid, err)
	}
	return nil
}

// abandonFetch drains a FETCH that stopped being consumed. The client's reader goroutine
// blocks handing over the next message, and Client.Close waits for that goroutine, so
// without a consumer the connection could never be closed. The result is irrelevant: the
// session is about to end.
func abandonFetch(cmd *imapclient.FetchCommand) {
	go func() { _ = cmd.Close() }()
}
