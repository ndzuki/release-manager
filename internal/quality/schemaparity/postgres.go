package schemaparity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SnapshotPostgres builds the PostgreSQL schema snapshot by parsing the
// migrations under dir, in filename order. It does not connect to a database:
// the gate must run in the default (non-integration) test path and in CI without
// a PostgreSQL service.
//
// The parser is deliberately small and covers only what the migrations use:
// CREATE TABLE, ALTER TABLE ADD/DROP/RENAME COLUMN, RENAME TO, and DROP TABLE.
// Anything else (indexes, constraints, data statements) is ignored, and an
// unrecognized statement is skipped rather than guessed at.
func SnapshotPostgres(dir string) (*Schema, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return nil, fmt.Errorf("glob migrations: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.up.sql migrations found under %s", dir)
	}
	sort.Strings(files)

	schema := NewSchema()
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		for _, stmt := range SplitStatements(StripComments(string(data))) {
			applyStatement(schema, stmt)
		}
	}
	return schema, nil
}

func applyStatement(schema *Schema, stmt string) {
	upper := strings.ToUpper(strings.TrimSpace(stmt))
	switch {
	case strings.HasPrefix(upper, "CREATE TABLE"), strings.HasPrefix(upper, "CREATE UNLOGGED TABLE"):
		applyCreateTable(schema, stmt)
	case strings.HasPrefix(upper, "ALTER TABLE"):
		applyAlterTable(schema, stmt)
	case strings.HasPrefix(upper, "DROP TABLE"):
		rest := strings.TrimSpace(stmt[len("DROP TABLE"):])
		rest = trimPrefixFold(rest, "IF EXISTS")
		name, _ := splitFirstToken(rest)
		schema.DropTable(unquoteIdent(name))
	}
}

func applyCreateTable(schema *Schema, stmt string) {
	name, body, ok := parseCreateTable(stmt)
	if !ok {
		return
	}
	table := schema.AddTable(name)
	for _, item := range SplitTopLevel(body, ',') {
		item = strings.TrimSpace(item)
		if item == "" || isTableConstraint(item) {
			continue
		}
		column, raw, ok := parseColumnDef(item)
		if !ok {
			continue
		}
		table.SetColumnAccept(column, raw, PGStorageClass(raw), PGStorageClasses(raw))
	}
}

func applyAlterTable(schema *Schema, stmt string) {
	rest := strings.TrimSpace(stmt[len("ALTER TABLE"):])
	rest = trimPrefixFold(rest, "IF EXISTS")
	rest = trimPrefixFold(rest, "ONLY")
	nameToken, remainder := splitFirstToken(rest)
	if nameToken == "" {
		return
	}
	tableName := unquoteIdent(nameToken)

	for _, action := range SplitTopLevel(remainder, ',') {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		upper := strings.ToUpper(action)
		switch {
		case strings.HasPrefix(upper, "ADD COLUMN"):
			colDef := trimPrefixFold(strings.TrimSpace(action[len("ADD COLUMN"):]), "IF NOT EXISTS")
			addColumn(schema, tableName, colDef)
		case strings.HasPrefix(upper, "ADD "):
			colDef := strings.TrimSpace(action[len("ADD "):])
			first, _ := splitFirstToken(colDef)
			switch strings.ToUpper(first) {
			case "CONSTRAINT", "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "EXCLUDE":
				// Table-level constraint, not a column.
			default:
				addColumn(schema, tableName, colDef)
			}
		case strings.HasPrefix(upper, "DROP COLUMN"):
			col := trimPrefixFold(strings.TrimSpace(action[len("DROP COLUMN"):]), "IF EXISTS")
			colToken, _ := splitFirstToken(col)
			if table, ok := schema.Table(tableName); ok {
				table.DropColumn(unquoteIdent(colToken))
			}
		case strings.HasPrefix(upper, "RENAME TO"):
			newName, _ := splitFirstToken(strings.TrimSpace(action[len("RENAME TO"):]))
			schema.RenameTable(tableName, unquoteIdent(newName))
			tableName = unquoteIdent(newName)
		case strings.HasPrefix(upper, "RENAME COLUMN"):
			from, remainder := splitFirstToken(strings.TrimSpace(action[len("RENAME COLUMN"):]))
			to := trimPrefixFold(strings.TrimSpace(remainder), "TO")
			toToken, _ := splitFirstToken(to)
			if table, ok := schema.Table(tableName); ok {
				table.RenameColumn(unquoteIdent(from), unquoteIdent(toToken))
			}
		}
	}
}

func addColumn(schema *Schema, tableName, colDef string) {
	column, raw, ok := parseColumnDef(colDef)
	if !ok {
		return
	}
	table, ok := schema.Table(tableName)
	if !ok {
		// An ALTER on a table the snapshot does not know about (dropped earlier
		// or created by a statement the parser does not handle) is skipped
		// rather than invented.
		return
	}
	table.SetColumnAccept(column, raw, PGStorageClass(raw), PGStorageClasses(raw))
}

// columnConstraintKeywords terminate a column's type. "WITH" and "PRECISION"
// are deliberately absent: they are part of multi-word types such as
// "TIMESTAMP WITH TIME ZONE" and "DOUBLE PRECISION".
var columnConstraintKeywords = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "UNIQUE": true,
	"REFERENCES": true, "CHECK": true, "CONSTRAINT": true, "GENERATED": true,
	"COLLATE": true, "DEFERRABLE": true, "INITIALLY": true,
}

func parseColumnDef(def string) (name, rawType string, ok bool) {
	fields := strings.Fields(def)
	if len(fields) == 0 {
		return "", "", false
	}
	name = unquoteIdent(fields[0])
	var typeTokens []string
	for _, token := range fields[1:] {
		if columnConstraintKeywords[strings.ToUpper(token)] {
			break
		}
		typeTokens = append(typeTokens, token)
	}
	if len(typeTokens) == 0 {
		return "", "", false
	}
	return name, strings.Join(typeTokens, " "), true
}

// isTableConstraint reports whether a CREATE TABLE item is a table-level
// constraint rather than a column definition. The keyword is extracted by hand
// because a constraint may be written with no space before its parentheses
// ("UNIQUE(customer_id, ...)").
func isTableConstraint(item string) bool {
	switch leadingKeyword(item) {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "EXCLUDE", "LIKE":
		return true
	}
	return false
}

// leadingKeyword returns the leading identifier of an item, uppercased.
func leadingKeyword(item string) string {
	item = strings.TrimSpace(item)
	i := 0
	for i < len(item) && (isIdentByte(item[i])) {
		i++
	}
	return strings.ToUpper(item[:i])
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func parseCreateTable(stmt string) (name, body string, ok bool) {
	upper := strings.ToUpper(stmt)
	idx := strings.Index(upper, "TABLE")
	if idx < 0 {
		return "", "", false
	}
	rest := strings.TrimSpace(stmt[idx+len("TABLE"):])
	rest = trimPrefixFold(rest, "IF NOT EXISTS")
	paren := strings.IndexByte(rest, '(')
	if paren < 0 {
		return "", "", false
	}
	name = unquoteIdent(strings.TrimSpace(rest[:paren]))
	body, ok = extractParenBody(rest[paren:])
	return name, body, ok
}

// extractParenBody returns the text inside the leading balanced parentheses.
func extractParenBody(s string) (string, bool) {
	if !strings.HasPrefix(s, "(") {
		return "", false
	}
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], true
			}
		}
	}
	return "", false
}

// StripComments removes SQL line and block comments while leaving string
// literals intact.
func StripComments(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'':
			i = copyQuoted(&b, sql, i)
		case hasSQLPrefix(sql, i, "--"):
			i = skipLineComment(sql, i)
			b.WriteByte('\n')
		case hasSQLPrefix(sql, i, "/*"):
			i = skipBlockComment(sql, i)
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}

// copyQuoted copies the single-quoted literal starting at i (which must point at
// the opening quote) and returns the index after its closing quote.
func copyQuoted(b *strings.Builder, sql string, i int) int {
	b.WriteByte(sql[i])
	i++
	for i < len(sql) {
		b.WriteByte(sql[i])
		if sql[i] == '\'' {
			if i+1 < len(sql) && sql[i+1] == '\'' {
				b.WriteByte(sql[i+1])
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

func skipLineComment(sql string, i int) int {
	for i < len(sql) && sql[i] != '\n' {
		i++
	}
	return i
}

func skipBlockComment(sql string, i int) int {
	i += 2
	for i+1 < len(sql) {
		if sql[i] == '*' && sql[i+1] == '/' {
			return i + 2
		}
		i++
	}
	return len(sql)
}

func hasSQLPrefix(sql string, i int, prefix string) bool {
	return i+len(prefix) <= len(sql) && sql[i:i+len(prefix)] == prefix
}

// SplitStatements splits SQL on semicolons outside string literals.
func SplitStatements(sql string) []string {
	var statements []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			b.WriteByte(c)
			if inQuote && i+1 < len(sql) && sql[i+1] == '\'' {
				b.WriteByte(sql[i+1])
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if c == ';' && !inQuote {
			if s := strings.TrimSpace(b.String()); s != "" {
				statements = append(statements, s)
			}
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		statements = append(statements, s)
	}
	return statements
}

// SplitTopLevel splits on sep at parenthesis depth zero, outside string
// literals.
func SplitTopLevel(s string, sep byte) []string {
	var parts []string
	var b strings.Builder
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			b.WriteByte(c)
			if inQuote && i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte(s[i+1])
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if !inQuote {
			switch c {
			case '(':
				depth++
			case ')':
				if depth > 0 {
					depth--
				}
			}
			if c == sep && depth == 0 {
				parts = append(parts, b.String())
				b.Reset()
				continue
			}
		}
		b.WriteByte(c)
	}
	parts = append(parts, b.String())
	return parts
}

// splitFirstToken returns the leading token (respecting double-quoted
// identifiers) and the remainder.
func splitFirstToken(s string) (token, rest string) {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return "", ""
	}
	if s[0] == '"' {
		if end := strings.IndexByte(s[1:], '"'); end >= 0 {
			return s[:end+2], s[end+2:]
		}
		return s, ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

func unquoteIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return strings.ToLower(s)
}

func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return strings.TrimSpace(s[len(prefix):])
	}
	return s
}
