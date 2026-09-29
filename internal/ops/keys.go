// Package ops holds the operator tools behind `echoo admin`: re-encrypting stored secrets with
// the active key and checking the blob store against the database.
package ops

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
)

// Column is one encrypted column. IDExpr is a SQL expression that yields the row's id as text;
// it must equal the row id the code that wrote the value used in keyring.AAD.
type Column struct {
	Table, Column, IDExpr string
}

// EncryptedColumns lists everything the application encrypts with the keyring and stores.
// System mail payloads and OAuth state are ephemeral and never outlive a rotation window, so
// they are not here. A new `_enc` column must be added; TestEveryEncryptedColumnIsListed fails
// until it is.
var EncryptedColumns = []Column{
	{"mailboxes", "imap_secret_enc", "id::text"},
	{"mailboxes", "smtp_secret_enc", "id::text"},
	{"mailboxes", "oauth_token_enc", "id::text"},
	{"mailboxes", "inbound_secret_enc", "id::text"},
	{"users", "totp_secret_enc", "id::text"},
	{"webhooks", "secret_enc", "id::text"},
	{"sso_settings", "client_secret_enc", "'singleton'"},
}

func (c Column) name() string { return c.Table + "." + c.Column }

func (c Column) sql(format string) string {
	return fmt.Sprintf(format, pgx.Identifier{c.Table}.Sanitize(), pgx.Identifier{c.Column}.Sanitize(), c.IDExpr)
}

// keyIDOf extracts the key id from the header of a stored value in SQL: version, length, id.
const keyIDOf = `CASE WHEN length(%[1]s) >= 2 AND get_byte(%[1]s, 0) = 1
    THEN encode(substring(%[1]s FROM 3 FOR get_byte(%[1]s, 1)), 'escape') ELSE '?' END`

// KeyUsage counts stored values per key id, per column.
type KeyUsage map[string]map[string]int64

// Total is the number of values encrypted with the key.
func (u KeyUsage) Total(keyID string) int64 {
	var n int64
	for _, c := range u[keyID] {
		n += c
	}
	return n
}

// CountKeyUsage reports which key each stored value was encrypted with. The id '?' stands for
// a value whose header cannot be read.
func CountKeyUsage(ctx context.Context, pool *pgxpool.Pool) (KeyUsage, error) {
	usage := KeyUsage{}
	for _, c := range EncryptedColumns {
		col := pgx.Identifier{c.Column}.Sanitize()
		rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT %s AS key_id, count(*) FROM %s WHERE %s IS NOT NULL GROUP BY 1`,
			fmt.Sprintf(keyIDOf, col), pgx.Identifier{c.Table}.Sanitize(), col))
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", c.name(), err)
		}
		for rows.Next() {
			var id string
			var n int64
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if usage[id] == nil {
				usage[id] = map[string]int64{}
			}
			usage[id][c.name()] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("count %s: %w", c.name(), err)
		}
	}
	return usage, nil
}

// RotationReport counts what a rotation did.
type RotationReport struct {
	Rotated int
	// Failed values could not be decrypted with any configured key; they are left as they were.
	Failed int
	// Raced values were changed by the application between reading and writing; run again.
	Raced int
}

const rotateBatch = 200

// Rotate re-encrypts every value that is not encrypted with the active key. It is idempotent:
// a second run finds nothing to do. Progress goes to out, one line per column.
func Rotate(ctx context.Context, pool *pgxpool.Pool, keys *keyring.Keyring, out io.Writer) (RotationReport, error) {
	var rep RotationReport
	for _, c := range EncryptedColumns {
		colReport, err := rotateColumn(ctx, pool, keys, c)
		rep.Rotated += colReport.Rotated
		rep.Failed += colReport.Failed
		rep.Raced += colReport.Raced
		if err != nil {
			return rep, fmt.Errorf("%s: %w", c.name(), err)
		}
		if _, err := fmt.Fprintf(out, "%-28s re-encrypted %d, unreadable %d, changed meanwhile %d\n", c.name(), colReport.Rotated, colReport.Failed, colReport.Raced); err != nil {
			return rep, err
		}
	}
	if rep.Rotated+rep.Failed > 0 {
		err := audit.Write(ctx, dbq.New(pool), audit.Entry{Action: audit.KeysRotated, TargetType: "keys", TargetID: keys.ActiveID(),
			Metadata: map[string]any{"rotated": rep.Rotated, "unreadable": rep.Failed, "raced": rep.Raced}})
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func rotateColumn(ctx context.Context, pool *pgxpool.Pool, keys *keyring.Keyring, c Column) (RotationReport, error) {
	var rep RotationReport
	after := ""
	for {
		type row struct {
			id string
			ct []byte
		}
		rows, err := pool.Query(ctx, c.sql(`SELECT id, ct FROM (SELECT %[3]s AS id, %[2]s AS ct FROM %[1]s WHERE %[2]s IS NOT NULL) AS v WHERE id > $1 ORDER BY id LIMIT $2`), after, rotateBatch)
		if err != nil {
			return rep, err
		}
		var batch []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.ct); err != nil {
				rows.Close()
				return rep, err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return rep, err
		}
		if len(batch) == 0 {
			return rep, nil
		}
		for _, r := range batch {
			after = r.id
			if !keys.NeedsRotation(r.ct) {
				continue
			}
			aad := keyring.AAD(c.Table, c.Column, r.id)
			plain, err := keys.Decrypt(r.ct, aad)
			if err != nil {
				rep.Failed++
				continue
			}
			sealed, err := keys.Encrypt(plain, aad)
			if err != nil {
				return rep, err
			}
			// Only replace the value that was read: if the application changed it meanwhile it
			// was already encrypted with the active key and needs nothing.
			tag, err := pool.Exec(ctx, c.sql(`UPDATE %[1]s SET %[2]s = $2 WHERE %[3]s = $1 AND %[2]s = $3`), r.id, sealed, r.ct)
			if err != nil {
				return rep, err
			}
			if tag.RowsAffected() == 0 {
				rep.Raced++
			} else {
				rep.Rotated++
			}
		}
	}
}

// RetiredKeys lists the configured keys other than the active one, with the number of stored
// values still encrypted with each.
func RetiredKeys(keys *keyring.Keyring, usage KeyUsage) map[string]int64 {
	out := map[string]int64{}
	for _, id := range keys.IDs() {
		if id != keys.ActiveID() {
			out[id] = usage.Total(id)
		}
	}
	return out
}

// UnusableKeys lists key ids found in the database that are not configured, with counts. Their
// values cannot be read until the key is configured again.
func UnusableKeys(keys *keyring.Keyring, usage KeyUsage) map[string]int64 {
	known := map[string]bool{}
	for _, id := range keys.IDs() {
		known[id] = true
	}
	out := map[string]int64{}
	for id := range usage {
		if !known[id] {
			out[id] = usage.Total(id)
		}
	}
	return out
}

// SortedKeys returns the keys of m in order, for stable output.
func SortedKeys(m map[string]int64) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
