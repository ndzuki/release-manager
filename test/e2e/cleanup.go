package e2e

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// RunnerUsername is the REQ-065 development account used for all E2E writes.
// Cleanup treats operations whose actor matches either the authenticated
// runner user id or this literal name as owned by the E2E runner.
const RunnerUsername = "e2e-runner"

// ErrCleanupRecovery reports that cleanup could not converge all residuals.
var ErrCleanupRecovery = errors.New("cleanup recovery incomplete")

// CleanupOperation is the safe projection of a non-terminal operation that
// cleanup must decide on (REQ-066 row.active_operation).
type CleanupOperation struct {
	OperationID   string
	OperationType string
	State         string
	Actor         string
}

// CleanupRow is the safe projection of one ListReleaseInventory row.
type CleanupRow struct {
	CustomerID   string
	ClusterID    string
	DefinitionID string
	Namespace    string
	ReleaseName  string
	Revision     int32
	Active       *CleanupOperation
}

// Recovery is the formal-API seam used by RunCleanup (AC-066-38/34/07/23/28).
//
// Implementations authenticate as e2e-runner and perform only formal writes:
// CancelOperation, RollbackRelease, and (in a future revision once baseline
// collection records replicas) EmergencyChange. They never patch Kubernetes,
// write the database, or shell out.
type Recovery interface {
	ListReleaseInventory(ctx context.Context) ([]CleanupRow, error)
	CancelOperation(ctx context.Context, operationID, reason string) error
	RollbackRelease(ctx context.Context, definitionID string, targetRevision, expectedCurrentRevision int32, reason string) error
}

// BaselineRecovery is the parsed {output-dir}/baseline.json recovery target.
//
// Revisions maps release_definition_id to the baseline Helm revision recorded
// when the run started. Replicas are intentionally absent until the run-side
// baseline collection records per-definition replicas; until then cleanup
// degrades the replicas restore with a stderr warning and never fabricates a
// recovery target (AC-066-23/34).
type BaselineRecovery struct {
	Revisions map[string]int32
}

// BaselineRecoveryFromSnapshots derives a recovery target from the identity
// projection embedded in baseline.json. Rows without a definition id or with a
// non-positive revision are ignored.
func BaselineRecoveryFromSnapshots(snapshot FixtureSnapshot) BaselineRecovery {
	revisions := make(map[string]int32)
	for index := range snapshot.Identity.ReleaseInventories {
		row := snapshot.Identity.ReleaseInventories[index]
		if row.ReleaseDefinitionID == "" || row.Revision < 1 || row.Revision > int(^uint32(0)>>1) {
			continue
		}
		revisions[row.ReleaseDefinitionID] = int32(row.Revision)
	}
	return BaselineRecovery{Revisions: revisions}
}

// CleanupReport is the machine-readable cleanup outcome.
type CleanupReport struct {
	CancelledOperationIDs []string `json:"cancelled_operation_ids"`
	RolledBackDefinitions []string `json:"rolled_back_definition_ids"`
	// SkippedRevisionRestore lists definitions whose revision differs from the
	// baseline but no baseline revision was available.
	SkippedRevisionRestore []string `json:"skipped_revision_restore"`
	// SkippedReplicasRestore records the residual-only degradation when the
	// baseline carries no replicas (AC-066-23/34).
	SkippedReplicasRestore bool `json:"skipped_replicas_restore"`
	// ResidualNonTerminal lists runner-owned non-terminal operations that could
	// not be cancelled (cancel failed or did not reach a terminal state).
	ResidualNonTerminal []string `json:"residual_nonterminal_remaining"`
	// BaselineMissing records the baseline.json degradation path.
	BaselineMissing bool `json:"baseline_missing"`
}

// NonZero reports whether any recovery action was attempted or skipped.
func (r CleanupReport) NonZero() bool {
	return len(r.CancelledOperationIDs) != 0 ||
		len(r.RolledBackDefinitions) != 0 ||
		len(r.SkippedRevisionRestore) != 0 ||
		r.SkippedReplicasRestore ||
		len(r.ResidualNonTerminal) != 0 ||
		r.BaselineMissing
}

// IsRunnerOwned reports whether an operation actor belongs to the E2E runner.
// ListReleaseInventory exposes the operation actor as the user id
// (internal/orchestrator/inventory_observation.go); the literal runner name is
// accepted defensively for environments that encode a stable principal name.
func IsRunnerOwned(actor, runnerUserID string) bool {
	switch strings.TrimSpace(actor) {
	case "", RunnerUsername, runnerUserID:
		return true
	}
	return false
}

// RunCleanup recovers E2E resources through the formal API surface only.
//
// It performs a single pass:
//  1. Unfiltered ListReleaseInventory discovers every e2e-* release row whose
//     active operation is owned by the E2E runner and non-terminal; each is
//     cancelled through CancelOperation (AC-066-07/22).
//  2. For each row whose current revision differs from the recorded baseline,
//     RollbackRelease restores the baseline revision when a recovery target
//     exists; without a baseline revision the restore is skipped and reported
//     (never guessed), matching the baseline.json degradation contract
//     (AC-066-28, D-026 D4).
//  3. Replicas restore degrades with a warning until baseline collection
//     records replicas (AC-066-34).
//
// A nil baseline is treated as missing and triggers the residual-only path.
// The context should already carry the caller's cleanup deadline.
func RunCleanup(ctx context.Context, baseline *BaselineRecovery, runnerUserID string, recovery Recovery, logger *slog.Logger) (CleanupReport, error) {
	report := CleanupReport{}
	if logger == nil {
		logger = slog.Default()
	}
	if baseline == nil {
		report.BaselineMissing = true
		logger.Warn("baseline.json missing; cleanup is residual-only (revision/replicas not restored)")
		baseline = &BaselineRecovery{Revisions: make(map[string]int32)}
	}
	if baseline.Revisions == nil {
		baseline.Revisions = make(map[string]int32)
	}

	rows, err := recovery.ListReleaseInventory(ctx)
	if err != nil {
		return report, fmt.Errorf("%w: list release inventory: %v", ErrCleanupRecovery, sanitizeError(err))
	}
	sortCleanupRows(rows)

	for index := range rows {
		row := rows[index]
		report.recoverRow(ctx, row, baseline, runnerUserID, recovery, logger)
	}

	// Replicas restore: degrade with an explicit warning until the run-side
	// baseline collection records replicas (AC-066-23/34, D-026 D4).
	report.SkippedReplicasRestore = true
	logger.Warn("cleanup replicas restore skipped: baseline.json does not record replicas yet")

	sort.Strings(report.CancelledOperationIDs)
	sort.Strings(report.RolledBackDefinitions)
	sort.Strings(report.SkippedRevisionRestore)
	sort.Strings(report.ResidualNonTerminal)
	return report, nil
}

// recoverRow applies the single-pass recovery decisions to one inventory row:
// cancel a runner-owned non-terminal active operation, then restore the
// baseline revision when the row drifted from it (AC-066-07/22/28).
func (report *CleanupReport) recoverRow(
	ctx context.Context,
	row CleanupRow,
	baseline *BaselineRecovery,
	runnerUserID string,
	recovery Recovery,
	logger *slog.Logger,
) {
	if row.Active != nil && IsRunnerOwned(row.Active.Actor, runnerUserID) && !terminalState(row.Active.State) {
		if err := recovery.CancelOperation(ctx, row.Active.OperationID, "e2e cleanup residual operation"); err != nil {
			logger.Error("cleanup cancel failed", "operation_id", row.Active.OperationID, "error", sanitizeError(err))
			report.ResidualNonTerminal = append(report.ResidualNonTerminal, row.Active.OperationID)
			return
		}
		report.CancelledOperationIDs = append(report.CancelledOperationIDs, row.Active.OperationID)
		logger.Info("cleanup cancelled residual operation", "operation_id", row.Active.OperationID, "definition_id", row.DefinitionID)
	}

	if row.DefinitionID == "" || row.Revision < 1 {
		return
	}
	baselineRevision, recorded := baseline.Revisions[row.DefinitionID]
	switch {
	case recorded && baselineRevision == row.Revision:
		return
	case !recorded:
		report.SkippedRevisionRestore = append(report.SkippedRevisionRestore, row.DefinitionID)
		logger.Warn("cleanup revision restore skipped: no baseline revision for definition", "definition_id", row.DefinitionID)
		return
	case baselineRevision < 1:
		report.SkippedRevisionRestore = append(report.SkippedRevisionRestore, row.DefinitionID)
		logger.Warn("cleanup revision restore skipped: baseline revision invalid", "definition_id", row.DefinitionID)
		return
	}
	reason := fmt.Sprintf("e2e cleanup rollback to baseline revision %d", baselineRevision)
	if err := recovery.RollbackRelease(ctx, row.DefinitionID, baselineRevision, row.Revision, reason); err != nil {
		logger.Error("cleanup rollback failed", "definition_id", row.DefinitionID, "error", sanitizeError(err))
		report.ResidualNonTerminal = append(report.ResidualNonTerminal, row.DefinitionID)
		return
	}
	report.RolledBackDefinitions = append(report.RolledBackDefinitions, row.DefinitionID)
	logger.Info("cleanup rollback to baseline", "definition_id", row.DefinitionID, "target_revision", baselineRevision)
}

// sortCleanupRows orders rows deterministically so reports are stable.
func sortCleanupRows(rows []CleanupRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		for _, pair := range [][2]string{
			{left.CustomerID, right.CustomerID},
			{left.ClusterID, right.ClusterID},
			{left.DefinitionID, right.DefinitionID},
			{left.ReleaseName, right.ReleaseName},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
}

func terminalState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "OPERATION_STATUS_SUCCEEDED",
		"OPERATION_STATUS_FAILED",
		"OPERATION_STATUS_CANCELLED",
		"OPERATION_STATUS_TIMEOUT",
		"SUCCEEDED", "FAILED", "CANCELLED", "TIMEOUT":
		return true
	}
	return false
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 256 || strings.ContainsAny(message, "\r\n\t") {
		return "cleanup operation failed"
	}
	for _, token := range []string{"password", "secret", "token", "jwt", "authorization", "postgres://", "postgresql://", "mysql://"} {
		if strings.Contains(strings.ToLower(message), token) {
			return "cleanup operation failed"
		}
	}
	return message
}

// CleanupDeadline returns a bounded cleanup context derived from root. The
// cleanup grace is capped by the contract's 30s cleanup timeout (REQ-066).
func CleanupDeadline(root context.Context, cleanupTimeout time.Duration) (context.Context, context.CancelFunc) {
	if root == nil {
		root = context.Background()
	}
	if cleanupTimeout <= 0 {
		cleanupTimeout = 30 * time.Second
	}
	return context.WithTimeout(root, cleanupTimeout)
}
