package imapsync

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errLockLost = errors.New("advisory lock connection lost")

// advisoryLock is a session-level Postgres advisory lock on a connection taken out of the pool.
// The lock lives exactly as long as that connection, so a crashed process loses it too.
type advisoryLock struct {
	conn *pgx.Conn
	key  int64
}

func lockKey(mailboxID pgtype.UUID) int64 {
	sum := sha256.Sum256(append([]byte("echoo/imapsync/"), mailboxID.Bytes[:]...))
	return int64(binary.BigEndian.Uint64(sum[:8])) //nolint:gosec // bit pattern reinterpretation is intended
}

// acquireLock reports false when another session holds the lock.
func acquireLock(ctx context.Context, pool *pgxpool.Pool, key int64) (*advisoryLock, bool, error) {
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire connection: %w", err)
	}
	// Hijacking keeps the long-lived lock connection from counting against the pool.
	conn := pc.Hijack()
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !ok {
		if err := conn.Close(context.WithoutCancel(ctx)); err != nil {
			return nil, false, fmt.Errorf("close connection: %w", err)
		}
		return nil, false, nil
	}
	return &advisoryLock{conn: conn, key: key}, true, nil
}

// check verifies the lock connection is still alive; if it is not, the lock is gone.
func (l *advisoryLock) check(ctx context.Context) error {
	if _, err := l.conn.Exec(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("%w: %w", errLockLost, err)
	}
	return nil
}

func (l *advisoryLock) release(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, unlockErr := l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", l.key)
	// Closing the connection releases the lock even if the explicit unlock failed.
	return errors.Join(unlockErr, l.conn.Close(ctx))
}
