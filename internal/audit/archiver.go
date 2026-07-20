package audit

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// archiveEventStore is the narrow store interface needed by the archiver.
type archiveEventStore interface {
	ListOlderThan(ctx context.Context, cutoff time.Time, batchSize int) ([]*store.AuditEvent, error)
	DeleteByIDs(ctx context.Context, ids []string) (int64, error)
}

// ArchiverImpl is the concrete implementation of Archiver.
type ArchiverImpl struct {
	store archiveEventStore
	sink  ArchiveSink
}

// NewArchiver creates a new Archiver with the given store and sink.
func NewArchiver(aeStore archiveEventStore, sink ArchiveSink) *ArchiverImpl {
	return &ArchiverImpl{store: aeStore, sink: sink}
}

var _ Archiver = (*ArchiverImpl)(nil)

// Archive exports events with created_at < cutoff as gzipped JSONL,
// writes a SHA-256 sidecar, and conditionally deletes exported events.
// Archive is idempotent: if the archive object already exists at the
// deterministic path and its checksum matches, only the DB cleanup is
// retried. On any encoding, I/O, or checksum failure, no events are deleted.
func (a *ArchiverImpl) Archive(ctx context.Context, cfg ArchiveConfig, cutoff time.Time) (*ArchiveBatch, error) { //nolint:gocyclo // sequential archive pipeline; extraction would scatter cleanup
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid archive config: %w", err)
	}
	if !cfg.Enabled() {
		return nil, nil
	}

	hasher := sha256.New()
	tempFile, tempPath, err := a.sink.CreateTemp(cfg.ArchiveDir, "audit_archive_*.tmp")
	if err != nil {
		return nil, err
	}
	cleanupTemp := func() { tempFile.Close(); os.Remove(tempPath) }

	// Tee writes to both the temp file and the SHA-256 hasher.
	writer := io.MultiWriter(tempFile, hasher)
	gzWriter := gzip.NewWriter(writer)
	enc := json.NewEncoder(gzWriter)

	var allIDs []string
	var eventCount int64

	for {
		if err := ctx.Err(); err != nil {
			cleanupTemp()
			return nil, err
		}
		events, err := a.store.ListOlderThan(ctx, cutoff, cfg.BatchSize)
		if err != nil {
			cleanupTemp()
			return nil, fmt.Errorf("list older events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			sanitized := sanitizeAuditEvent(event)
			if err := enc.Encode(sanitized); err != nil {
				cleanupTemp()
				return nil, fmt.Errorf("encode event %s: %w", event.ID, err)
			}
			allIDs = append(allIDs, event.ID)
			eventCount++
		}
		if len(events) < cfg.BatchSize {
			break
		}
	}

	// Close writers in order: gzip → temp file.
	if err := gzWriter.Close(); err != nil {
		cleanupTemp()
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("close temp file: %w", err)
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))
	objectName := archiveObjectName(cutoff, checksum)
	finalPath := filepath.Join(cfg.ArchiveDir, objectName)

	// Idempotency: if the archive already exists with matching checksum,
	// skip the commit and only retry deletion of DB events.
	existing, err := verifyExistingArchive(finalPath, checksum)
	if err != nil {
		os.Remove(tempPath)
		return nil, err
	}
	if existing {
		os.Remove(tempPath)
	} else {
		if err := a.sink.Commit(tempPath, finalPath, checksum); err != nil {
			return nil, fmt.Errorf("commit archive: %w", err)
		}
	}

	// Transactionally delete exported events.
	if len(allIDs) > 0 {
		if _, err := a.store.DeleteByIDs(ctx, allIDs); err != nil {
			return nil, fmt.Errorf("delete archived events: %w", err)
		}
	}

	return &ArchiveBatch{
		Cutoff:     cutoff,
		FilePath:   finalPath,
		Checksum:   checksum,
		EventCount: eventCount,
	}, nil
}

// DryRun counts events with created_at < cutoff without writing or deleting.
func (a *ArchiverImpl) DryRun(ctx context.Context, cfg ArchiveConfig, cutoff time.Time) (int64, error) {
	if err := cfg.Validate(); err != nil {
		return 0, fmt.Errorf("invalid archive config: %w", err)
	}
	if !cfg.Enabled() {
		return 0, nil
	}

	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		events, err := a.store.ListOlderThan(ctx, cutoff, cfg.BatchSize)
		if err != nil {
			return 0, fmt.Errorf("list older events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		count += int64(len(events))
		if len(events) < cfg.BatchSize {
			break
		}
	}
	return count, nil
}

// verifyExistingArchive checks whether finalPath and its sidecar exist
// and whether the stored checksum matches expectedChecksum.
// Returns (true, nil) on match, (false, nil) if neither file exists,
// (false, error) on mismatch or I/O error.
func verifyExistingArchive(finalPath, expectedChecksum string) (bool, error) {
	sidecar := finalPath + ".sha256"
	data, err := os.ReadFile(sidecar)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read sidecar %s: %w", sidecar, err)
	}
	parts := strings.Fields(string(data))
	if len(parts) < 1 || parts[0] != expectedChecksum {
		return false, fmt.Errorf("archive %s exists but checksum mismatch: expected %s, got %s",
			finalPath, expectedChecksum, strings.Join(parts, " "))
	}
	if _, err := os.Stat(finalPath); err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("archive %s: sidecar exists but file missing", finalPath)
		}
		return false, fmt.Errorf("stat archive %s: %w", finalPath, err)
	}
	return true, nil
}
