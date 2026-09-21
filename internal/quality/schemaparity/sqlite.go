package schemaparity

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/ndzuki/release-manager/internal/store/sqlite"
)

var sqliteSnapshotSequence atomic.Uint64

// SnapshotSQLite reads the SQLite schema by executing the authoritative
// migration path against a private in-memory database.
//
// Why executing the DDL is safe: the database is process-private
// (mode=memory&cache=shared with a unique name), so nothing is written to disk
// and no existing database can be reached; the tool only reads schema metadata
// back. Executing is also more faithful than parsing internal/store/sqlite/db.go
// textually -- it sees the incremental ALTER statements and the repair branches
// that a CREATE TABLE grep would miss.
func SnapshotSQLite() (*Schema, error) {
	dsn := fmt.Sprintf("file:schemaparity-%d-%d?mode=memory&cache=shared",
		os.Getpid(), sqliteSnapshotSequence.Add(1))
	st, err := sqlite.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("open in-memory sqlite: %w", err)
	}
	defer st.Close()

	return readSQLiteSchema(st.DB())
}

func readSQLiteSchema(db *sql.DB) (*Schema, error) {
	names, err := sqliteTableNames(db)
	if err != nil {
		return nil, err
	}
	schema := NewSchema()
	for _, name := range names {
		columns, err := sqliteColumns(db, name)
		if err != nil {
			return nil, err
		}
		table := schema.AddTable(name)
		for _, column := range columns {
			table.SetColumn(column.Name, column.Raw, column.Storage)
		}
	}
	return schema, nil
}

func sqliteTableNames(db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan sqlite table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite tables: %w", err)
	}
	return names, nil
}

func sqliteColumns(db *sql.DB, table string) ([]Column, error) {
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("read sqlite columns for %s: %w", table, err)
	}
	defer rows.Close()

	var columns []Column
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, fmt.Errorf("scan sqlite column for %s: %w", table, err)
		}
		columns = append(columns, Column{Name: name, Raw: raw, Storage: SQLiteStorageClass(raw)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite columns for %s: %w", table, err)
	}
	return columns, nil
}
