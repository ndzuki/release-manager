package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/ndzuki/release-manager/internal/store"
)

// SpoolRecoverer reads a spool file and replays events into the store.
type SpoolRecoverer struct {
	store  store.AuditEventStore
	logger *slog.Logger
}

// NewSpoolRecoverer creates a spool recoverer.
func NewSpoolRecoverer(st store.AuditEventStore, logger *slog.Logger) *SpoolRecoverer {
	return &SpoolRecoverer{store: st, logger: logger}
}

// Recover reads the spool file at the given path and persists any events found.
// On success, the spool file is removed. Partial failures leave the file intact
// for the next recovery attempt.
func (r *SpoolRecoverer) Recover(ctx context.Context, spoolPath string) (int, error) {
	f, err := os.Open(spoolPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("open spool file: %w", err)
	}
	defer f.Close()

	var events []*store.AuditEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 10<<20) // 10 MB max line

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev store.AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			r.logger.Warn("skipping corrupt spool line",
				"line", lineNo,
				"error", err,
			)
			continue
		}
		events = append(events, &ev)
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan spool file: %w", err)
	}

	if len(events) == 0 {
		// Remove empty spool file.
		_ = os.Remove(spoolPath)
		return 0, nil
	}

	if err := r.store.CreateBatch(ctx, events); err != nil {
		return 0, fmt.Errorf("replay spool events: %w", err)
	}

	if err := os.Remove(spoolPath); err != nil {
		r.logger.Warn("failed to remove spool file after successful recovery",
			"path", spoolPath,
			"error", err,
		)
	}
	r.logger.Info("spool recovery complete",
		"events", len(events),
		"path", spoolPath,
	)
	return len(events), nil
}
