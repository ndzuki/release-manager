// Command schemaparity compares the SQLite and PostgreSQL schema snapshots and
// fails on real drift (a same-named table whose column set or column type
// differs). Tables that exist on only one side are reported with their counts
// and names but do not fail the gate: the two engines legitimately do not carry
// the same table set.
//
// Usage:
//
//	schemaparity [-migrations migrations]
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/schemaparity"
)

func main() {
	migrations := flag.String("migrations", "migrations", "directory holding the PostgreSQL *.up.sql migrations")
	flag.Parse()

	sqliteSchema, err := schemaparity.SnapshotSQLite()
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemaparity: sqlite snapshot: %v\n", err)
		os.Exit(2)
	}
	pgSchema, err := schemaparity.SnapshotPostgres(*migrations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemaparity: postgres snapshot: %v\n", err)
		os.Exit(2)
	}

	report := schemaparity.Diff(sqliteSchema, pgSchema)

	for _, finding := range report.Drift {
		fmt.Println(finding.String())
	}

	fmt.Printf("schemaparity: sqlite=%d table(s), postgres=%d table(s), common=%d, sqlite-only=%d, postgres-only=%d, drift=%d\n",
		report.SQLiteTables, report.PGTables, len(report.CommonTables),
		len(report.SQLiteOnly), len(report.PGOnly), len(report.Drift))

	if len(report.SQLiteOnly) > 0 {
		fmt.Printf("schemaparity: sqlite-only tables (reported, not failed): %v\n", report.SQLiteOnly)
	}
	if len(report.PGOnly) > 0 {
		fmt.Printf("schemaparity: postgres-only tables (reported, not failed): %v\n", report.PGOnly)
	}

	if report.Failed() {
		os.Exit(1)
	}
}
