package contacts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"echoo/internal/db/dbq"
	"echoo/internal/storage"
)

const drainBatch = 500

// Blobs is a store that can also delete: import files, exports and erased attachments must
// not outlive their purpose.
type Blobs interface {
	storage.Store
	storage.Deleter
}

// AsBlobs returns store as Blobs, or an error when the backend cannot delete.
func AsBlobs(store storage.Store) (Blobs, error) {
	b, ok := store.(Blobs)
	if !ok {
		return nil, errors.New("the configured storage backend cannot delete blobs")
	}
	return b, nil
}

// QueueBlobDeletions records keys whose references were just dropped. Call it with the
// Queries of the transaction that dropped them, so the request survives a crash right after
// the commit; DrainBlobDeletions does the deleting.
func QueueBlobDeletions(ctx context.Context, q *dbq.Queries, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := q.QueueBlobDeletions(ctx, keys); err != nil {
		return fmt.Errorf("queue blob deletions: %w", err)
	}
	return nil
}

// DrainBlobDeletions deletes queued blobs that nothing refers to any more (attachments, raw
// messages, uploads, the image cache, imports and exports are all checked) and forgets the
// ones that are still in use, e.g. the same PDF sent by two customers. It returns how many
// deletions failed; those stay queued and the purge job retries them. Only call it after the
// transaction that dropped the references committed.
func DrainBlobDeletions(ctx context.Context, q *dbq.Queries, blobs Blobs) (int, error) {
	keys, err := q.ListPendingBlobDeletions(ctx, drainBatch)
	if err != nil {
		return 0, fmt.Errorf("list pending blob deletions: %w", err)
	}
	failed := 0
	for _, key := range keys {
		referenced, err := q.BlobIsReferenced(ctx, key)
		if err == nil && !referenced {
			err = blobs.Delete(ctx, key)
		}
		if err == nil {
			err = q.DeletePendingBlobDeletion(ctx, key)
		}
		if err != nil {
			failed++
			slog.ErrorContext(ctx, "delete blob", "key", key, "err", err)
		}
	}
	return failed, nil
}

// DrainAllBlobDeletions repeats DrainBlobDeletions until the queue is empty. It stops at the
// first round with a failed deletion, because retrying the same file right away helps nothing,
// and returns how many failed in that round.
func DrainAllBlobDeletions(ctx context.Context, q *dbq.Queries, blobs Blobs) (int, error) {
	for {
		failed, err := DrainBlobDeletions(ctx, q, blobs)
		if err != nil || failed > 0 {
			return failed, err
		}
		pending, err := q.CountPendingBlobDeletions(ctx)
		if err != nil {
			return 0, fmt.Errorf("count pending blob deletions: %w", err)
		}
		if pending == 0 {
			return 0, nil
		}
	}
}

// DeleteUnreferenced queues keys and drains right away. It returns the number of failures;
// the keys stay queued for the purge job.
func DeleteUnreferenced(ctx context.Context, q *dbq.Queries, blobs Blobs, keys []string) int {
	if err := QueueBlobDeletions(ctx, q, keys); err != nil {
		slog.ErrorContext(ctx, "queue blob deletions", "err", err)
		return len(keys)
	}
	failed, err := DrainBlobDeletions(ctx, q, blobs)
	if err != nil {
		slog.ErrorContext(ctx, "drain blob deletions", "err", err)
		return max(failed, 1)
	}
	return failed
}
