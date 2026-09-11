package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RecoveryStatus is a durable mutation or compensation journal state.
type RecoveryStatus string

const (
	RecoveryUnknown             RecoveryStatus = "unknown"
	RecoveryPrepared            RecoveryStatus = "prepared"
	RecoveryAttempted           RecoveryStatus = "attempted"
	RecoveryApplied             RecoveryStatus = "applied"
	RecoveryNotApplied          RecoveryStatus = "not_applied"
	RecoveryAborted             RecoveryStatus = "aborted"
	RecoveryStillUnknown        RecoveryStatus = "still_unknown"
	RecoveryCompensating        RecoveryStatus = "compensating"
	RecoveryCompensated         RecoveryStatus = "compensated"
	RecoveryCompensationUnknown RecoveryStatus = "compensation_unknown"
	RecoveryDirty               RecoveryStatus = "dirty"

	// Short names keep state assertions readable for package consumers.
	Unknown             = RecoveryUnknown
	Prepared            = RecoveryPrepared
	Attempted           = RecoveryAttempted
	Applied             = RecoveryApplied
	NotApplied          = RecoveryNotApplied
	Aborted             = RecoveryAborted
	StillUnknown        = RecoveryStillUnknown
	Compensating        = RecoveryCompensating
	Compensated         = RecoveryCompensated
	CompensationUnknown = RecoveryCompensationUnknown
	Dirty               = RecoveryDirty
)

var (
	ErrLedgerNotFound     = errors.New("e2e: recovery entry not found")
	ErrLedgerCorrupt      = errors.New("e2e: recovery ledger corrupt")
	ErrLedgerTransition   = errors.New("e2e: invalid recovery transition")
	ErrLedgerDirty        = errors.New("e2e: recovery ledger dirty")
	ErrInvalidLedgerEntry = errors.New("e2e: invalid recovery entry")
)

// LedgerEntry is the durable evidence needed to reconcile a mutation.
type LedgerEntry struct {
	RunID          string `json:"run_id"`
	Seq            int64  `json:"seq"`
	CompensationID string `json:"compensation_id,omitempty"`
	DriverVersion  string `json:"driver_version,omitempty"`
	OwnerFence     string `json:"owner_fence,omitempty"`
	// IdempotencyKey is durable recovery metadata. Public run and snapshot
	// artifacts do not serialize LedgerEntry values.
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	OperationID    string         `json:"operation_id,omitempty"`
	PreVersion     string         `json:"pre_version,omitempty"`
	PostVersion    string         `json:"post_version,omitempty"`
	Attempt        int            `json:"attempt"`
	Deadline       time.Time      `json:"deadline,omitempty"`
	Status         RecoveryStatus `json:"status"`
	Error          string         `json:"error,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// RecoveryLedger is a durable, atomic JSON journal.
type RecoveryLedger struct {
	mu      sync.Mutex
	path    string
	entries []LedgerEntry
}

// NewRecoveryLedger opens path, or creates an empty in-memory ledger which is
// persisted on the first mutation.
func NewRecoveryLedger(path string) (*RecoveryLedger, error) {
	if path == "" {
		return nil, fmt.Errorf("open recovery ledger: empty path")
	}
	ledger := &RecoveryLedger{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read recovery ledger: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("read recovery ledger: %w", ErrLedgerCorrupt)
	}
	if err := json.Unmarshal(data, &ledger.entries); err != nil {
		return nil, fmt.Errorf("decode recovery ledger: %w", ErrLedgerCorrupt)
	}
	for index := range ledger.entries {
		if ledger.entries[index].Status == "" || ledger.entries[index].RunID == "" {
			return nil, fmt.Errorf("decode recovery ledger: %w", ErrLedgerCorrupt)
		}
	}
	return ledger, nil
}

// OpenRecoveryLedger is an explicit alias for NewRecoveryLedger.
func OpenRecoveryLedger(path string) (*RecoveryLedger, error) { return NewRecoveryLedger(path) }

// Path returns the ledger path.
func (l *RecoveryLedger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Entries returns a defensive snapshot of all journal entries.
func (l *RecoveryLedger) Entries() []LedgerEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneEntries(l.entries)
}

// Entry returns a defensive snapshot for id, where id is operation id or
// compensation id. RunID and Seq together are also accepted as an identity.
func (l *RecoveryLedger) Entry(id string) (LedgerEntry, error) {
	if l == nil {
		return LedgerEntry{}, ErrLedgerNotFound
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for index := range l.entries {
		if l.entries[index].OperationID == id || l.entries[index].CompensationID == id {
			return l.entries[index], nil
		}
	}
	return LedgerEntry{}, ErrLedgerNotFound
}

// Prepare durably records a new mutation before any external write.
func (l *RecoveryLedger) Prepare(entry LedgerEntry) error {
	if l == nil {
		return ErrInvalidLedgerEntry
	}
	if entry.Status != "" && entry.Status != RecoveryPrepared {
		return fmt.Errorf("prepare recovery entry: %w", ErrInvalidLedgerEntry)
	}
	if entry.RunID == "" || (entry.OperationID == "" && entry.CompensationID == "") {
		return fmt.Errorf("prepare recovery entry: %w", ErrInvalidLedgerEntry)
	}
	entry.Status = RecoveryPrepared
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}
	return l.append(entry)
}

// Append is an alias for Prepare and exists for callers that build entries.
func (l *RecoveryLedger) Append(entry LedgerEntry) error { return l.Prepare(entry) }

// MarkAttempted persists the pre-commit write marker.
func (l *RecoveryLedger) MarkAttempted(id string) error { return l.Transition(id, RecoveryAttempted) }

// MarkApplied records a successful mutation.
func (l *RecoveryLedger) MarkApplied(id string) error { return l.Transition(id, RecoveryApplied) }

// MarkNotApplied records a reconciled mutation that did not take effect.
func (l *RecoveryLedger) MarkNotApplied(id string) error { return l.Transition(id, RecoveryNotApplied) }

// MarkAborted records a mutation abandoned before it could be applied.
func (l *RecoveryLedger) MarkAborted(id string) error { return l.Transition(id, RecoveryAborted) }

// MarkUnknown records an outcome that requires reconciliation.
func (l *RecoveryLedger) MarkUnknown(id string) error { return l.Transition(id, RecoveryUnknown) }

// Reconcile updates Unknown to a concrete or still-unknown observation.
func (l *RecoveryLedger) Reconcile(id string, status RecoveryStatus) error {
	if status != RecoveryApplied && status != RecoveryNotApplied && status != RecoveryStillUnknown {
		return fmt.Errorf("reconcile recovery entry: %w", ErrLedgerTransition)
	}
	return l.Transition(id, status)
}

// BeginCompensation marks an applied mutation for cleanup.
func (l *RecoveryLedger) BeginCompensation(id string) error {
	return l.Transition(id, RecoveryCompensating)
}

// MarkCompensated records a successful compensation.
func (l *RecoveryLedger) MarkCompensated(id string) error {
	return l.Transition(id, RecoveryCompensated)
}

// MarkCompensationUnknown records a compensation whose result is uncertain.
func (l *RecoveryLedger) MarkCompensationUnknown(id string) error {
	return l.Transition(id, RecoveryCompensationUnknown)
}

// MarkDirty fences the ledger against a future run until manually reconciled.
func (l *RecoveryLedger) MarkDirty(id string) error { return l.Transition(id, RecoveryDirty) }

// Transition durably applies one validated state transition.
func (l *RecoveryLedger) Transition(id string, next RecoveryStatus) error {
	if l == nil {
		return ErrLedgerNotFound
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	index := -1
	for i := range l.entries {
		if l.entries[i].OperationID == id || l.entries[i].CompensationID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("transition recovery entry: %w", ErrLedgerNotFound)
	}
	current := l.entries[index].Status
	if !validTransition(current, next) {
		return fmt.Errorf("transition recovery entry %s to %s: %w", current, next, ErrLedgerTransition)
	}
	updated := cloneEntries(l.entries)
	updated[index].Status = next
	updated[index].UpdatedAt = time.Now().UTC()
	if err := l.persist(updated); err != nil {
		return fmt.Errorf("persist recovery transition: %w", err)
	}
	l.entries = updated
	return nil
}

func (l *RecoveryLedger) append(entry LedgerEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for index := range l.entries {
		if entry.OperationID != "" && l.entries[index].OperationID == entry.OperationID {
			return fmt.Errorf("append recovery entry: %w", ErrInvalidLedgerEntry)
		}
		if entry.CompensationID != "" && l.entries[index].CompensationID == entry.CompensationID {
			return fmt.Errorf("append recovery entry: %w", ErrInvalidLedgerEntry)
		}
	}
	updated := append(cloneEntries(l.entries), entry)
	if err := l.persist(updated); err != nil {
		return fmt.Errorf("persist recovery entry: %w", err)
	}
	l.entries = updated
	return nil
}

func validTransition(from, to RecoveryStatus) bool {
	if to == RecoveryDirty {
		return from != "" && from != RecoveryDirty
	}
	switch from {
	case RecoveryPrepared:
		return to == RecoveryAttempted || to == RecoveryAborted
	case RecoveryAttempted:
		return to == RecoveryApplied || to == RecoveryNotApplied || to == RecoveryUnknown || to == RecoveryAborted
	case RecoveryUnknown:
		return to == RecoveryUnknown || to == RecoveryApplied || to == RecoveryNotApplied || to == RecoveryStillUnknown || to == RecoveryDirty
	case RecoveryStillUnknown:
		return to == RecoveryStillUnknown || to == RecoveryApplied || to == RecoveryNotApplied || to == RecoveryDirty
	case RecoveryApplied:
		return to == RecoveryCompensating || to == RecoveryDirty
	case RecoveryCompensating:
		return to == RecoveryCompensated || to == RecoveryCompensationUnknown || to == RecoveryDirty
	case RecoveryCompensationUnknown:
		return to == RecoveryCompensating || to == RecoveryCompensated || to == RecoveryDirty
	case RecoveryNotApplied, RecoveryAborted, RecoveryCompensated:
		return to == RecoveryDirty
	case RecoveryDirty:
		return false
	default:
		return false
	}
}

func (l *RecoveryLedger) persist(entries []LedgerEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".recovery-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func cloneEntries(entries []LedgerEntry) []LedgerEntry {
	return append([]LedgerEntry(nil), entries...)
}
