package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
)

// errGCLockHeld reports that another GC run (local mutex or a remote replica's
// advisory lock) is already executing. Callers decide between a Connect
// resource_exhausted error (RPC) and skipping the cycle (ticker/startup).
var errGCLockHeld = errors.New("cleanup lock held")

// runGCWithKey executes one GC cycle with a stable key and records the outcome
// on the health tracker (AC-069-32/40) plus the audit log (REQ-069 审计).
// Cycles that cannot acquire the GC lock are skipped without audit or success
// updates (REQ: 任一 Phase 执行才审计).
func (s *CleanupService) runGCWithKey(ctx context.Context, key string) {
	s.health.RecordAttempt()
	resp, errs, err := s.runGC(ctx)
	if err != nil {
		// The lock-held path already logged the specific DEBUG reason.
		s.logger.Debug("gc_cycle_skipped", "key", key, "error", err)
		return
	}
	s.emitGCAudit(ctx, key, resp, errs)
	s.logger.Info("gc_completed",
		"idempotency_key", key,
		"deleted_bundles", resp.GetDeletedBundles(),
		"deleted_candidates", resp.GetDeletedCandidates(),
		"deleted_preflights", resp.GetDeletedPreflights(),
		"skipped_bundles", resp.GetSkippedBundles(),
		"errors", len(errs),
	)
	if len(errs) == 0 {
		s.recordSuccess()
	}
}

// runGC executes the ordered six-phase garbage collection. The returned error
// is non-nil only for pre-start failures (lock acquisition); phase-level
// failures are accumulated in the response errors slice (AC-069-52).
//
//nolint:gocyclo // The six-phase pipeline is intentionally sequential; each phase is a bounded loop.
func (s *CleanupService) runGC(ctx context.Context) (*orchestratorv1.RunCleanupResponse, []string, error) {
	resp := &orchestratorv1.RunCleanupResponse{}
	timeout := gcDefaultTimeout
	if s.config.GCMaxDurationMinutes > 0 {
		timeout = time.Duration(s.config.GCMaxDurationMinutes) * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	release, acquired, err := s.acquireGCLock(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire gc advisory lock: %w", err)
	}
	if !acquired {
		return nil, nil, errGCLockHeld
	}
	defer func() {
		if err := release(); err != nil {
			s.logger.Warn("gc lock release failed", "error", err)
		}
	}()

	guard := func() (bool, string) {
		if err := ctx.Err(); err != nil {
			return true, fmt.Sprintf("Phase0: %s", err)
		}
		return false, ""
	}
	stop := func(errs []string) (*orchestratorv1.RunCleanupResponse, []string) {
		resp.Errors = errs
		return resp, errs
	}
	errs := make([]string, 0, 6)
	if stopped, reason := guard(); stopped {
		resp, errs := stop(append(errs, reason))
		return resp, errs, nil
	}

	terminalStates := []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed, store.StatusCancelled, store.StatusTimeout,
	}

	// Phase 1: archive eligible bundles in bounded batches.
	for batch := 1; ; batch++ {
		if stopped, reason := guard(); stopped {
			resp, errs := stop(append(errs, reason))
		return resp, errs, nil
		}
		ids, err := s.store.Bundles().ListForArchive(ctx, s.config.BundleRetentionDays, terminalStates, gcBatchLimit)
		if err != nil {
			errs = append(errs, s.phaseError(1, batch, err))
			break
		}
		if len(ids) == 0 {
			break
		}
		n, err := s.store.Bundles().Archive(ctx, ids)
		if err != nil {
			errs = append(errs, s.phaseError(1, batch, err))
			break
		}
		if n < int64(len(ids)) {
			resp.SkippedBundles += int64(len(ids)) - n
		}
		if n == 0 {
			break
		}
	}

	// Phase 2: physically delete archived bundles past archive grace.
	if stopped, reason := guard(); stopped {
		resp, errs := stop(append(errs, reason))
		return resp, errs, nil
	}
	archiveCutoff := time.Now().UTC().AddDate(0, 0, -s.config.ArchiveGraceDays)
	for batch := 1; ; batch++ {
		if stopped, reason := guard(); stopped {
			resp, errs := stop(append(errs, reason))
		return resp, errs, nil
		}
		n, err := s.store.Bundles().DeleteExpiredBefore(ctx, archiveCutoff, gcBatchLimit)
		if err != nil {
			errs = append(errs, s.phaseError(2, batch, err))
			break
		}
		resp.DeletedBundles += n
		if n < gcBatchLimit {
			break
		}
	}

	// Phase 3: delete orphan candidate artifacts past their TTL.
	if stopped, reason := guard(); stopped {
		resp, errs := stop(append(errs, reason))
		return resp, errs, nil
	}
	candidateCutoff := time.Now().UTC().AddDate(0, 0, -s.config.CandidateArtifactRetentionDays)
	for batch := 1; ; batch++ {
		if stopped, reason := guard(); stopped {
			resp, errs := stop(append(errs, reason))
		return resp, errs, nil
		}
		n, err := s.store.CandidateArtifacts().DeleteOrphanBefore(ctx, candidateCutoff, gcBatchLimit)
		if err != nil {
			errs = append(errs, s.phaseError(3, batch, err))
			break
		}
		resp.DeletedCandidates += n
		if n < gcBatchLimit {
			break
		}
	}

	// Phase 4: delete expired preflight lifecycle rows.
	if stopped, reason := guard(); stopped {
		resp, errs := stop(append(errs, reason))
		return resp, errs, nil
	}
	preflightTTL := time.Duration(s.config.PreflightRetentionDays) * 24 * time.Hour
	orphanPreflightTTL := time.Duration(s.config.OrphanPreflightRetentionDays) * 24 * time.Hour
	for batch := 1; ; batch++ {
		if stopped, reason := guard(); stopped {
			resp, errs := stop(append(errs, reason))
		return resp, errs, nil
		}
		n, err := s.store.PreflightLifecycles().DeleteExpired(ctx, preflightTTL, orphanPreflightTTL, gcBatchLimit)
		if err != nil {
			errs = append(errs, s.phaseError(4, batch, err))
			break
		}
		resp.DeletedPreflights += n
		if n < gcBatchLimit {
			break
		}
	}

	// Phase 5: purge persistent cleanup idempotency records.
	if stopped, reason := guard(); stopped {
		resp, errs := stop(append(errs, reason))
		return resp, errs, nil
	}
	if idem := s.store.CleanupIdempotency(); idem != nil {
		idempotencyCutoff := time.Now().UTC().Add(-time.Duration(s.config.CleanupIdempotencyRetentionHours) * time.Hour)
		for batch := 1; ; batch++ {
			if stopped, reason := guard(); stopped {
				resp, errs := stop(append(errs, reason))
		return resp, errs, nil
			}
			n, err := idem.DeleteExpiredBefore(ctx, idempotencyCutoff, gcBatchLimit)
			if err != nil {
				if errors.Is(err, store.ErrCleanupIdempotencyUnavailable) {
					break
				}
				errs = append(errs, s.phaseError(5, batch, err))
				break
			}
			if n < gcBatchLimit {
				break
			}
		}
	}

	resp.Errors = errs
	return resp, errs, nil
}

// acquireGCLock takes the in-process mutex first (RPC > ticker priority,
// AC-069-51) and then the PostgreSQL advisory lock for cross-replica
// exclusivity (AC-069-28). The lock is held on a dedicated *sql.Conn and
// released before it returns to the pool.
func (s *CleanupService) acquireGCLock(ctx context.Context) (release func() error, acquired bool, err error) {
	if !s.gcMu.TryLock() {
		s.logger.Debug("gc_skipped_mutex_held")
		return nil, false, nil
	}
	releaseLocal := func() error {
		s.gcMu.Unlock()
		return nil
	}
	provider, ok := s.store.(interface{ SQLDB() *sql.DB })
	if !ok || provider.SQLDB() == nil {
		return releaseLocal, true, nil
	}
	lock, acquired, err := postgres.TryAcquireAdvisoryLock(ctx, provider.SQLDB(), gcAdvisoryLockKey)
	if err != nil {
		s.gcMu.Unlock()
		return nil, false, err
	}
	if !acquired {
		s.gcMu.Unlock()
		s.logger.Debug("gc_skipped_advisory_lock_held")
		return nil, false, nil
	}
	return func() error {
		unlockErr := lock.Unlock()
		s.gcMu.Unlock()
		return unlockErr
	}, true, nil
}

// phaseError renders the stable, sanitized error entry format required by the
// output contract: "Phase{N} batch{M}: {reason}" (AC-069-36/52).
func (s *CleanupService) phaseError(phase, batch int, err error) string {
	reason := "operation failed"
	if err != nil && err.Error() != "" {
		reason = err.Error()
	}
	return fmt.Sprintf("Phase%d batch%d: %s", phase, batch, reason)
}
