package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// gormDB wraps a *sql.DB with GORM using the PostgreSQL driver.
func gormDB(t *testing.T, sdb *sql.DB) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sdb}), &gorm.Config{
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)
	return gdb
}

func TestOperationCreationUnitOfWork_NilDatabase(t *testing.T) {
	err := OperationCreationUnitOfWork(context.Background(), nil, func(_ *gorm.DB, _ *sql.Tx) error {
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil database")
	assert.NotContains(t, err.Error(), "postgres://")
}

func TestOperationCreationUnitOfWork_Success(t *testing.T) {
	sdb, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sdb.Close()

	db := gormDB(t, sdb)

	mock.ExpectBegin()
	mock.ExpectCommit()

	var capturedTx *gorm.DB
	var capturedSQLTx *sql.Tx

	err = OperationCreationUnitOfWork(context.Background(), db, func(tx *gorm.DB, sqlTx *sql.Tx) error {
		capturedTx = tx
		capturedSQLTx = sqlTx
		return nil
	})
	require.NoError(t, err)
	assert.NotNil(t, capturedTx)
	assert.NotNil(t, capturedSQLTx)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationCreationUnitOfWork_RollbackOnError(t *testing.T) {
	sdb, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sdb.Close()

	db := gormDB(t, sdb)

	mock.ExpectBegin()
	mock.ExpectRollback()

	sentinel := assert.AnError

	got := OperationCreationUnitOfWork(context.Background(), db, func(_ *gorm.DB, _ *sql.Tx) error {
		return sentinel
	})
	assert.ErrorIs(t, got, sentinel)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationCreationUnitOfWork_ErrorWithoutDSN(t *testing.T) {
	sdb, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sdb.Close()

	db := gormDB(t, sdb)

	mock.ExpectBegin()
	mock.ExpectRollback()

	const secretKey = "postgres://user:secret@localhost/db"
	got := OperationCreationUnitOfWork(context.Background(), db, func(_ *gorm.DB, _ *sql.Tx) error {
		return assert.AnError
	})
	assert.ErrorIs(t, got, assert.AnError)
	assert.NotContains(t, got.Error(), secretKey)
	assert.NotContains(t, got.Error(), "postgres://")
	assert.NoError(t, mock.ExpectationsWereMet())
}
