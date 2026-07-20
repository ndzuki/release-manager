package audit

import (
	"context"
	"fmt"
	"time"
)

// ArchiveBatch describes a single completed archive run.
type ArchiveBatch struct {
	// Cutoff is the time before which events were archived.
	Cutoff time.Time

	// FilePath is the path to the gzipped JSONL archive file.
	FilePath string

	// Checksum is the hex-encoded SHA-256 digest of the archive.
	Checksum string

	// EventCount is the number of events archived and deleted.
	EventCount int64
}

// Archiver exports and prunes audit events older than a given cutoff.
//
// The caller (worker) is responsible for computing the cutoff from
// retention_days; the archiver uses cfg.BatchSize and cfg.ArchiveDir.
//
// Archive:
//  1. Queries events with created_at < cutoff in stable ASC batches
//  2. Streams them as gzipped JSONL through ArchiveSink, computing SHA-256
//  3. On success, atomically commits the archive + checksum sidecar
//  4. Transactionally deletes exported events by ID
//
// Archiving is idempotent: a repeated run with the same cutoff and same
// underlying data produces the same object name. If the archive already
// exists and its checksum matches, the write is skipped.
//
// DryRun counts eligible events without writing or deleting.
type Archiver interface {
	Archive(ctx context.Context, cfg ArchiveConfig, cutoff time.Time) (*ArchiveBatch, error)
	DryRun(ctx context.Context, cfg ArchiveConfig, cutoff time.Time) (int64, error)
}

// archiveObjectName returns a deterministic archive filename.
// Format: audit_<cutoff_unix>_<checksum_prefix>.jsonl.gz
func archiveObjectName(cutoff time.Time, checksum string) string {
	prefix := checksum
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return fmt.Sprintf("audit_%d_%s.jsonl.gz", cutoff.Unix(), prefix)
}
