package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// discoverTables returns all user table names from the SQLite source,
// excluding sqlite_* system tables.
func discoverTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// fkTopology defines the FK dependency graph for topological ordering.
// key: table, value: tables it depends on (must be copied first).
// Tables not listed here are treated as leaf nodes (no dependencies).
var fkTopology = map[string][]string{
	// Tier 1: tables with FKs to leaf tables
	"clusters":                  {"customers"},
	"release_definition_events": {"release_definitions"},
	"values_revisions":          {"release_definitions"},
	"values_revision_decisions": {"values_revisions"},
	"operations":                {"release_definitions"},
	"sessions":                  {"operators"},
	"auth_sessions":             {"users"},
	"organization_members":      {"organizations", "users"},
	"org_customer_bindings":     {"organizations"},
	"customer_events":           {"customers"},
	"preflight_lifecycles":      {"operations"},
	"emergency_intents":         {"operations", "release_definitions"},
	"convergence_tasks":         {"operations", "release_definitions"},
	// Tier 2: tables with FKs to tier-1 tables
	"operation_events":                     {"operations"},
	"operation_timeline":                   {"operations"},
	"organization_customer_binding_events": {"org_customer_bindings", "organizations"},
	"cluster_routes":                       {"clusters"},
	// Tier 3: cross-cutting (FKs in PostgreSQL schema from migrations)
	"bundle_candidate_artifacts": {"release_bundles", "candidate_artifacts"},
}

// orderTables returns tables sorted so that referenced tables precede referencing tables.
// Tables unknown to fkTopology are treated as leaf nodes (no FKs) and placed first.
func orderTables(tables []string) []string {
	// Build the dependency graph only from tables present in the source.
	// A min-heap keeps the ordering deterministic and prefers leaf tables by name.
	present := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		present[table] = struct{}{}
	}
	dependencies := make(map[string]map[string]struct{}, len(tables))
	dependents := make(map[string][]string, len(tables))
	for _, table := range tables {
		dependencies[table] = make(map[string]struct{})
		for _, dependency := range fkTopology[table] {
			if _, ok := present[dependency]; !ok {
				continue
			}
			dependencies[table][dependency] = struct{}{}
			dependents[dependency] = append(dependents[dependency], table)
		}
	}

	ready := make([]string, 0, len(tables))
	for _, table := range tables {
		if len(dependencies[table]) == 0 {
			ready = append(ready, table)
		}
	}
	sort.Strings(ready)

	ordered := make([]string, 0, len(tables))
	for len(ready) > 0 {
		table := ready[0]
		ready = ready[1:]
		ordered = append(ordered, table)
		for _, dependent := range dependents[table] {
			delete(dependencies[dependent], table)
			if len(dependencies[dependent]) == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(tables) {
		remaining := make([]string, 0, len(tables)-len(ordered))
		seen := make(map[string]struct{}, len(ordered))
		for _, table := range ordered {
			seen[table] = struct{}{}
		}
		for _, table := range tables {
			if _, ok := seen[table]; !ok {
				remaining = append(remaining, table)
			}
		}
		sort.Strings(remaining)
		ordered = append(ordered, remaining...)
	}
	return ordered
}

// readTableColumns returns the column names and conversion metadata for the
// given SQLite table.
func readTableColumns(ctx context.Context, db *sql.DB, table string) (cols []string, timeCols, blobCols map[int]bool, err error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(table)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read SQLite table info for %s: %w", table, err)
	}
	defer rows.Close()

	type colInfo struct {
		cid       int
		name      string
		colType   string
		notNull   int
		dfltValue *string
		pkOrder   int
	}
	var infos []colInfo
	for rows.Next() {
		var ci colInfo
		if err := rows.Scan(&ci.cid, &ci.name, &ci.colType, &ci.notNull, &ci.dfltValue, &ci.pkOrder); err != nil {
			return nil, nil, nil, fmt.Errorf("scan SQLite table info for %s: %w", table, err)
		}
		infos = append(infos, ci)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterate SQLite table info for %s: %w", table, err)
	}

	cols = make([]string, len(infos))
	timeCols = make(map[int]bool)
	blobCols = make(map[int]bool)
	for i, ci := range infos {
		cols[i] = ci.name
		if isTimeColumn(ci.name) {
			timeCols[i] = true
		}
		if strings.Contains(strings.ToUpper(ci.colType), "BLOB") {
			blobCols[i] = true
		}
	}
	return cols, timeCols, blobCols, nil
}

type columnDefault struct {
	name  string
	value any
}

var targetColumnDefaults = map[string][]columnDefault{
	"candidate_artifacts": {
		{name: "last_seen_at", value: func(values map[string]any) any { return values["created_at"] }},
	},
	"preflight_lifecycles": {
		{name: "updated_at", value: func(values map[string]any) any { return values["created_at"] }},
	},
}

func appendTargetColumns(table string, cols []string) ([]string, []columnDefault) {
	defaults := targetColumnDefaults[table]
	if len(defaults) == 0 {
		return cols, nil
	}
	targetCols := append([]string(nil), cols...)
	for _, column := range defaults {
		targetCols = append(targetCols, column.name)
	}
	return targetCols, defaults
}

// isTimeColumn returns true if the column name indicates a timestamp.
func isTimeColumn(name string) bool {
	if strings.HasSuffix(name, "_at") {
		return true
	}
	switch name {
	case "deadline", "last_heartbeat":
		return true
	}
	return false
}

// copyTable reads all rows from the SQLite source table and inserts them
// into the PostgreSQL target using parameterized SQL within the transaction.
// Time columns (TEXT in SQLite) are parsed as UTC time.Time for TIMESTAMPTZ.
//
//nolint:gocyclo // Conversion precedence is explicit per SQLite storage class.
func copyTable(
	ctx context.Context,
	srcDB *sql.DB,
	tx *sql.Tx,
	table string,
	cols []string,
	timeCols, blobCols map[int]bool,
) (int64, error) {
	// Read all rows from source.
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(quoteIdentifiers(cols), ", "), quoteIdentifier(table)) //nolint:gosec // table and columns come from database metadata and are quoted.
	rows, err := srcDB.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("query source %s: %w", table, err)
	}
	defer rows.Close()

	// Build INSERT statement with placeholders, adding PostgreSQL-only columns
	// derived from legacy SQLite values.
	targetCols, defaults := appendTargetColumns(table, cols)
	placeholders := make([]string, len(targetCols))
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	insertSQL := fmt.Sprintf( //nolint:gosec // identifiers come from metadata and static mappings; values use placeholders.
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentifier(table),
		strings.Join(quoteIdentifiers(targetCols), ", "),
		strings.Join(placeholders, ", "),
	)

	var count int64
	for rows.Next() {
		values := make([]any, len(cols))
		destinations := make([]any, len(cols))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return count, fmt.Errorf("scan row from %s: %w", table, err)
		}

		args := make([]any, len(targetCols))
		converted := make(map[string]any, len(targetCols))
		for i, value := range values {
			convertedValue, err := convertSQLiteValue(table, cols[i], value, timeCols[i], blobCols[i])
			if err != nil {
				return count, err
			}
			args[i] = convertedValue
			converted[cols[i]] = convertedValue
		}
		for i, column := range defaults {
			value := column.value
			if derive, ok := value.(func(map[string]any) any); ok {
				value = derive(converted)
			}
			args[len(cols)+i] = value
		}

		if _, err := tx.ExecContext(ctx, insertSQL, args...); err != nil {
			return count, fmt.Errorf("insert into %s: %w", table, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("iterate %s: %w", table, err)
	}
	return count, nil
}

func convertSQLiteValue(table, column string, value any, timeColumn, blobColumn bool) (any, error) {
	if value == nil {
		return nil, nil
	}
	if timeColumn {
		converted, err := parseTimeUTC(sqliteText(value))
		if err != nil {
			return nil, fmt.Errorf("parse time column %s.%s: %w", table, column, err)
		}
		return converted, nil
	}
	if isBooleanColumn(table, column) {
		converted, err := sqliteBoolean(value)
		if err != nil {
			return nil, fmt.Errorf("parse boolean column %s.%s: %w", table, column, err)
		}
		return converted, nil
	}
	if isJSONColumn(table, column) {
		text := sqliteText(value)
		if !json.Valid([]byte(text)) {
			return nil, fmt.Errorf("invalid JSON column %s.%s", table, column)
		}
		return text, nil
	}
	if blobColumn {
		switch typed := value.(type) {
		case []byte:
			return typed, nil
		case string:
			return []byte(typed), nil
		default:
			return nil, fmt.Errorf("unsupported BLOB value %T for %s.%s", value, table, column)
		}
	}
	return value, nil
}

var jsonColumns = map[string]map[string]struct{}{
	"audit_outbox":         {"payload_json": {}},
	"notification_outbox":  {"payload_json": {}},
	"operations":           {"actor": {}},
	"operation_timeline":   {"data": {}},
	"sessions":             {"capabilities": {}},
	"audit_events":         {"metadata": {}},
	"notification_jobs":    {"metadata": {}},
	"release_bundles":      {"images": {}},
	"preflight_lifecycles": {"stages": {}},
	"scan_results":         {"severity_json": {}, "findings_json": {}},
	"emergency_intents": {
		"annotation_entries": {}, "promotion_paths": {}, "before_snapshot": {}, "after_snapshot": {},
	},
}

func isJSONColumn(table, column string) bool {
	_, ok := jsonColumns[table][column]
	return ok
}

var booleanColumns = map[string]map[string]struct{}{
	"audit_outbox":        {"delivered": {}},
	"auth_sessions":       {"revoked": {}},
	"enrollment_tokens":   {"used": {}},
	"inventory_sync_log":  {"is_full_snapshot": {}},
	"notification_outbox": {"delivered": {}},
}

func isBooleanColumn(table, column string) bool {
	_, ok := booleanColumns[table][column]
	return ok
}

func sqliteText(value any) string {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return fmt.Sprint(value)
}

func sqliteBoolean(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int64:
		return typed != 0, nil
	case []byte:
		return strconv.ParseBool(string(typed))
	case string:
		if typed == "0" {
			return false, nil
		}
		if typed == "1" {
			return true, nil
		}
		return strconv.ParseBool(typed)
	default:
		return false, fmt.Errorf("unsupported SQLite boolean value %T", value)
	}
}

// parseTimeUTC parses a SQLite time string as UTC.
// SQLite uses RFC3339-like formats. We try several common variants.
func parseTimeUTC(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999999",
	}
	for _, f := range formats {
		t, err := time.Parse(f, s)
		if err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", s)
}

// runBackfills executes data transformations that migrations applied before
// SQLite rows were copied into the PostgreSQL transaction.
func runBackfills(ctx context.Context, tx *sql.Tx) ([]string, error) {
	backfills := []struct {
		name  string
		query string
	}{
		{
			name: "terminal_at",
			query: `UPDATE operations
				SET terminal_at = updated_at
				WHERE status IN ('succeeded', 'failed', 'cancelled', 'timeout') AND terminal_at IS NULL`,
		},
		{
			name: "candidate_artifacts_join",
			query: `INSERT INTO bundle_candidate_artifacts (bundle_id, candidate_artifact_id)
				SELECT bundle_id, id FROM candidate_artifacts
				WHERE bundle_id IS NOT NULL AND bundle_id <> ''
				ON CONFLICT DO NOTHING`,
		},
		{
			name: "values_state_version",
			query: `UPDATE values_revisions
				SET state_version = CASE WHEN version > 0 THEN version ELSE 1 END
				WHERE state_version = 0 OR state_version = 1`,
		},
		{
			name: "values_created_by_user",
			query: `UPDATE values_revisions
				SET created_by_user_id = created_by
				WHERE created_by_user_id = ''`,
		},
		{
			name: "values_superseded",
			query: `UPDATE values_revisions AS current
				SET status = 'superseded',
					state_version = state_version + 1,
					version = version + 1,
					updated_at = CURRENT_TIMESTAMP
				WHERE status = 'approved'
				  AND EXISTS (
					SELECT 1 FROM values_revisions AS newer
					WHERE newer.release_definition_id = current.release_definition_id
					  AND newer.status = 'approved'
					  AND (newer.revision > current.revision
						OR (newer.revision = current.revision AND newer.id > current.id))
				  )`,
		},
	}

	backfilled := make([]string, 0, len(backfills))
	for _, backfill := range backfills {
		if _, err := tx.ExecContext(ctx, backfill.query); err != nil {
			return backfilled, fmt.Errorf("backfill %s: %w", backfill.name, err)
		}
		backfilled = append(backfilled, backfill.name)
	}
	return backfilled, nil
}

// quoteIdentifier double-quotes a SQL identifier for safe use in queries.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteIdentifiers applies quoteIdentifier to each name.
func quoteIdentifiers(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdentifier(n)
	}
	return out
}
