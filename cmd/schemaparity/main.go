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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ndzuki/release-manager/internal/quality/schemaparity"
)

// exception is one accepted drift. Mirrors errcodes.exceptions.yaml: every entry
// needs an owner, a reason and an expires_at, so an allowance cannot become
// permanent and the gate re-checks it once the date passes.
type exception struct {
	Owner     string `yaml:"owner"`
	Table     string `yaml:"table"`
	Column    string `yaml:"column"`
	Reason    string `yaml:"reason"`
	ExpiresAt string `yaml:"expires_at"`
}

type exceptionFile struct {
	Version    string      `yaml:"version"`
	Exceptions []exception `yaml:"exceptions"`
}

func loadExceptions(path, today string) (map[string]exception, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var file exceptionFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make(map[string]exception, len(file.Exceptions))
	for _, e := range file.Exceptions {
		if e.Owner == "" || e.Reason == "" || e.ExpiresAt == "" {
			return nil, fmt.Errorf("%s: exception %s.%s needs an owner, a reason and an expires_at", path, e.Table, e.Column)
		}
		if e.ExpiresAt < today {
			return nil, fmt.Errorf("%s: exception for %s.%s expired on %s (%s)", path, e.Table, e.Column, e.ExpiresAt, e.Reason)
		}
		out[e.Table+"."+e.Column] = e
	}
	return out, nil
}

func main() {
	migrations := flag.String("migrations", "migrations", "directory holding the PostgreSQL *.up.sql migrations")
	exceptionsPath := flag.String("exceptions", "", "path to the schema-parity exception list (optional)")
	flag.Parse()

	excepted := map[string]exception{}
	if *exceptionsPath != "" {
		var err error
		excepted, err = loadExceptions(*exceptionsPath, time.Now().UTC().Format("2006-01-02"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "schemaparity: %v\n", err)
			os.Exit(2)
		}
	}

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

	unexcepted := make([]schemaparity.Finding, 0, len(report.Drift))
	for _, finding := range report.Drift {
		if e, ok := excepted[finding.Table+"."+finding.Column]; ok {
			fmt.Printf("schemaparity: excepted until %s (%s): %s.%s [%s]\n", e.ExpiresAt, e.Reason, finding.Table, finding.Column, finding.Kind)
			continue
		}
		fmt.Println(finding.String())
		unexcepted = append(unexcepted, finding)
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

	if len(unexcepted) > 0 {
		os.Exit(1)
	}
}
