package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	gcBatchLimit      = 100
	gcDefaultTimeout  = 55 * time.Minute
	gcAdvisoryLockKey = int64(69069069)
)

// RetentionConfig holds configurable GC retention parameters. The gc.*
// configuration layer (GcConfig) maps onto this engine-facing shape.
type RetentionConfig struct {
	BundleRetentionDays              int `mapstructure:"bundle_retention_days"`
	ArchiveGraceDays                 int `mapstructure:"archive_grace_days"`
	CandidateArtifactRetentionDays   int `mapstructure:"candidate_artifact_retention_days"`
	PreflightRetentionDays           int `mapstructure:"preflight_retention_days"`
	OrphanPreflightRetentionDays     int `mapstructure:"orphan_preflight_retention_days"`
	GCIntervalHours                  int `mapstructure:"gc_interval_hours"`
	GCInterval                       time.Duration
	PrepareSessionHours              int `mapstructure:"prepare_session_hours"`
	PrepareSessionGCIntervalMinutes  int `mapstructure:"prepare_session_gc_interval_minutes"`
	GCMaxDurationMinutes             int `mapstructure:"gc_max_duration_minutes"`
	CleanupIdempotencyRetentionHours int `mapstructure:"cleanup_idempotency_retention_hours"`
}

// DefaultRetentionConfig returns safe defaults.
func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		BundleRetentionDays: 90, ArchiveGraceDays: 30, CandidateArtifactRetentionDays: 30,
		PreflightRetentionDays: 90, OrphanPreflightRetentionDays: 7,
		GCIntervalHours: 6, GCInterval: 6 * time.Hour, PrepareSessionHours: 24,
		PrepareSessionGCIntervalMinutes: 10, GCMaxDurationMinutes: 55,
		CleanupIdempotencyRetentionHours: 24,
	}
}

// Validate checks parameter ranges.
func (c RetentionConfig) Validate() error {
	if c.BundleRetentionDays < 7 {
		return fmt.Errorf("bundle_retention_days must be >= 7, got %d", c.BundleRetentionDays)
	}
	if c.ArchiveGraceDays < 1 {
		return fmt.Errorf("archive_grace_days must be >= 1, got %d", c.ArchiveGraceDays)
	}
	if c.CandidateArtifactRetentionDays < 1 {
		return fmt.Errorf("candidate_artifact_retention_days must be >= 1, got %d", c.CandidateArtifactRetentionDays)
	}
	if c.PreflightRetentionDays < 1 {
		return fmt.Errorf("preflight_retention_days must be >= 1, got %d", c.PreflightRetentionDays)
	}
	if c.OrphanPreflightRetentionDays < 1 {
		return fmt.Errorf("orphan_preflight_retention_days must be >= 1, got %d", c.OrphanPreflightRetentionDays)
	}
	if c.GCIntervalHours < 0 {
		return fmt.Errorf("gc_interval_hours must be >= 0, got %d", c.GCIntervalHours)
	}
	if c.PrepareSessionHours < 1 {
		return fmt.Errorf("prepare_session_hours must be >= 1, got %d", c.PrepareSessionHours)
	}
	if c.PrepareSessionGCIntervalMinutes < 1 {
		return fmt.Errorf("prepare_session_gc_interval_minutes must be >= 1, got %d", c.PrepareSessionGCIntervalMinutes)
	}
	if c.GCMaxDurationMinutes < 5 {
		return fmt.Errorf("gc_max_duration_minutes must be >= 5, got %d", c.GCMaxDurationMinutes)
	}
	if c.CleanupIdempotencyRetentionHours < 1 {
		return fmt.Errorf("cleanup_idempotency_retention_hours must be >= 1, got %d", c.CleanupIdempotencyRetentionHours)
	}
	return nil
}

// CleanupService implements the CleanupServiceHandler Connect interface.
type CleanupService struct {
	store  store.Store
	config RetentionConfig
	logger *slog.Logger
	audit  audit.Sink
	health *GCHealth

	mu      sync.Mutex
	active  map[string]struct{}

	gcMu          sync.Mutex
	prepareMu     sync.Mutex
	lastSuccessMu sync.RWMutex
	lastSuccess   time.Time
}

// NewCleanupService creates a new CleanupService. An optional audit sink may
// be supplied for durable GC and unarchive audit events.
func NewCleanupService(st store.Store, cfg RetentionConfig, logger *slog.Logger, sinks ...audit.Sink) *CleanupService {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.GCMaxDurationMinutes == 0 {
		cfg.GCMaxDurationMinutes = DefaultRetentionConfig().GCMaxDurationMinutes
	}
	interval := cfg.GCInterval
	if interval <= 0 && cfg.GCIntervalHours > 0 {
		interval = time.Duration(cfg.GCIntervalHours) * time.Hour
	}
	var sink audit.Sink
	if len(sinks) > 0 {
		sink = sinks[0]
	}
	return &CleanupService{
		store: st, config: cfg, logger: logger, audit: sink,
		health: NewGCHealth(interval), active: make(map[string]struct{}),
	}
}

func (s *CleanupService) Health() bool {
	return s.health == nil || s.health.Health()
}

// GCHealthSnapshot returns the current GC health state for health endpoints.
func (s *CleanupService) GCHealthSnapshot() GCHealthSnapshot {
	if s.health == nil {
		return GCHealthSnapshot{Status: GCHealthDisabled, Disabled: true, Healthy: true}
	}
	return s.health.Snapshot()
}

// UnarchiveBundle restores a validated bundle from archive.
func (s *CleanupService) UnarchiveBundle(ctx context.Context, req *connect.Request[orchestratorv1.UnarchiveBundleRequest]) (*connect.Response[orchestratorv1.UnarchiveBundleResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetBundleId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bundle_id is required"))
	}
	bundleID := req.Msg.GetBundleId()
	previousStatus, err := s.store.Bundles().Unarchive(ctx, bundleID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.emitBundleAudit(ctx, bundleID, "failed", "bundle unarchive failed", map[string]string{"reason": "not_found"})
			return nil, connect.NewError(connect.CodeNotFound, errors.New("bundle_not_found"))
		case errors.Is(err, store.ErrBundleRejected):
			s.emitBundleAudit(ctx, bundleID, "failed", "bundle unarchive failed", map[string]string{"reason": "rejected"})
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
		case errors.Is(err, store.ErrBundleNotReady):
			s.emitBundleAudit(ctx, bundleID, "failed", "bundle unarchive failed", map[string]string{"reason": "not_ready"})
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
		default:
			s.emitBundleAudit(ctx, bundleID, "failed", "bundle unarchive failed", map[string]string{"reason": "internal"})
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unarchive bundle: %w", err))
		}
	}
	s.emitBundleAudit(ctx, bundleID, "succeeded", "bundle unarchived", map[string]string{"previous_status": previousStatus})
	s.logger.Info("bundle_unarchived", "bundle_id", bundleID, "previous_status", previousStatus)
	return connect.NewResponse(&orchestratorv1.UnarchiveBundleResponse{BundleId: bundleID, PreviousStatus: previousStatus}), nil
}

func (s *CleanupService) recordSuccess() {
	if s.health != nil {
		s.health.RecordSuccess()
	}
	s.lastSuccessMu.Lock()
	s.lastSuccess = time.Now().UTC()
	s.lastSuccessMu.Unlock()
}

var _ orchestratorv1connect.CleanupServiceHandler = (*CleanupService)(nil)

// RunCleanup triggers artifact lifecycle garbage collection (AC-069-36).
func (s *CleanupService) RunCleanup(ctx context.Context, req *connect.Request[orchestratorv1.RunCleanupRequest]) (*connect.Response[orchestratorv1.RunCleanupResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cleanup request is required"))
	}
	key := req.Msg.GetIdempotencyKey()
	if key == "" || len(key) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("idempotency_key must be 1-64 characters"))
	}
	if idem := s.store.CleanupIdempotency(); idem != nil {
		retention := time.Duration(s.config.CleanupIdempotencyRetentionHours) * time.Hour
		if err := idem.TryCreate(ctx, key, retention); err != nil {
			if errors.Is(err, store.ErrCleanupAlreadyRequested) {
				return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("cleanup_already_requested"))
			}
			if !errors.Is(err, store.ErrCleanupIdempotencyUnavailable) {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reserve cleanup idempotency key: %w", err))
			}
		}
	}

	s.mu.Lock()
	if _, ok := s.active[key]; ok {
		s.mu.Unlock()
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("cleanup is already in progress"))
	}
	s.active[key] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, key)
		s.mu.Unlock()
	}()

	s.health.RecordAttempt()
	resp, errs, err := s.runGC(ctx)
	if err != nil {
		if errors.Is(err, errGCLockHeld) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("cleanup already running"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run cleanup: %w", err))
	}
	if len(errs) == 0 {
		s.recordSuccess()
	}
	s.emitGCAudit(ctx, key, resp, errs)
	return connect.NewResponse(resp), nil
}

// runPrepareSessionGC deletes expired or consumed prepare-session metadata on
// its own schedule. It does not acquire the artifact GC advisory lock.
func (s *CleanupService) runPrepareSessionGC(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(s.config.PrepareSessionHours) * time.Hour)
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	n, err := s.store.PrepareSessions().DeleteExpired(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.logger.Info("gc_prepare_sessions_deleted", "count", n)
	}
	return n, nil
}

// StartTicker runs one startup GC cycle (AC-069-40), then artifact GC and
// independent prepare-session GC on their schedules until ctx is canceled.
// A zero interval disables the artifact GC ticker entirely (AC-069-34).
func (s *CleanupService) StartTicker(ctx context.Context) {
	interval := s.config.GCInterval
	if interval <= 0 && s.config.GCIntervalHours > 0 {
		interval = time.Duration(s.config.GCIntervalHours) * time.Hour
	}
	if interval <= 0 {
		return
	}
	s.runGCWithKey(ctx, "gc-startup-"+fmt.Sprintf("%d", os.Getpid()))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	prepareInterval := time.Duration(s.config.PrepareSessionGCIntervalMinutes) * time.Minute
	prepareTicker := time.NewTicker(prepareInterval)
	defer prepareTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("gc_ticker_stopped")
			return
		case <-ticker.C:
			s.runGCWithKey(ctx, uuid.NewString())
		case <-prepareTicker.C:
			if _, err := s.runPrepareSessionGC(ctx); err != nil {
				s.logger.Warn("cleanup_prepare_phase_failed", "error", err)
			}
		}
	}
}
