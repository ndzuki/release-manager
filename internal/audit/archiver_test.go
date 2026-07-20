package audit_test

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupArchiver(t *testing.T) (*audit.ArchiverImpl, *sqlitestore.Store, func()) {
	t.Helper()
	st, err := sqlitestore.Open("file::memory:?cache=shared")
	require.NoError(t, err)
	sink := audit.NewFileSystemSink()
	arch := audit.NewArchiver(st.AuditEvents(), sink)
	cleanup := func() { st.Close() }
	return arch, st, cleanup
}

// seedArchiveEvents inserts count events with created_at = base + offset each.
func seedArchiveEvents(t *testing.T, st *sqlitestore.Store, count int, base time.Time) []string {
	t.Helper()
	ids := make([]string, count)
	for i := range count {
		id := fmt.Sprintf("archive-ev-%03d", i)
		ids[i] = id
		require.NoError(t, st.AuditEvents().Create(context.Background(), &store.AuditEvent{
			ID:             id,
			OrganizationID: "org-001",
			ActorKind:      store.AuditActorSystem,
			ResourceType:   "release_operation",
			ResourceID:     "op-001",
			Action:         "create",
			Status:         "accepted",
			Metadata:       map[string]string{"idx": fmt.Sprintf("%d", i)},
			CreatedAt:      base.Add(time.Duration(i) * time.Minute),
		}))
	}
	return ids
}

func TestArchiverRetentionDaysZeroReturnsNil(t *testing.T) {
	arch, _, cleanup := setupArchiver(t)
	defer cleanup()

	cfg := audit.DefaultArchiveConfig()
	cfg.RetentionDays = 0

	batch, err := arch.Archive(context.Background(), cfg, time.Now())
	require.NoError(t, err)
	assert.Nil(t, batch)
}

func TestArchiverArchiveCreatesGzipJSONL(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 5, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()
	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	batch, err := arch.Archive(context.Background(), cfg, cutoff)
	require.NoError(t, err)
	require.NotNil(t, batch)
	assert.Equal(t, int64(5), batch.EventCount)
	assert.FileExists(t, batch.FilePath)
	assert.NotEmpty(t, batch.Checksum)

	// Verify the archive is valid gzip containing JSONL.
	f, err := os.Open(batch.FilePath)
	require.NoError(t, err)
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	require.NoError(t, err)
	defer gzReader.Close()

	data, err := io.ReadAll(gzReader)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 5)
	for _, line := range lines {
		var event store.AuditEvent
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		assert.Equal(t, "org-001", event.OrganizationID)
	}
}

func TestArchiverChecksumSidecar(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 3, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()
	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	batch, err := arch.Archive(context.Background(), cfg, cutoff)
	require.NoError(t, err)

	sidecar := batch.FilePath + ".sha256"
	data, err := os.ReadFile(sidecar)
	require.NoError(t, err)

	parts := strings.Fields(string(data))
	require.GreaterOrEqual(t, len(parts), 1)
	assert.Equal(t, batch.Checksum, parts[0])

	archiveBytes, err := os.ReadFile(batch.FilePath)
	require.NoError(t, err)
	actual := sha256.Sum256(archiveBytes)
	assert.Equal(t, batch.Checksum, hex.EncodeToString(actual[:]))
}

func TestArchiverDeletesArchivedEvents(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	ids := seedArchiveEvents(t, st, 5, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()
	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	batch, err := arch.Archive(context.Background(), cfg, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(5), batch.EventCount)

	for _, id := range ids {
		_, err := st.AuditEvents().GetByID(context.Background(), id)
		assert.ErrorIs(t, err, store.ErrNotFound, "event %s should be deleted", id)
	}
}

func TestArchiverPreservesRecentEvents(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	require.NoError(t, st.AuditEvents().Create(context.Background(), &store.AuditEvent{
		ID:             "recent-001",
		OrganizationID: "org-001",
		ActorKind:      store.AuditActorSystem,
		ResourceType:   "release_operation",
		ResourceID:     "op-001",
		Action:         "create",
		Status:         "accepted",
		Metadata:       map[string]string{},
		CreatedAt:      time.Now().Add(-1 * time.Hour),
	}))

	base := time.Now().Add(-200 * 24 * time.Hour)
	oldIDs := seedArchiveEvents(t, st, 3, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()
	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	batch, err := arch.Archive(context.Background(), cfg, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(3), batch.EventCount)

	for _, id := range oldIDs {
		_, err := st.AuditEvents().GetByID(context.Background(), id)
		assert.ErrorIs(t, err, store.ErrNotFound, "old event %s should be deleted", id)
	}

	recent, err := st.AuditEvents().GetByID(context.Background(), "recent-001")
	require.NoError(t, err)
	assert.Equal(t, "recent-001", recent.ID)
}

func TestArchiverIdempotentSameCutoff(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	// Use a fixed cutoff far in the future so all events are eligible.
	fixedCutoff := time.Now().Add(365 * 24 * time.Hour)
	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 3, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()

	// First run: archive + delete.
	batch1, err := arch.Archive(context.Background(), cfg, fixedCutoff)
	require.NoError(t, err)
	require.NotNil(t, batch1)
	assert.Equal(t, int64(3), batch1.EventCount)

	// Second run with same cutoff: no events left, empty archive.
	batch2, err := arch.Archive(context.Background(), cfg, fixedCutoff)
	require.NoError(t, err)
	require.NotNil(t, batch2)
	assert.Equal(t, int64(0), batch2.EventCount)
}

func TestArchiverDryRunCountsOnly(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	ids := seedArchiveEvents(t, st, 4, base)

	cfg := audit.DefaultArchiveConfig()
	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	count, err := arch.DryRun(context.Background(), cfg, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(4), count)

	for _, id := range ids {
		ev, err := st.AuditEvents().GetByID(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, id, ev.ID)
	}
}

func TestArchiverChecksumMismatchErrors(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	fixedCutoff := time.Now().Add(365 * 24 * time.Hour)
	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 3, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.ArchiveDir = t.TempDir()

	// First run succeeds.
	batch1, err := arch.Archive(context.Background(), cfg, fixedCutoff)
	require.NoError(t, err)

	// Corrupt the sidecar of the first archive.
	sidecar := batch1.FilePath + ".sha256"
	require.NoError(t, os.WriteFile(sidecar, []byte("0000000000000000000000000000000000000000000000000000000000000000  audit.jsonl.gz\n"), 0o640))

	// Recreate the same events so the stream produces the same checksum.
	seedArchiveEvents(t, st, 3, base)

	// Second run: same checksum → same path → corrupted sidecar → error.
	_, err = arch.Archive(context.Background(), cfg, fixedCutoff)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestArchiverContextCancellationCleansUp(t *testing.T) {
	arch, st, cleanup := setupArchiver(t)
	defer cleanup()

	base := time.Now().Add(-180 * 24 * time.Hour)
	seedArchiveEvents(t, st, 50, base)

	cfg := audit.DefaultArchiveConfig()
	cfg.BatchSize = 1
	cfg.ArchiveDir = t.TempDir()
	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := arch.Archive(ctx, cfg, cutoff)
	assert.ErrorIs(t, err, context.Canceled)

	count, err := st.AuditEvents().Count(context.Background(), store.AuditEventFilter{OrganizationID: "org-001"})
	require.NoError(t, err)
	assert.Equal(t, int64(50), count)
}
