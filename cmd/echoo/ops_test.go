package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"echoo/internal/keyring"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const (
	oldKey = "k1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	newKey = "k2:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
)

func TestRotateKeysCommandReportsWhenTheOldKeyCanGo(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	old, err := keyring.Parse(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.Parse(newKey + "," + oldKey)
	if err != nil {
		t.Fatal(err)
	}
	var mailbox string
	if err := pool.QueryRow(ctx, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id::text`).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Encrypt([]byte("geheim"), keyring.AAD("mailboxes", "imap_secret_enc", mailbox))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mailboxes SET imap_secret_enc = $1 WHERE id = $2`, sealed, mailbox); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runRotateKeys(ctx, &app{pool: pool, keys: ring}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"Active key: k2", "old key k1 no longer used", "Done: 1 values re-encrypted."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// A value under a key that is not configured fails the command and names the key.
	if _, err := pool.Exec(ctx, `UPDATE mailboxes SET smtp_secret_enc = $1 WHERE id = $2`, sealed, mailbox); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	onlyNew, err := keyring.Parse(newKey)
	if err != nil {
		t.Fatal(err)
	}
	err = runRotateKeys(ctx, &app{pool: pool, keys: onlyNew}, &out)
	if err == nil || !strings.Contains(err.Error(), "rotation incomplete") || !strings.Contains(out.String(), `use key "k1", which is not configured`) {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
}
