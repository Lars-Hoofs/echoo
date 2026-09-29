package ops

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func newRing(t *testing.T, spec string) *keyring.Keyring {
	t.Helper()
	k, err := keyring.Parse(spec)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

const (
	key1 = "k1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	key2 = "k2:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
)

func id(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) pgtype.UUID {
	t.Helper()
	var out pgtype.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type sealedRows struct {
	mailbox, user, hook pgtype.UUID
}

// seed stores one value per encrypted column, sealed with ring and bound to its row.
func seed(t *testing.T, pool *pgxpool.Pool, ring *keyring.Keyring) sealedRows {
	t.Helper()
	ctx := context.Background()
	seal := func(table, column string, rowID pgtype.UUID, plain string) []byte {
		b, err := ring.Encrypt([]byte(plain), keyring.AAD(table, column, rowID.String()))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var out sealedRows
	out.mailbox = id(t, pool, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id`)
	for _, col := range []string{"imap_secret_enc", "smtp_secret_enc", "oauth_token_enc", "inbound_secret_enc"} {
		if _, err := pool.Exec(ctx, `UPDATE mailboxes SET `+col+` = $2 WHERE id = $1`, out.mailbox, seal("mailboxes", col, out.mailbox, "geheim "+col)); err != nil {
			t.Fatal(err)
		}
	}
	u, err := dbq.New(pool).CreateUser(ctx, dbq.CreateUserParams{Email: "a@example.com", Name: "A", Role: "agent", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	out.user = u.ID
	if _, err := pool.Exec(ctx, `UPDATE users SET totp_secret_enc = $2 WHERE id = $1`, u.ID, seal("users", "totp_secret_enc", u.ID, "totp")); err != nil {
		t.Fatal(err)
	}
	out.hook = id(t, pool, `INSERT INTO webhooks (url, secret_enc, events) VALUES ('https://example.com/h', ''::bytea, '{conversation.created}') RETURNING id`)
	if _, err := pool.Exec(ctx, `UPDATE webhooks SET secret_enc = $2 WHERE id = $1`, out.hook, seal("webhooks", "secret_enc", out.hook, "hook")); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRotateMovesEveryValueToTheActiveKeyAndIsIdempotent(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	old := newRing(t, key1)
	rows := seed(t, pool, old)
	ring := newRing(t, key2+","+key1)

	usage, err := CountKeyUsage(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Total("k1") != 6 || usage.Total("k2") != 0 {
		t.Fatalf("before: %v", usage)
	}

	var out bytes.Buffer
	rep, err := Rotate(ctx, pool, ring, &out)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rotated != 6 || rep.Failed != 0 || rep.Raced != 0 {
		t.Fatalf("report = %+v\n%s", rep, out.String())
	}
	for _, want := range []string{"mailboxes.imap_secret_enc", "users.totp_secret_enc", "webhooks.secret_enc", "re-encrypted 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("progress lacks %q:\n%s", want, out.String())
		}
	}

	usage, err = CountKeyUsage(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Total("k1") != 0 || usage.Total("k2") != 6 {
		t.Fatalf("after: %v", usage)
	}
	if retired := RetiredKeys(ring, usage); retired["k1"] != 0 || len(retired) != 1 {
		t.Errorf("retired = %v", retired)
	}
	// Everything still decrypts with only the new key, bound to its own row.
	only := newRing(t, key2)
	var ct []byte
	if err := pool.QueryRow(ctx, `SELECT smtp_secret_enc FROM mailboxes WHERE id = $1`, rows.mailbox).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if pt, err := only.Decrypt(ct, keyring.AAD("mailboxes", "smtp_secret_enc", rows.mailbox.String())); err != nil || string(pt) != "geheim smtp_secret_enc" {
		t.Fatalf("decrypt = %q, %v", pt, err)
	}
	if count(t, pool, `SELECT count(*) FROM audit_log WHERE action = $1 AND (metadata->>'rotated')::int = 6`, audit.KeysRotated) != 1 {
		t.Error("the rotation should be audited with its count")
	}

	rep, err = Rotate(ctx, pool, ring, &bytes.Buffer{})
	if err != nil || rep != (RotationReport{}) {
		t.Fatalf("second run: %+v, %v", rep, err)
	}
	if count(t, pool, `SELECT count(*) FROM audit_log WHERE action = $1`, audit.KeysRotated) != 1 {
		t.Error("a run that changed nothing writes no audit entry")
	}
}

func TestRotateLeavesUnreadableValuesAloneAndReportsThem(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	old := newRing(t, key1)
	rows := seed(t, pool, old)
	// A value copied to another row fails the AAD check, like tampering or a restore mix-up.
	if _, err := pool.Exec(ctx, `UPDATE users SET totp_secret_enc = (SELECT imap_secret_enc FROM mailboxes WHERE id = $1) WHERE id = $2`, rows.mailbox, rows.user); err != nil {
		t.Fatal(err)
	}
	rep, err := Rotate(ctx, pool, newRing(t, key2+","+key1), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 1 || rep.Rotated != 5 {
		t.Fatalf("report = %+v", rep)
	}
	usage, err := CountKeyUsage(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if usage["k1"]["users.totp_secret_enc"] != 1 {
		t.Errorf("the unreadable value keeps its old key: %v", usage)
	}
}

func TestUnusableKeys(t *testing.T) {
	pool := testdb.New(t)
	seed(t, pool, newRing(t, key1))
	usage, err := CountKeyUsage(ctx(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if got := UnusableKeys(newRing(t, key2), usage); got["k1"] != 6 {
		t.Errorf("UnusableKeys = %v", got)
	}
}

func ctx() context.Context { return context.Background() }

func TestEveryEncryptedColumnIsListed(t *testing.T) {
	pool := testdb.New(t)
	rows, err := pool.Query(ctx(), `SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE '%\_enc' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var listed []string
	for _, c := range EncryptedColumns {
		listed = append(listed, c.name())
	}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(listed, col) {
			t.Errorf("%s is encrypted but not rotated: add it to EncryptedColumns", col)
		}
	}
}

func TestBlobReferencesCoverEveryBlobColumn(t *testing.T) {
	pool := testdb.New(t)
	rows, err := pool.Query(ctx(), `SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND (column_name LIKE '%blob%' OR column_name IN ('source_key', 'error_key'))
		  AND table_name <> 'pending_blob_deletions'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(blobReferences, "'"+col+"'") {
			t.Errorf("%s holds blob keys but the orphan scan does not know it", col)
		}
	}
}

func TestCheckBlobsFindsMissingFilesAndOrphans(t *testing.T) {
	pool := testdb.New(t)
	root := t.TempDir()
	store, err := storage.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	c := ctx()
	put := func(content string) (string, []byte) {
		key, sum, err := store.Put(c, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		return key, sum
	}
	mailbox := id(t, pool, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id`)
	conv := id(t, pool, `INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 's') RETURNING id`, mailbox)
	msg := id(t, pool, `INSERT INTO messages (conversation_id, mailbox_id, kind, direction) VALUES ($1, $2, 'email', 'in') RETURNING id`, conv, mailbox)
	attach := func(key string, sum []byte) {
		if _, err := pool.Exec(c, `INSERT INTO attachments (message_id, filename, sniffed_type, size_bytes, sha256, blob_key, disposition)
			VALUES ($1, 'a', 'x', 1, $2, $3, 'attachment')`, msg, sum, key); err != nil {
			t.Fatal(err)
		}
	}
	keptKey, keptSum := put("bewaard")
	attach(keptKey, keptSum)
	// Referenced but never stored.
	missingKey := "sha256/ab/cd/" + strings.Repeat("ab", 32)
	attach(missingKey, make([]byte, 32))
	orphanKey, _ := put("wees")
	youngKey, _ := put("nieuw")
	queuedKey, _ := put("in de wachtrij")
	if _, err := pool.Exec(c, `INSERT INTO pending_blob_deletions (blob_key) VALUES ($1)`, queuedKey); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, k := range []string{keptKey, orphanKey, queuedKey} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(k)), old, old); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := CheckBlobs(c, pool, store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Referenced != 2 || len(rep.Missing) != 1 || rep.Missing[0].Key != missingKey || rep.Missing[0].Source != "attachments.blob_key" {
		t.Errorf("missing = %+v (referenced %d)", rep.Missing, rep.Referenced)
	}
	if !rep.Walked || len(rep.Orphans) != 1 || rep.Orphans[0] != orphanKey || rep.SkippedRecent != 1 {
		t.Errorf("orphans = %v, skipped recent %d", rep.Orphans, rep.SkippedRecent)
	}

	queued, failed, err := DeleteOrphans(c, pool, store, rep.Orphans)
	if err != nil || queued != 1 || failed != 0 {
		t.Fatalf("delete: %d %d %v", queued, failed, err)
	}
	for key, want := range map[string]bool{orphanKey: false, keptKey: true, youngKey: true} {
		rc, err := store.Open(c, key)
		if (err == nil) != want {
			t.Errorf("blob %s exists = %v, want %v", key, err == nil, want)
		}
		if rc != nil {
			_ = rc.Close()
		}
	}
	if count(t, pool, `SELECT count(*) FROM audit_log WHERE action = $1`, audit.BlobsDeleted) != 1 {
		t.Error("the deletion should be audited")
	}
}

// A file that gained a reference after the scan must survive the deletion.
func TestDeleteOrphansKeepsWhatBecameReferenced(t *testing.T) {
	pool := testdb.New(t)
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := ctx()
	key, sum, err := store.Put(c, []byte("net gebruikt"))
	if err != nil {
		t.Fatal(err)
	}
	user, err := dbq.New(pool).CreateUser(c, dbq.CreateUserParams{Email: "u@example.com", Name: "U", Role: "agent", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(c, `INSERT INTO uploads (user_id, filename, content_type, size_bytes, sha256, blob_key, content_id)
		VALUES ($1, 'f', 'text/plain', 1, $2, $3, 'cid')`, user.ID, sum, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DeleteOrphans(c, pool, store, []string{key}); err != nil {
		t.Fatal(err)
	}
	rc, err := store.Open(c, key)
	if err != nil {
		t.Fatalf("a referenced blob must not be deleted: %v", err)
	}
	_ = rc.Close()
}
