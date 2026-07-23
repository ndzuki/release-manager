package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// RetentionConfig holds configurable GC retention parameters.
type RetentionConfig struct {
	BundleDays           int `mapstructure:"bundle_days"`            // default 90, min 7
	CandidateArtifactDays int `mapstructure:"candidate_artifact_days"` // default 30, min 1
	PreflightResultHours int `mapstructure:"preflight_result_hours"`  // default 168 (7d), min 1
	GCIntervalHours       int `mapstructure:"gc_interval_hours"`      // default 6, min 1
}

// DefaultRetentionConfig returns safe defaults.
func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		BundleDays:            90,
		CandidateArtifactDays: 30,
		PreflightResultHours:  168,
		GCIntervalHours:       6,
	}
}

// Validate checks parameter ranges. Returns nil on success.
func (c RetentionConfig) Validate() error {
	if c.BundleDays < 7 {
		return fmt.Errorf("bundle_days must be >= 7, got %d", c.BundleDays)
	}
	if c.CandidateArtifactDays < 1 {
		return fmt.Errorf("candidate_artifact_days must be >= 1, got %d", c.CandidateArtifactDays)
	}
	if c.PreflightResultHours < 1 {
		return fmt.Errorf("preflight_result_hours must be >= 1, got %d", c.PreflightResultHours)
	}
	if c.GCIntervalHours < 1 {
		return fmt.Errorf("gc_interval_hours must be >= 1, got %d", c.GCIntervalHours)
	}
	return nil
}

// CleanupService implements the CleanupServiceHandler Connect interface.
type CleanupService struct {
	store  store.Store
	config RetentionConfig
	logger *slog.Logger

	mu       sync.Mutex
	active   map[string]struct{} // in-flight idempotency keys
	results  map[string]*orchestratorv1.RunCleanupResponse
}

// NewCleanupService creates a new CleanupService.
func NewCleanupService(st store.Store, cfg RetentionConfig, logger *slog.Logger) *CleanupService {
	return &CleanupService{
		store:   st,
		config:  cfg,
		logger:  logger,
		active:  make(map[string]struct{}),
		results: make(map[string]*orchestratorv1.RunCleanupResponse),
	}
}

// Compile-time check.
var _ orchestratorv1connect.CleanupServiceHandler = (*CleanupService)(nil)

// RunCleanup triggers artifact lifecycle GC.
func (s *CleanupService) RunCleanup(
	ctx context.Context,
	req *connect.Request[orchestratorv1.RunCleanupRequest],
) (*connect.Response[orchestratorv1.RunCleanupResponse], error) {
	key := req.Msg.IdempotencyKey
	if key == "" || len(key) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("idempotency_key must be 1-64 characters"))
	}

	s.mu.Lock()
	if _, ok := s.active[key]; ok {
		s.mu.Unlock()
		return nil, connect.NewError(connect.CodeAlreadyExists,
			fmt.Errorf("cleanup %s is already in progress", key))
	}
	if cached, ok := s.results[key]; ok {
		s.mu.Unlock()
		s.logger.Info("idempotent cleanup result returned", "key", key)
		return connect.NewResponse(cached), nil
	}
	s.active[key] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.active, key)
		s.mu.Unlock()
	}()

	start := time.Now()
	resp, errs := s.runGC(ctx, key)
	duration := time.Since(start)

	// Cache result for idempotency.
	s.mu.Lock()
	s.results[key] = resp
	s.mu.Unlock()

	// Audit log.
	s.logger.Info("cleanup_completed",
		"deleted_bundles", resp.DeletedBundles,
		"deleted_candidates", resp.DeletedCandidates,
		"deleted_preflights", resp.DeletedPreflights,
		"errors", len(errs),
		"duration_ms", duration.Milliseconds(),
		"idempotency_key", key,
	)

	if len(errs) > 0 {
		for _, e := range errs {
			s.logger.Warn("cleanup_error", "error", e, "idempotency_key", key)
		}
	}

	return connect.NewResponse(resp), nil
}

// runGC executes the 4-phase garbage collection.
func (s *CleanupService) runGC(ctx context.Context, key string) (*orchestratorv1.RunCleanupResponse, []string) {
	resp := &orchestratorv1.RunCleanupResponse{}
	var errs []string

	// Phase 1: Archive eligible bundles.
	terminalStates := []store.OperationStatus{
		store.StatusSucceeded, store.StatusFailed,
		store.StatusCancelled, store.StatusTimeout,
	}

	ids, err := s.store.Bundles().ListForArchive(ctx, s.config.BundleDays, terminalStates)
	if err != nil {
		errs = append(errs, fmt.Sprintf("phase1 list: %v", err))
	} else if len(ids) > 0 {
		n, err := s.store.Bundles().Archive(ctx, ids)
		if err != nil {
			errs = append(errs, fmt.Sprintf("phase1 archive: %v", err))
		} else {
			s.logger.Info("gc_phase1_archived", "count", n)
		}
	}

	// Phase 2: Delete expired bundles (archived > 30d grace or rejected).
	archiveCutoff := time.Now().UTC().AddDate(0, 0, -s.config.BundleDays-30)
	n, err := s.store.Bundles().DeleteBefore(ctx, archiveCutoff)
	if err != nil {
		errs = append(errs, fmt.Sprintf("phase2 delete bundles: %v", err))
	} else {
		resp.DeletedBundles = int32(n)
		s.logger.Info("gc_phase2_deleted_bundles", "count", n)
	}

	// Phase 3: Delete orphan candidate artifacts.
	candidateCutoff := time.Now().UTC().AddDate(0, 0, -s.config.CandidateArtifactDays)
	n, err = s.store.CandidateArtifacts().DeleteOrphanBefore(ctx, candidateCutoff)
	if err != nil {
		errs = append(errs, fmt.Sprintf("phase3 delete candidates: %v", err))
	} else {
		resp.DeletedCandidates = int32(n)
		s.logger.Info("gc_phase3_deleted_candidates", "count", n)
	}

	// Phase 4: Delete expired preflight lifecycles.
	preflightTTL := time.Duration(s.config.PreflightResultHours) * time.Hour
	n, err = s.store.PreflightLifecycles().DeleteExpired(ctx, preflightTTL)
	if err != nil {
		errs = append(errs, fmt.Sprintf("phase4 delete preflights: %v", err))
	} else {
		resp.DeletedPreflights = int32(n)
		s.logger.Info("gc_phase4_deleted_preflights", "count", n)
	}

	resp.Errors = errs
	return resp, errs
}

// StartTicker runs the GC on a periodic timer. Runs until ctx is canceled.
// Skips the tick if a previous GC is still running (防重叠).
func (s *CleanupService) StartTicker(ctx context.Context) {
	interval := time.Duration(s.config.GCIntervalHours) * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var running sync.Mutex

	run := func() {
		if !running.TryLock() {
			s.logger.Info("gc_skip", "reason", "previous run still active")
			return
		}
		defer running.Unlock()

		key := uuid.New().String()
		s.logger.Info("gc_ticker_start", "key", key)

		start := time.Now()
		resp, errs := s.runGC(ctx, key)
		duration := time.Since(start)

		s.logger.Info("cleanup_completed",
			"deleted_bundles", resp.DeletedBundles,
			"deleted_candidates", resp.DeletedCandidates,
			"deleted_preflights", resp.DeletedPreflights,
			"errors", len(errs),
			"duration_ms", duration.Milliseconds(),
			"idempotency_key", key,
		)
		for _, e := range errs {
			s.logger.Warn("cleanup_error", "error", e, "idempotency_key", key)
		}
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("gc_ticker_stopped")
			return
		case <-ticker.C:
			run()
		}
	}
}
