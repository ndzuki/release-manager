// Package migrations embeds the versioned PostgreSQL schema migrations.
package migrations

import (
	"embed"
	"io/fs"
)

// FS contains every versioned up/down SQL migration for the orchestrator
// (release_manager) database.
//
//go:embed *.sql
var FS embed.FS

// ReleaseNotifierFS exposes the release_notifier database migrations (REQ-031
// PostgreSQL contract) with the migration files at the FS root, as required by
// golang-migrate's iofs source (iofs.New(fs, ".")).
//
//go:embed release_notifier/*.sql
var releaseNotifierEmbed embed.FS

// ReleaseNotifierFS returns the embedded release_notifier migrations.
func ReleaseNotifierFS() fs.FS {
	sub, err := fs.Sub(releaseNotifierEmbed, "release_notifier")
	if err != nil {
		// The embed directive guarantees the directory exists; a failure here
		// is a build-time invariant violation.
		panic(err)
	}
	return sub
}
