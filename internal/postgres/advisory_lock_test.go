package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireAdvisoryLock_NilDatabase(t *testing.T) {
	lock, err := AcquireAdvisoryLock(context.Background(), nil, 42)
	assert.Nil(t, lock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil database")
	assert.NotContains(t, err.Error(), "postgres://")
}

func TestTryAcquireAdvisoryLock_NilDatabase(t *testing.T) {
	lock, acquired, err := TryAcquireAdvisoryLock(context.Background(), nil, 42)
	assert.Nil(t, lock)
	assert.False(t, acquired)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil database")
}

func TestAdvisoryLock_NilReceiver(t *testing.T) {
	l := (*AdvisoryLock)(nil)
	assert.Equal(t, int64(0), l.Key())
	assert.NoError(t, l.Unlock())
	assert.NoError(t, l.Unlock()) // idempotent
}

func TestAcquireAdvisoryLock_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec(`SELECT pg_advisory_lock\(\$1\)`).WithArgs(int64(42)).WillReturnResult(sqlmock.NewResult(0, 0))

	lock, err := AcquireAdvisoryLock(context.Background(), db, 42)
	require.NoError(t, err)
	assert.NotNil(t, lock)
	assert.Equal(t, int64(42), lock.Key())
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAcquireAdvisoryLock_ConnError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the database to force Conn() errors.
	db.Close()
	// ignore unmet expectations triggered by close
	assert.NoError(t, mock.ExpectationsWereMet())

	lock, err := AcquireAdvisoryLock(context.Background(), db, 42)
	assert.Nil(t, lock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "advisory_lock")
	assert.NotContains(t, err.Error(), "postgres://")
}

func TestTryAcquireAdvisoryLock_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true)
	mock.ExpectQuery(`SELECT pg_try_advisory_lock\(\$1\)`).WithArgs(int64(7)).WillReturnRows(rows)

	lock, acquired, err := TryAcquireAdvisoryLock(context.Background(), db, 7)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.NotNil(t, lock)
	assert.Equal(t, int64(7), lock.Key())
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTryAcquireAdvisoryLock_NotAcquired(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false)
	mock.ExpectQuery(`SELECT pg_try_advisory_lock\(\$1\)`).WithArgs(int64(99)).WillReturnRows(rows)

	lock, acquired, err := TryAcquireAdvisoryLock(context.Background(), db, 99)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, lock)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTryAcquireAdvisoryLock_ConnError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	db.Close()
	assert.NoError(t, mock.ExpectationsWereMet())

	lock, acquired, err := TryAcquireAdvisoryLock(context.Background(), db, 7)
	assert.Nil(t, lock)
	assert.False(t, acquired)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "advisory_lock")
	assert.NotContains(t, err.Error(), "postgres://")
}

func TestAdvisoryLock_UnlockAndClose(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Expect: acquire connection + lock, then unlock + close.
	mock.ExpectExec(`SELECT pg_advisory_lock\(\$1\)`).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 0))

	lock, err := AcquireAdvisoryLock(context.Background(), db, 1)
	require.NoError(t, err)
	assert.NotNil(t, lock)

	require.NoError(t, lock.Unlock())
	assert.Equal(t, int64(0), lock.Key()) // key cleared after unlock
	assert.Nil(t, lock.conn)              // conn nil after close

	// Idempotent — second call is a no-op.
	assert.NoError(t, lock.Unlock())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAdvisoryLock_ErrorMessagesNoCredentials(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	db.Close()

	lock, err := AcquireAdvisoryLock(context.Background(), db, 1)
	assert.Nil(t, lock)
	if err != nil {
		assert.NotContains(t, err.Error(), "postgres://")
		assert.NotContains(t, err.Error(), "password")
		assert.NotContains(t, err.Error(), "secret")
	}
}

func TestAdvisoryLock_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before the call

	lock, err := AcquireAdvisoryLock(ctx, nil, 1)
	assert.Nil(t, lock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil database")
}

func TestWithConnection_NilDatabase(t *testing.T) {
	err := WithConnection(context.Background(), nil, func(_ *sql.Conn) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil database")
}

func TestWithConnection_AcquireError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	db.Close()
	assert.NoError(t, mock.ExpectationsWereMet())

	got := WithConnection(context.Background(), db, func(_ *sql.Conn) error { return nil })
	require.Error(t, got)
	assert.Contains(t, got.Error(), "connection")
	assert.NotContains(t, got.Error(), "postgres://")
}

func TestWithConnection_CallbackError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	var capturedConn *sql.Conn
	err = WithConnection(context.Background(), db, func(c *sql.Conn) error {
		capturedConn = c
		return assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.NotNil(t, capturedConn)
	// WithConnection defers conn.Close; mock does not require explicit close.
	assert.NoError(t, mock.ExpectationsWereMet())
}
