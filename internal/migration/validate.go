package migration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// validateMigration verifies the data migration correctness:
// 1. Row counts match between source SQLite and target PostgreSQL.
// 2. No foreign key violations in PostgreSQL.
// 3. All time columns are TIMESTAMPTZ in PostgreSQL.
// On failure, returns an error starting with "data_import_mismatch".
func validateMigration(
	ctx context.Context,
	srcDB *sql.DB,
	tx *sql.Tx,
	tables []string,
	rowCounts map[string]int64,
) error {
	// 1. Per-table row count validation.
	for _, tbl := range tables {
		srcCount, err := countRows(ctx, srcDB, tbl)
		if err != nil {
			return fmt.Errorf("data_import_mismatch: count source %s: %w", tbl, err)
		}
		tgtCount, err := countRows(ctx, tx, tbl)
		if err != nil {
			return fmt.Errorf("data_import_mismatch: count target %s: %w", tbl, err)
		}
		if srcCount != tgtCount {
			return fmt.Errorf("data_import_mismatch: table %s row count mismatch: source=%d, target=%d", tbl, srcCount, tgtCount)
		}
		if rowCounts[tbl] != tgtCount {
			return fmt.Errorf("data_import_mismatch: table %s copied %d rows but target has %d", tbl, rowCounts[tbl], tgtCount)
		}
	}

	// 2. Foreign key violation check.
	if err := checkFKViolations(ctx, tx); err != nil {
		return err
	}

	// 3. Validate every PostgreSQL table, including target-only tables created
	// by later migrations, so schema type drift cannot hide behind empty data.
	if err := checkTimeColumnTypes(ctx, tx); err != nil {
		return err
	}

	return nil
}

func countRows(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table string) (int64, error) {
	var count int64
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdentifier(table))).Scan(&count)
	return count, err
}

// checkFKViolations queries PostgreSQL catalog to detect any rows that violate
// foreign key constraints.
func checkFKViolations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			tc.table_name AS referencing_table,
			kcu.column_name AS referencing_column,
			ccu.table_name AS referenced_table,
			ccu.column_name AS referenced_column
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON tc.constraint_name = ccu.constraint_name
			AND tc.table_schema = ccu.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
			AND tc.table_schema = current_schema()
		ORDER BY tc.table_name, kcu.column_name
	`)
	if err != nil {
		return fmt.Errorf("data_import_mismatch: query FK constraints: %w", err)
	}
	defer rows.Close()
	type foreignKey struct {
		refTable, refCol, parentTable, parentCol string
	}
	var constraints []foreignKey
	for rows.Next() {
		var constraint foreignKey
		if err := rows.Scan(&constraint.refTable, &constraint.refCol, &constraint.parentTable, &constraint.parentCol); err != nil {
			return fmt.Errorf("data_import_mismatch: scan FK constraint: %w", err)
		}
		constraints = append(constraints, constraint)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("data_import_mismatch: iterate FK constraints: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("data_import_mismatch: close FK constraints: %w", err)
	}

	var violations []string
	for _, constraint := range constraints {
		checkSQL := fmt.Sprintf( //nolint:gosec // all identifiers originate from PostgreSQL catalogs and are quoted.
			`SELECT COUNT(*) FROM %s r
			 LEFT JOIN %s p ON r.%s = p.%s
			 WHERE r.%s IS NOT NULL AND p.%s IS NULL`,
			quoteIdentifier(constraint.refTable), quoteIdentifier(constraint.parentTable),
			quoteIdentifier(constraint.refCol), quoteIdentifier(constraint.parentCol),
			quoteIdentifier(constraint.refCol), quoteIdentifier(constraint.parentCol),
		)
		var orphanCount int64
		if err := tx.QueryRowContext(ctx, checkSQL).Scan(&orphanCount); err != nil {
			return fmt.Errorf("data_import_mismatch: check FK %s.%s -> %s.%s: %w",
				constraint.refTable, constraint.refCol, constraint.parentTable, constraint.parentCol, err)
		}
		if orphanCount > 0 {
			violations = append(violations, fmt.Sprintf("%s.%s -> %s.%s (%d orphans)",
				constraint.refTable, constraint.refCol, constraint.parentTable, constraint.parentCol, orphanCount))
		}
	}
	if len(violations) > 0 {
		return fmt.Errorf("data_import_mismatch: foreign key violations: %s", strings.Join(violations, "; "))
	}
	return nil
}

// checkTimeColumnTypes verifies all application time columns in PostgreSQL use TIMESTAMPTZ.
func checkTimeColumnTypes(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		ORDER BY table_name, ordinal_position
	`)
	if err != nil {
		return fmt.Errorf("data_import_mismatch: query time column types: %w", err)
	}
	defer rows.Close()

	var violations []string
	for rows.Next() {
		var tableName, columnName, dataType string
		if err := rows.Scan(&tableName, &columnName, &dataType); err != nil {
			return fmt.Errorf("data_import_mismatch: scan time column type: %w", err)
		}
		if isTimeColumn(columnName) && dataType != "timestamp with time zone" {
			violations = append(violations, fmt.Sprintf("%s.%s is %s, expected timestamp with time zone", tableName, columnName, dataType))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("data_import_mismatch: iterate time column types: %w", err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("data_import_mismatch: time column type violations: %s", strings.Join(violations, "; "))
	}
	return nil
}
