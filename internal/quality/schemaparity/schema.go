// Package schemaparity compares the two schema sources this project keeps in
// parallel: the inline DDL that SQLite executes (internal/store/sqlite/db.go)
// and the PostgreSQL migrations under migrations/*.up.sql.
//
// AGENTS.md hard constraint 4 requires every new table or column to work on both
// engines, but the only link between the two sources is a comment. Drift is
// therefore invisible locally: it only shows up in the integration-tagged
// PostgreSQL job or in production. This package makes the comparison mechanical.
//
// The two engines do not describe types the same way -- SQLite has five storage
// classes while PostgreSQL has dozens of types -- so the comparison normalizes
// each side to the storage class the SQLite mirror must use, rather than
// demanding textual equality. A PostgreSQL TIMESTAMPTZ and a SQLite TEXT are
// therefore consistent; a PostgreSQL BIGINT and a SQLite TEXT are not.
//
// The table sets are legitimately different (SQLite and PostgreSQL do not carry
// the same feature set), so a table present on only one side is reported, not
// failed. Only a same-named table whose column set or column type differs is a
// drift finding.
package schemaparity

import (
	"fmt"
	"sort"
	"strings"
)

// Storage classes a SQLite column can use. These are the values the comparison
// is normalized to.
const (
	StorageText    = "text"
	StorageInteger = "integer"
	StorageNumeric = "numeric"
	StorageBlob    = "blob"
)

// Column is one table column.
type Column struct {
	Name string
	// Raw is the declared type as written in the source.
	Raw string
	// Storage is the normalized storage class (SQLite side) or the primary
	// storage class the SQLite mirror must use (PostgreSQL side).
	Storage string
	// Accept lists the storage classes that are also correct for this column.
	// It is used only where SQLite has no faithful equivalent: a PostgreSQL
	// JSONB column is stored as either TEXT or BLOB in this project, so both are
	// accepted. An empty Accept means Storage is the only correct class.
	Accept []string
}

// Table is a named set of columns.
type Table struct {
	Name    string
	Columns map[string]Column
}

// Schema is a whole snapshot.
type Schema struct {
	Tables map[string]*Table
}

// NewSchema returns an empty schema.
func NewSchema() *Schema {
	return &Schema{Tables: make(map[string]*Table)}
}

// AddTable inserts a table, replacing any previous definition of the same name.
func (s *Schema) AddTable(name string) *Table {
	t := &Table{Name: name, Columns: make(map[string]Column)}
	s.Tables[name] = t
	return t
}

// Table returns the named table for mutation.
func (s *Schema) Table(name string) (*Table, bool) {
	t, ok := s.Tables[name]
	return t, ok
}

// SetColumn adds or replaces a column on a table.
func (t *Table) SetColumn(name, raw, storage string) {
	t.SetColumnAccept(name, raw, storage, nil)
}

// SetColumnAccept adds or replaces a column with an explicit accepted-class set.
func (t *Table) SetColumnAccept(name, raw, storage string, accept []string) {
	if t.Columns == nil {
		t.Columns = make(map[string]Column)
	}
	t.Columns[name] = Column{Name: name, Raw: raw, Storage: storage, Accept: accept}
}

// DropColumn removes a column.
func (t *Table) DropColumn(name string) {
	delete(t.Columns, name)
}

// RenameColumn renames a column in place.
func (t *Table) RenameColumn(from, to string) {
	if c, ok := t.Columns[from]; ok {
		delete(t.Columns, from)
		c.Name = to
		t.Columns[to] = c
	}
}

// RenameTable renames a table in place.
func (s *Schema) RenameTable(from, to string) {
	t, ok := s.Tables[from]
	if !ok {
		return
	}
	delete(s.Tables, from)
	t.Name = to
	s.Tables[to] = t
}

// DropTable removes a table.
func (s *Schema) DropTable(name string) {
	delete(s.Tables, name)
}

// TableNames returns the table names in sorted order.
func (s *Schema) TableNames() []string {
	names := make([]string, 0, len(s.Tables))
	for name := range s.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ColumnNames returns the column names of one table in sorted order.
func (t Table) ColumnNames() []string {
	names := make([]string, 0, len(t.Columns))
	for name := range t.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Drift kinds.
const (
	// KindColumnMissingInPG: the SQLite table has a column PostgreSQL lacks.
	KindColumnMissingInPG = "column_missing_in_postgres"
	// KindColumnMissingInSQLite: the PostgreSQL table has a column SQLite lacks.
	KindColumnMissingInSQLite = "column_missing_in_sqlite"
	// KindColumnTypeMismatch: both sides have the column but its storage class differs.
	KindColumnTypeMismatch = "column_type_mismatch"
)

// Finding is one drift in a table present on both sides.
type Finding struct {
	Kind        string
	Table       string
	Column      string
	SQLiteRaw   string
	SQLiteStore string
	PGRaw       string
	PGStore     string
}

func (f Finding) String() string {
	switch f.Kind {
	case KindColumnMissingInPG:
		return fmt.Sprintf("%s.%s: [%s] declared by SQLite (%s) but absent from the PostgreSQL migrations",
			f.Table, f.Column, f.Kind, f.SQLiteRaw)
	case KindColumnMissingInSQLite:
		return fmt.Sprintf("%s.%s: [%s] declared by the PostgreSQL migrations (%s) but absent from SQLite",
			f.Table, f.Column, f.Kind, f.PGRaw)
	default:
		return fmt.Sprintf("%s.%s: [%s] SQLite %s (storage %s) vs PostgreSQL %s (storage %s)",
			f.Table, f.Column, f.Kind, f.SQLiteRaw, f.SQLiteStore, f.PGRaw, f.PGStore)
	}
}

// Report is the outcome of comparing two snapshots.
type Report struct {
	SQLiteTables int
	PGTables     int
	CommonTables []string
	SQLiteOnly   []string
	PGOnly       []string
	Drift        []Finding
}

// Failed reports whether the comparison found a real drift. A table present on
// only one side is reported but is not a failure.
func (r *Report) Failed() bool { return len(r.Drift) > 0 }

// Diff compares the SQLite snapshot against the PostgreSQL snapshot.
func Diff(sqliteSchema, pgSchema *Schema) *Report {
	report := &Report{
		SQLiteTables: len(sqliteSchema.Tables),
		PGTables:     len(pgSchema.Tables),
	}
	for _, name := range sqliteSchema.TableNames() {
		pgTable, ok := pgSchema.Table(name)
		if !ok {
			report.SQLiteOnly = append(report.SQLiteOnly, name)
			continue
		}
		report.CommonTables = append(report.CommonTables, name)
		sqliteTable := sqliteSchema.Tables[name]
		for _, column := range sqliteTable.ColumnNames() {
			sc := sqliteTable.Columns[column]
			pc, ok := pgTable.Columns[column]
			if !ok {
				report.Drift = append(report.Drift, Finding{
					Kind: KindColumnMissingInPG, Table: name, Column: column,
					SQLiteRaw: sc.Raw, SQLiteStore: sc.Storage,
				})
				continue
			}
			if !pgAccepts(pc, sc.Storage) {
				report.Drift = append(report.Drift, Finding{
					Kind: KindColumnTypeMismatch, Table: name, Column: column,
					SQLiteRaw: sc.Raw, SQLiteStore: sc.Storage,
					PGRaw: pc.Raw, PGStore: pc.Storage,
				})
			}
		}
		for _, column := range pgTable.ColumnNames() {
			if _, ok := sqliteTable.Columns[column]; !ok {
				report.Drift = append(report.Drift, Finding{
					Kind: KindColumnMissingInSQLite, Table: name, Column: column,
					PGRaw: pgTable.Columns[column].Raw, PGStore: pgTable.Columns[column].Storage,
				})
			}
		}
	}
	sort.Strings(report.SQLiteOnly)
	sort.Strings(report.PGOnly)
	for _, name := range pgSchema.TableNames() {
		if _, ok := sqliteSchema.Tables[name]; !ok {
			report.PGOnly = append(report.PGOnly, name)
		}
	}
	sort.SliceStable(report.Drift, func(i, j int) bool {
		if report.Drift[i].Table != report.Drift[j].Table {
			return report.Drift[i].Table < report.Drift[j].Table
		}
		if report.Drift[i].Column != report.Drift[j].Column {
			return report.Drift[i].Column < report.Drift[j].Column
		}
		return report.Drift[i].Kind < report.Drift[j].Kind
	})
	return report
}

// normalizeType uppercases a declared type, removes any parenthesized size or
// precision, and collapses whitespace so "TIMESTAMP(6) WITH TIME ZONE" and
// "timestamp with time zone" compare equal.
func normalizeType(raw string) string {
	t := strings.ToUpper(strings.TrimSpace(raw))
	t = stripParens(t)
	return strings.Join(strings.Fields(t), " ")
}

// stripParens removes balanced "(...)" groups, keeping the text around them.
func stripParens(t string) string {
	var b strings.Builder
	depth := 0
	for _, r := range t {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// SQLiteStorageClass maps a declared SQLite type to its storage class using
// SQLite's type-affinity rules (https://sqlite.org/datatype3.html §3.1).
func SQLiteStorageClass(raw string) string {
	t := normalizeType(raw)
	switch {
	case t == "":
		return StorageBlob // no declared type -> BLOB affinity
	case strings.Contains(t, "INT"):
		return StorageInteger
	case strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"):
		return StorageText
	case strings.Contains(t, "BLOB"):
		return StorageBlob
	// SQLite's affinity rule also matches the four-letter prefix of "DOUBLE",
	// which is the only real type that contains it.
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUBLE"):
		return StorageNumeric
	default:
		return StorageNumeric // NUMERIC affinity
	}
}

// PGStorageClass maps a declared PostgreSQL type to the storage class the SQLite
// mirror of that column must use. The mapping is the project's convention, not
// PostgreSQL's own storage: timestamps and JSON are TEXT in SQLite, booleans are
// INTEGER, and so on.
func PGStorageClass(raw string) string {
	return PGStorageClasses(raw)[0]
}

// PGStorageClasses returns every SQLite storage class that is correct for a
// PostgreSQL type. Almost always there is exactly one; JSON is the exception,
// because SQLite has no JSON type and this project stores JSON columns as either
// TEXT or BLOB.
func PGStorageClasses(raw string) []string {
	t := normalizeType(raw)
	switch {
	case t == "":
		return []string{StorageBlob}
	case strings.Contains(t, "TIMESTAMP"), t == "DATE", t == "TIME", strings.Contains(t, "INTERVAL"):
		return []string{StorageText}
	case strings.Contains(t, "INT"), t == "SERIAL", t == "BIGSERIAL", strings.Contains(t, "SERIAL"):
		return []string{StorageInteger}
	case t == "BOOLEAN", t == "BOOL":
		return []string{StorageInteger}
	case strings.Contains(t, "CHAR"), strings.Contains(t, "TEXT"), t == "UUID", strings.Contains(t, "NAME"):
		return []string{StorageText}
	case strings.Contains(t, "JSON"):
		return []string{StorageText, StorageBlob}
	case strings.Contains(t, "BYTEA"), strings.Contains(t, "BLOB"):
		return []string{StorageBlob}
	case strings.Contains(t, "NUMERIC"), strings.Contains(t, "DECIMAL"),
		strings.Contains(t, "REAL"), strings.Contains(t, "DOUBLE"), strings.Contains(t, "FLOAT"),
		strings.Contains(t, "MONEY"):
		return []string{StorageNumeric}
	default:
		return []string{StorageText}
	}
}

// pgAccepts reports whether a SQLite storage class is compatible with a
// PostgreSQL column.
func pgAccepts(pc Column, storage string) bool {
	if len(pc.Accept) == 0 {
		return pc.Storage == storage
	}
	for _, accept := range pc.Accept {
		if accept == storage {
			return true
		}
	}
	return false
}
