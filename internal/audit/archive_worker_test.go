package audit_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
)

var testLogger = slog.New(slog.DiscardHandler)

func TestArchiveWorkerStartStop(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 3, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()
	cfg.PollInterval = 50 * time.Millisecond

	worker := audit.NewArchiveWorker(arch, cfg, testLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	worker.Run(ctx)

	count, err := st.AuditEvents().Count(context.Background(), store.AuditEventFilter{OrganizationID: "org-001"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestArchiveWorkerDisabled(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 3, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.RetentionDays = 0

	worker := audit.NewArchiveWorker(arch, cfg, testLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	worker.Run(ctx)

	count, err := st.AuditEvents().Count(context.Background(), store.AuditEventFilter{OrganizationID: "org-001"})
	require.NoError(t, err)
	assert.Greater(t, count, int64(0), "events should not be archived when disabled")
}

func TestArchiveWorkerUpdateConfig(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()

	worker := audit.NewArchiveWorker(arch, cfg, testLogger)

	// Valid update.
	err := worker.UpdateConfig(audit.ArchiveConfig{
		RetentionDays:     30,
		PollInterval:      1 * time.Hour,
		BatchSize:         500,
		ArchiveDir:        "/tmp/archives",
		Compression:       "gzip_jsonl",
		ChecksumAlgorithm: "sha256",
	})
	require.NoError(t, err)

	// Invalid update: negative retention.
	err = worker.UpdateConfig(audit.ArchiveConfig{
		RetentionDays: -1,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "retention_days")
	// Invalid update: unsupported compression.
	err = worker.UpdateConfig(audit.ArchiveConfig{
		RetentionDays: 30,
		PollInterval:  1 * time.Hour,
		BatchSize:     100,
		ArchiveDir:    "/tmp",
		Compression:   "zstd",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported compression")

	// DB should be unaffected by config-only changes.
	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 1, base)
	_, getErr := st.AuditEvents().GetByID(context.Background(), fmt.Sprintf("archive-ev-%03d", 0))
	require.NoError(t, getErr)
}
