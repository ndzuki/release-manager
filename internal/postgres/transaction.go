package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"
)

// OperationCreationUnitOfWork executes fn with a GORM transaction. Raw SQL that must be
// atomic with GORM writes must use tx.Statement.ConnPool as a *sql.Tx.
func OperationCreationUnitOfWork(ctx context.Context, db *gorm.DB, fn func(*gorm.DB, *sql.Tx) error) error {
	if db == nil {
		return fmt.Errorf("transaction: nil database")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sqlTx, ok := tx.Statement.ConnPool.(*sql.Tx)
		if !ok {
			return fmt.Errorf("transaction: GORM connection is not *sql.Tx")
		}
		return fn(tx, sqlTx)
	})
}

// WithConnection acquires one connection from the shared pool for session
// scoped PostgreSQL operations such as advisory locks.
func WithConnection(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	if db == nil {
		return fmt.Errorf("connection: nil database")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connection: acquire: %w", err)
	}
	defer conn.Close()
	return fn(conn)
}
