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
	defer func() { _ = f.Close() }()

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
			r.logger.Warn("corrupt audit spool line retained", "line", lineNo, "error", err)
			return 0, fmt.Errorf("decode spool line %d: %w", lineNo, err)
		}
		normalized, err := Normalize(&ev)
		if err != nil {
			return 0, fmt.Errorf("normalize spool line %d: %w", lineNo, err)
		}
		events = append(events, normalized)
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan spool file: %w", err)
	}

	if len(events) == 0 {
		if err := os.Remove(spoolPath); err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("remove empty spool file: %w", err)
		}
		return 0, nil
	}

	if err := r.store.CreateBatch(ctx, events); err != nil {
		return 0, fmt.Errorf("replay spool events: %w", err)
	}

	if err := os.Remove(spoolPath); err != nil {
		return len(events), fmt.Errorf("remove recovered spool file: %w", err)
	}
	r.logger.Info("spool recovery complete",
		"events", len(events),
		"path", spoolPath,
	)
	return len(events), nil
}
