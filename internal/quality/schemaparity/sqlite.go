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
			table.SetNullable(column.Name, column.Nullable)
			table.SetHasDefault(column.Name, column.HasDefault)
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

// sqliteColumns reads the column list of one table. It asks pragma_table_info
// rather than parsing the DDL text: the pragma knows what the executed
// migrations produced, including the incremental ALTERs, and it reports the
// effective NOT NULL / DEFAULT / PRIMARY KEY state rather than the declaration.
//
// A PRIMARY KEY column counts as non-nullable on both engines even though a
// SQLite rowid table tolerates NULL there: PostgreSQL makes its primary key
// NOT NULL implicitly, so treating only the explicit constraint as non-nullable
// would report every key column as drift.
func sqliteColumns(db *sql.DB, table string) ([]Column, error) {
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("read sqlite columns for %s: %w", table, err)
	}
	defer rows.Close()

	var columns []Column
	for rows.Next() {
		var name, raw string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&name, &raw, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan sqlite column for %s: %w", table, err)
		}
		columns = append(columns, Column{
			Name:       name,
			Raw:        raw,
			Storage:    SQLiteStorageClass(raw),
			Nullable:   notNull == 0 && primaryKey == 0,
			HasDefault: defaultValue.Valid,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite columns for %s: %w", table, err)
	}
	return columns, nil
}
