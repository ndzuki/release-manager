package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// AdvisoryLock holds a PostgreSQL session-level advisory lock.
// The lock is released automatically when Unlock is called
// or when the underlying connection returns to the pool.
type AdvisoryLock struct {
	conn *sql.Conn
	key  int64
}

// AcquireAdvisoryLock acquires a connection from the shared db pool
// and obtains a session-level advisory lock on the same connection.
// Call Unlock to release the lock and return the connection.
func AcquireAdvisoryLock(ctx context.Context, db *sql.DB, key int64) (*AdvisoryLock, error) {
	if db == nil {
		return nil, fmt.Errorf("advisory_lock: nil database")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("advisory_lock: acquire connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("advisory_lock: pg_advisory_lock(%d): %w", key, err)
	}
	return &AdvisoryLock{conn: conn, key: key}, nil
}

// TryAcquireAdvisoryLock attempts to acquire a session-level advisory lock
// without blocking. Returns the lock and true on success, or nil and false
// if the lock is already held by another session.
func TryAcquireAdvisoryLock(ctx context.Context, db *sql.DB, key int64) (*AdvisoryLock, bool, error) {
	if db == nil {
		return nil, false, fmt.Errorf("advisory_lock: nil database")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("advisory_lock: acquire connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("advisory_lock: pg_try_advisory_lock(%d): %w", key, err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return &AdvisoryLock{conn: conn, key: key}, true, nil
}

// Unlock releases the advisory lock and returns the connection to the pool.
// It is safe to call multiple times; subsequent calls are no-ops.
func (l *AdvisoryLock) Unlock() error {
	if l == nil || l.conn == nil {
		return nil
	}
	_, err := l.conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", l.key)
	closeErr := l.conn.Close()
	l.conn = nil
	k := l.key
	l.key = 0
	if err != nil {
		return fmt.Errorf("advisory_lock: pg_advisory_unlock(%d): %w", k, err)
	}
	if closeErr != nil {
		return fmt.Errorf("advisory_lock: close connection: %w", closeErr)
	}
	return nil
}

// Key returns the advisory lock key.
func (l *AdvisoryLock) Key() int64 {
	if l == nil {
		return 0
	}
	return l.key
}
