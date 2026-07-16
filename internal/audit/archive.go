package audit

import (
	"context"
	"time"
)

// ArchiveRequest defines parameters for audit event archiving (REQ-030).
type ArchiveRequest struct {
	// OlderThan specifies the age threshold for archiving.
	OlderThan time.Duration

	// DryRun when true performs a count without actual archiving.
	DryRun bool
}

// ArchiveResult reports the outcome of an archive operation.
type ArchiveResult struct {
	EventsArchived int64
	ArchivePath    string
	Checksum       string
}

// Archiver defines the audit archive interface (REQ-030).
// BLOCKED: TASK-005 — depends on REQ-029 audit query pipeline.
// The archiver:
//  1. Queries audit events older than the threshold
//  2. Exports them as gzipped JSONL with a SHA-256 checksum
//  3. Deletes the exported events in a single transaction
type Archiver interface {
	// Archive exports and prunes audit events older than the given threshold.
	// Returns the number of events archived and the archive file path.
	Archive(ctx context.Context, req ArchiveRequest) (*ArchiveResult, error)

	// Restore replays archived events back into the audit store.
	Restore(ctx context.Context, archivePath string) (int, error)
}
