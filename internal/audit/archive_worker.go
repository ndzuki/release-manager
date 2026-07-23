package audit

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// ArchiveWorker runs periodic archive cycles, one at a time.
// It is safe for concurrent use: UpdateConfig atomically swaps
// the configuration snapshot so the next cycle picks it up.
type ArchiveWorker struct {
	archiver Archiver
	config   atomic.Pointer[ArchiveConfig]
	logger   *slog.Logger
}

// NewArchiveWorker creates a new ArchiveWorker.
func NewArchiveWorker(archiver Archiver, cfg ArchiveConfig, logger *slog.Logger) *ArchiveWorker {
	w := &ArchiveWorker{archiver: archiver, logger: logger}
	w.config.Store(&cfg)
	return w
}

// Run starts the periodic archive loop. It blocks until ctx is cancelled.
// If the config has retention_days=0, Run returns immediately.
func (w *ArchiveWorker) Run(ctx context.Context) {
	cfg := w.config.Load()
	if !cfg.Enabled() {
		w.logger.Info("archive worker disabled (retention_days=0)")
		return
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run once immediately on start.
	w.runOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("archive worker stopped")
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

// UpdateConfig atomically replaces the running configuration.
// The new config is validated before swapping. On validation failure,
// the old config is retained and an error is returned.
// The new config takes effect on the next archive cycle.
func (w *ArchiveWorker) UpdateConfig(cfg ArchiveConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	w.config.Store(&cfg)
	w.logger.Info("archive config updated",
		"retention_days", cfg.RetentionDays,
		"poll_interval", cfg.PollInterval,
	)
	return nil
}

func (w *ArchiveWorker) runOnce(ctx context.Context) {
	cfg := w.config.Load()
	if !cfg.Enabled() {
		return
	}

	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)

	batch, err := w.archiver.Archive(ctx, *cfg, cutoff)
	if err != nil {
		w.logger.Error("archive run failed", "error", err)
		return
	}
	if batch == nil || batch.EventCount == 0 {
		return
	}

	w.logger.Info("archive run completed",
		"cutoff", batch.Cutoff,
		"events", batch.EventCount,
		"checksum", batch.Checksum,
		"path", batch.FilePath,
	)
}
