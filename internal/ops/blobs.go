package ops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db/dbq"
	"echoo/internal/storage"
)

// blobReferences yields every blob key the database refers to and the column it came from.
// It mirrors the BlobIsReferenced query; TestBlobReferencesCoverEveryBlobColumn fails when a
// new blob column is missing from either.
const blobReferences = `
SELECT DISTINCT key, source FROM (
    SELECT blob_key AS key, 'attachments.blob_key' AS source FROM attachments
    UNION ALL SELECT blob_key, 'raw_messages.blob_key' FROM raw_messages
    UNION ALL SELECT blob_key, 'uploads.blob_key' FROM uploads
    UNION ALL SELECT blob_key, 'image_proxy_cache.blob_key' FROM image_proxy_cache
    UNION ALL SELECT source_key, 'contact_imports.source_key' FROM contact_imports
    UNION ALL SELECT error_key, 'contact_imports.error_key' FROM contact_imports
    UNION ALL SELECT blob_key, 'data_exports.blob_key' FROM data_exports
    UNION ALL SELECT blob_key, 'kb_images.blob_key' FROM kb_images
) refs WHERE key <> ''`

// MissingBlob is a key the database refers to that the store does not have.
type MissingBlob struct {
	Key, Source string
}

// BlobReport is the result of comparing the database with the blob store.
type BlobReport struct {
	// Referenced is the number of distinct keys the database refers to.
	Referenced int
	Missing    []MissingBlob
	// Walked is true when the store could be listed (filesystem storage), so Orphans is
	// meaningful.
	Walked bool
	// Orphans are files nothing refers to, older than the grace period.
	Orphans       []string
	OrphanBytes   int64
	SkippedRecent int
}

// OrphanGrace keeps files that were written recently out of the orphan list: a blob is stored
// before the row that refers to it is committed.
const OrphanGrace = 24 * time.Hour

const walkBatch = 500

// CheckBlobs lists keys the database refers to that the store lacks and, when the store can be
// listed, files nothing refers to. now is the reference for the grace period.
func CheckBlobs(ctx context.Context, pool *pgxpool.Pool, store storage.Store, now time.Time) (BlobReport, error) {
	var rep BlobReport
	rows, err := pool.Query(ctx, blobReferences)
	if err != nil {
		return rep, fmt.Errorf("list referenced blobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, source string
		if err := rows.Scan(&key, &source); err != nil {
			return rep, err
		}
		rep.Referenced++
		rc, err := store.Open(ctx, key)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			rep.Missing = append(rep.Missing, MissingBlob{Key: key, Source: source})
		case err != nil:
			return rep, fmt.Errorf("open blob %s: %w", key, err)
		default:
			if err := rc.Close(); err != nil {
				return rep, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("list referenced blobs: %w", err)
	}

	walker, ok := store.(storage.Walker)
	if !ok {
		return rep, nil
	}
	rep.Walked = true
	q := dbq.New(pool)
	type file struct {
		key  string
		size int64
	}
	var batch []file
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		keys := make([]string, len(batch))
		for i, f := range batch {
			keys[i] = f.key
		}
		referenced, err := q.OpsReferencedBlobKeys(ctx, keys)
		if err != nil {
			return fmt.Errorf("check referenced blobs: %w", err)
		}
		used := make(map[string]bool, len(referenced))
		for _, k := range referenced {
			used[k] = true
		}
		for _, f := range batch {
			if !used[f.key] {
				rep.Orphans = append(rep.Orphans, f.key)
				rep.OrphanBytes += f.size
			}
		}
		batch = batch[:0]
		return nil
	}
	err = walker.Walk(ctx, func(key string, size int64, modTime time.Time) error {
		if now.Sub(modTime) < OrphanGrace {
			rep.SkippedRecent++
			return nil
		}
		batch = append(batch, file{key, size})
		if len(batch) >= walkBatch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return rep, fmt.Errorf("walk blob store: %w", err)
	}
	return rep, flush()
}

// DeleteOrphans removes the files in orphans through the reference-checked deletion queue, so a
// blob that gained a reference since the scan stays. It returns how many keys were handed over
// and how many deletions failed.
func DeleteOrphans(ctx context.Context, pool *pgxpool.Pool, blobs contacts.Blobs, orphans []string) (queued, failed int, err error) {
	q := dbq.New(pool)
	if err := contacts.QueueBlobDeletions(ctx, q, orphans); err != nil {
		return 0, 0, err
	}
	failed, err = contacts.DrainAllBlobDeletions(ctx, q, blobs)
	if err != nil {
		return len(orphans), failed, err
	}
	err = audit.Write(ctx, q, audit.Entry{Action: audit.BlobsDeleted, TargetType: "blobs",
		Metadata: map[string]any{"orphans": len(orphans), "failed": failed}})
	return len(orphans), failed, err
}
