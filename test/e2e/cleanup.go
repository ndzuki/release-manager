package e2e

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
// CancelOperation, RollbackRelease, and EmergencyChange (the replica restore).
// They never patch Kubernetes, write the database, or shell out.
type Recovery interface {
	ListReleaseInventory(ctx context.Context) ([]CleanupRow, error)
	CancelOperation(ctx context.Context, operationID, reason string) error
	RollbackRelease(ctx context.Context, definitionID string, targetRevision, expectedCurrentRevision int32, reason string) error
	// SetReplicas restores one workload to its baseline replica count through
	// the formal emergency-change API (AC-066-23/34). Signature is primitive
	// because this package must not import test/e2e/stages (the stage packages
	// import it), mirroring RollbackRelease.
	SetReplicas(ctx context.Context, definitionID, workloadRef string, replicas int32, reason string) error
}

// BaselineRecovery is the parsed {output-dir}/baseline.json recovery target.
//
// Revisions maps release_definition_id to the baseline Helm revision recorded
// when the run started. Replicas carries the per-workload replica counts from
// the same snapshot; when the baseline records none (an older baseline, or a
// run that never reached collection) cleanup degrades the replicas restore with
// a stderr warning and never fabricates a recovery target (AC-066-23/34).
type BaselineRecovery struct {
	Revisions map[string]int32
	// Replicas reuses the snapshot projection type: the recorded baseline and
	// the recovery target are the same fact crossing the artifact boundary, so
	// a second identical type would only invite them to drift.
	Replicas []WorkloadReplicaRef
	// Residue records the revision each definition was left at when the run
	// ended, and is what makes the rollback decision converge.
	//
	// The revision number cannot do it alone. RollbackRelease advances the
	// counter instead of restoring it -- a rollback to revision 21 was observed
	// to read back as 23, and as 24 after the next one -- so "differs from the
	// baseline" stays true forever, and cleanup rolls back again on every
	// invocation while the counter climbs. Comparing against the residue asks
	// the question that does have an answer: is the run's own residue still in
	// place? Once a rollback replaces it, the answer is no.
	//
	// The values digest is not an alternative. It is not sensitive to what a run
	// changes (all four e2e releases shared one digest while their revisions
	// moved), so it reported drifted releases as restored (D-033).
	Residue map[string]int32
}

// ResidueFromInventory projects a post-run inventory sample onto the residue
// map. Rows without a definition id or with a revision outside int32 are
// ignored, so a partial sample degrades that row to the baseline comparison
// rather than fabricating a residue.
func ResidueFromInventory(rows []InventoryRef) map[string]int32 {
	residue := make(map[string]int32, len(rows))
	for index := range rows {
		row := rows[index]
		if row.ReleaseDefinitionID == "" || row.Revision < 0 || row.Revision > math.MaxInt32 {
			continue
		}
		residue[row.ReleaseDefinitionID] = int32(row.Revision)
	}
	return residue
}

// needsRollback reports whether a definition still carries the run's residue, so
// that a rollback to the baseline would change something.
//
// It prefers the residue because that is the only signal which is both sensitive
// to what the run changed and convergent once a rollback replaces it. Without a
// residue -- a run that never sampled, or an artifact from before residues
// existed -- it falls back to the revision comparison, which still restores but
// never converges (see BaselineRecovery.Residue and D-033).
func needsRollback(baselineRevision, residueRevision, observedRevision int32) bool {
	if residueRevision > 0 {
		// The run left it exactly where it found it, so there is nothing of the
		// run's to undo. Without this the residue would equal the baseline and
		// equal the observation, triggering a rollback that restores nothing.
		if residueRevision == baselineRevision {
			return false
		}
		return observedRevision == residueRevision
	}
	return observedRevision != baselineRevision
}

// BaselineRecoveryFromSnapshots derives a recovery target from the identity
// projection embedded in baseline.json. Rows without a definition id or with a
// non-positive revision are ignored, as are replica rows without an identity or
// with a negative count.
func BaselineRecoveryFromSnapshots(snapshot FixtureSnapshot) BaselineRecovery {
	revisions := make(map[string]int32)
	for index := range snapshot.Identity.ReleaseInventories {
		row := snapshot.Identity.ReleaseInventories[index]
		if row.ReleaseDefinitionID == "" || row.Revision < 1 || row.Revision > int(^uint32(0)>>1) {
			continue
		}
		revisions[row.ReleaseDefinitionID] = int32(row.Revision)
	}
	replicas := make([]WorkloadReplicaRef, 0, len(snapshot.Identity.WorkloadReplicas))
	for index := range snapshot.Identity.WorkloadReplicas {
		row := snapshot.Identity.WorkloadReplicas[index]
		if row.ReleaseDefinitionID == "" || strings.TrimSpace(row.WorkloadRef) == "" || row.Replicas < 0 {
			continue
		}
		replicas = append(replicas, row)
	}
	sort.SliceStable(replicas, func(i, j int) bool {
		if replicas[i].ReleaseDefinitionID != replicas[j].ReleaseDefinitionID {
			return replicas[i].ReleaseDefinitionID < replicas[j].ReleaseDefinitionID
		}
		return replicas[i].WorkloadRef < replicas[j].WorkloadRef
	})
	return BaselineRecovery{Revisions: revisions, Replicas: replicas}
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
	// RestoredReplicas lists the workloads whose baseline replica count was
	// re-applied through the formal emergency API.
	RestoredReplicas []string `json:"restored_replicas"`
	// ResidualReplicas lists the workloads the outcome check read back at a
	// replica count that still differs from the baseline. Action verification (a
	// write reached a successful terminal state) is not outcome verification:
	// only a fresh read says the workload is back.
	ResidualReplicas []string `json:"residual_replicas"`
	// UnverifiedReplicas lists the workloads the outcome check could not read.
	// They are reported rather than omitted so that an unread workload is never
	// mistaken for a workload that was confirmed restored.
	UnverifiedReplicas []string `json:"unverified_replicas"`
	// ResidualRevisions lists the releases the outcome check read back still
	// carrying the run's residue, so the rollback did not take.
	ResidualRevisions []string `json:"residual_revisions"`
	// UnverifiedRevisions lists the releases the outcome check could not decide:
	// no residue was recorded for them, or the inventory read failed. Same rule as
	// the replica half -- undecided is reported, never counted as restored.
	UnverifiedRevisions []string `json:"unverified_revisions"`
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
		len(r.RestoredReplicas) != 0 ||
		len(r.ResidualReplicas) != 0 ||
		len(r.UnverifiedReplicas) != 0 ||
		len(r.ResidualRevisions) != 0 ||
		len(r.UnverifiedRevisions) != 0 ||
		len(r.ResidualNonTerminal) != 0 ||
		r.BaselineMissing
}

// MergeVerification folds an outcome check into the report the recovery pass
// produced, so the artifact stays one account of the cleanup rather than two that
// a reader has to reconcile.
func (r *CleanupReport) MergeVerification(verification CleanupReport) {
	if r == nil {
		return
	}
	r.ResidualReplicas = append(r.ResidualReplicas, verification.ResidualReplicas...)
	r.UnverifiedReplicas = append(r.UnverifiedReplicas, verification.UnverifiedReplicas...)
	r.ResidualRevisions = append(r.ResidualRevisions, verification.ResidualRevisions...)
	r.UnverifiedRevisions = append(r.UnverifiedRevisions, verification.UnverifiedRevisions...)
}

// VerifyRevisions reads the releases back and reports the ones that still carry
// the run's residue.
//
// This is the release half of "a write reaching a successful terminal state is
// not the same as the change being undone". It is only possible because the run
// sampled its own residue: a rollback advances the revision counter rather than
// restoring the number, so "differs from the baseline" is true of every release
// the run touched, forever, and would report a permanent false residual (D-033).
// "Still at the residue" is the question a rollback does answer, and it stops
// being true the moment the rollback lands.
//
// A release the run left alone, a release with no recorded residue, and a failed
// read all land in UnverifiedRevisions: none is evidence of a mismatch, and none
// is evidence of a match either.
func VerifyRevisions(ctx context.Context, baseline *BaselineRecovery, recovery Recovery, logger *slog.Logger) CleanupReport {
	report := CleanupReport{}
	if logger == nil {
		logger = slog.Default()
	}
	if baseline == nil || len(baseline.Residue) == 0 {
		return report
	}
	// Only the releases the run actually moved have something of the run's to
	// look for. Checking the others would report "still at the residue" for a
	// release that never left it.
	definitionIDs := make([]string, 0, len(baseline.Residue))
	for definitionID, residue := range baseline.Residue {
		if baselineRevision, recorded := baseline.Revisions[definitionID]; recorded && residue == baselineRevision {
			continue
		}
		definitionIDs = append(definitionIDs, definitionID)
	}
	sort.Strings(definitionIDs)
	if len(definitionIDs) == 0 {
		return report
	}

	observed, err := readObservedRevisions(ctx, recovery)
	if err != nil {
		logger.Warn("cleanup verification could not read the release inventory", "error", sanitizeError(err))
		report.UnverifiedRevisions = append(report.UnverifiedRevisions, definitionIDs...)
		return report
	}
	for _, definitionID := range definitionIDs {
		current, read := observed[definitionID]
		switch {
		case !read:
			report.UnverifiedRevisions = append(report.UnverifiedRevisions, definitionID)
		case current == baseline.Residue[definitionID]:
			report.ResidualRevisions = append(report.ResidualRevisions, definitionID)
		}
	}
	return report
}

// readObservedRevisions reads the current revision of every release, keyed by
// definition. A nil recovery is reported as an error so the caller degrades to
// unverified rather than to a silent pass.
func readObservedRevisions(ctx context.Context, recovery Recovery) (map[string]int32, error) {
	if recovery == nil {
		return nil, errors.New("no recovery implementation to read releases with")
	}
	rows, err := recovery.ListReleaseInventory(ctx)
	if err != nil {
		return nil, err
	}
	observed := make(map[string]int32, len(rows))
	for index := range rows {
		row := rows[index]
		if row.DefinitionID == "" {
			continue
		}
		observed[row.DefinitionID] = row.Revision
	}
	return observed, nil
}

// RecoveryObserver re-reads the state a recovery is supposed to have restored.
//
// Observation is a separate capability from recovery on purpose: an
// implementation that can write but not read still performs every write, and the
// outcome check reports what it could not confirm instead of assuming it. It is
// declared here, where it is consumed, so the recovery implementation does not
// have to grow a read it may not be able to serve.
type RecoveryObserver interface {
	ObserveReplicas(ctx context.Context, cluster, namespace, workloadName string) (int32, error)
}

// VerifyRestore re-reads the environment and reports every workload that still
// differs from the baseline.
//
// It is a second phase rather than a refinement of RunCleanup's own reporting,
// because the two answer different questions. RunCleanup reports which recovery
// operations it completed; a completed operation means the orchestrator accepted
// and finished the work, not that the workload now reads back at the baseline
// count. Only a fresh read says that, and without one cleanup reported
// "restored baseline replicas" while the workload kept the count the run had set
// (real smoke 2026-09-11).
//
// A nil observer, a workload row with no cluster, or a failed read all land in
// UnverifiedReplicas: none of them is evidence of a mismatch, and none of them is
// evidence of a match either.
func VerifyRestore(ctx context.Context, baseline *BaselineRecovery, observer RecoveryObserver, logger *slog.Logger) CleanupReport {
	report := CleanupReport{}
	if logger == nil {
		logger = slog.Default()
	}
	if baseline == nil || len(baseline.Replicas) == 0 {
		return report
	}
	for index := range baseline.Replicas {
		reference := baseline.Replicas[index]
		namespace, name, ok := parseWorkloadRef(reference.WorkloadRef)
		if !ok || reference.Cluster == "" {
			// Without a cluster or a parseable reference there is nothing to
			// read. A baseline written before rows carried a cluster degrades
			// here rather than being read against the wrong cluster.
			report.UnverifiedReplicas = append(report.UnverifiedReplicas, reference.WorkloadRef)
			continue
		}
		if observer == nil {
			report.UnverifiedReplicas = append(report.UnverifiedReplicas, reference.WorkloadRef)
			continue
		}
		observed, err := observer.ObserveReplicas(ctx, reference.Cluster, namespace, name)
		if err != nil {
			logger.Warn("cleanup verification could not observe a workload",
				"workload_ref", reference.WorkloadRef, "cluster", reference.Cluster, "error", err)
			report.UnverifiedReplicas = append(report.UnverifiedReplicas, reference.WorkloadRef)
			continue
		}
		if observed != reference.Replicas {
			report.ResidualReplicas = append(report.ResidualReplicas, reference.WorkloadRef)
		}
	}
	return report
}

// parseWorkloadRef splits the authoritative "<resource>/<namespace>/<name>"
// reference the emergency API accepts.
func parseWorkloadRef(reference string) (namespace, name string, ok bool) {
	parts := strings.Split(strings.TrimSpace(reference), "/")
	if len(parts) != 3 {
		return "", "", false
	}
	namespace, name = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	if namespace == "" || name == "" {
		return "", "", false
	}
	return namespace, name, true
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
//  3. Each baseline workload replica count is re-applied through the formal
//     emergency API; without a baseline replica row the restore is skipped and
//     reported (AC-066-34).
//
// It reports which recovery operations completed. Whether the environment is
// actually back at the baseline is a separate question that needs a fresh read,
// so the caller follows this with VerifyRestore and merges the result; a
// completed operation means the orchestrator finished the work, not that the
// workload reads back at the baseline count.
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

	// Replicas restore: re-apply each baseline replica count through the formal
	// emergency API. Without a recorded baseline the restore degrades with an
	// explicit warning rather than guessing a target (AC-066-23/34, D-026 D4).
	if len(baseline.Replicas) == 0 {
		report.SkippedReplicasRestore = true
		logger.Warn("cleanup replicas restore skipped: baseline.json records no workload replicas")
	}
	for index := range baseline.Replicas {
		report.restoreReplicas(ctx, baseline.Replicas[index], recovery, logger)
	}

	sort.Strings(report.CancelledOperationIDs)
	sort.Strings(report.RolledBackDefinitions)
	sort.Strings(report.SkippedRevisionRestore)
	sort.Strings(report.RestoredReplicas)
	sort.Strings(report.ResidualNonTerminal)
	return report, nil
}

// restoreReplicas re-applies one workload's baseline replica count. Cleanup
// carries no replica observer: the point of the restore is to undo whatever the
// run left behind, so the target is always written rather than compared first —
// a redundant write to the baseline value is harmless and keeps the recovery
// free of a second read that could itself fail. An entry that cannot be
// addressed is reported, never silently dropped.
func (report *CleanupReport) restoreReplicas(
	ctx context.Context,
	target WorkloadReplicaRef,
	recovery Recovery,
	logger *slog.Logger,
) {
	if strings.TrimSpace(target.ReleaseDefinitionID) == "" || strings.TrimSpace(target.WorkloadRef) == "" || target.Replicas < 0 {
		logger.Warn("cleanup replicas restore skipped: incomplete baseline entry",
			"definition_id", target.ReleaseDefinitionID, "workload_ref", target.WorkloadRef)
		report.ResidualNonTerminal = append(report.ResidualNonTerminal, target.WorkloadRef)
		return
	}
	reason := fmt.Sprintf("e2e cleanup restore baseline replicas %d", target.Replicas)
	if err := recovery.SetReplicas(ctx, target.ReleaseDefinitionID, target.WorkloadRef, target.Replicas, reason); err != nil {
		logger.Error("cleanup replicas restore failed",
			"definition_id", target.ReleaseDefinitionID, "workload_ref", target.WorkloadRef, "error", sanitizeError(err))
		report.ResidualNonTerminal = append(report.ResidualNonTerminal, target.WorkloadRef)
		return
	}
	report.RestoredReplicas = append(report.RestoredReplicas, target.WorkloadRef)
	logger.Info("cleanup restored baseline replicas",
		"definition_id", target.ReleaseDefinitionID, "workload_ref", target.WorkloadRef, "replicas", target.Replicas)
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
	case !recorded:
		report.SkippedRevisionRestore = append(report.SkippedRevisionRestore, row.DefinitionID)
		logger.Warn("cleanup revision restore skipped: no baseline revision for definition", "definition_id", row.DefinitionID)
		return
	case baselineRevision < 1:
		report.SkippedRevisionRestore = append(report.SkippedRevisionRestore, row.DefinitionID)
		logger.Warn("cleanup revision restore skipped: baseline revision invalid", "definition_id", row.DefinitionID)
		return
	}
	// The run's own residue decides, where it was sampled. The baseline revision
	// cannot: a rollback advances the counter rather than restoring it, so the
	// difference would outlive the rollback and fire again on every later
	// invocation, inflating the counter each time (D-033).
	if !needsRollback(baselineRevision, baseline.Residue[row.DefinitionID], row.Revision) {
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

// defaultCleanupRecoveryTimeout bounds the standalone `cmd/e2e cleanup` recovery
// command.
//
// It is deliberately not lifecycle.go's defaultCleanupTimeout (30s). That is the
// REQ-066 `cleanup_timeout` grace, which bounds draining a stage's own
// compensations; this budget has to span a window the run itself creates. The
// restart stage restarts the orchestrator and drops every operator command
// stream, so the agents reconnect on their own backoff — measured at ~32s in the
// real smoke — and each replica restore then needs its own apply window on top.
//
// A 30s budget expired before the emergency cluster's agent returned, so every
// restore failed however often it was retried, and cleanup reported a residual it
// could not have avoided (real smoke 2026-09-11). D-032 separates the two budgets
// and fixes this one at a value that spans the reconnect.
const defaultCleanupRecoveryTimeout = 3 * time.Minute

// CleanupDeadline returns a bounded recovery context derived from root, so
// `make e2e-all` immediately followed by `make e2e-cleanup` is a supported
// pairing rather than a race against the agent reconnect the run just caused.
func CleanupDeadline(root context.Context, cleanupTimeout time.Duration) (context.Context, context.CancelFunc) {
	if root == nil {
		root = context.Background()
	}
	if cleanupTimeout <= 0 {
		cleanupTimeout = defaultCleanupRecoveryTimeout
	}
	return context.WithTimeout(root, cleanupTimeout)
}
