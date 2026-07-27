// Package migrations embeds the versioned PostgreSQL schema migrations.
package migrations

import "embed"

// FS contains every versioned up/down SQL migration.
//
//go:embed *.sql
var FS embed.FS
