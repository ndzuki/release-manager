// Command e2e is the single entry point for the staged E2E runner.
//
// The run command is also the default command. It deliberately keeps machine
// readable output in --output-dir; stdout is reserved for a human summary and
// slog diagnostics are written to stderr.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/livewire"
)

const (
	exitSuccess exitCode = iota
	exitRuntime
	exitUsage
)

const exitLock exitCode = 3

// baselineReplicaTimeout bounds the pre-run replica sample. It is deliberately
// short: the sample is a recovery aid, and a slow environment must not delay the
// run's own start beyond the point where cleanup could still be meaningful.
const baselineReplicaTimeout = 30 * time.Second

type exitCode int

type runOptions struct {
	stages       string
	timeout      time.Duration
	totalTimeout time.Duration
	outputDir    string
	parallel     bool
	keepFailure  bool
	snapshotFull bool
	envConfig    string
}

type cleanupOptions struct {
	envConfig    string
	outputDir    string
	baselineFile string
}

type baselineArtifact struct {
	e2e.FixtureSnapshot
	RunID          string `json:"run_id"`
	Environment    string `json:"environment"`
	EnvironmentID  string `json:"environment_id"`
	FixtureVersion string `json:"fixture_version"`
	SnapshotFull   bool   `json:"snapshot_full"`
}

// residueFileName is the post-run sample's artifact name. It sits beside
// baseline.json because both describe the same run.
const residueFileName = "residue.json"

// residueArtifact is what the run left behind, sampled once the stages have run.
//
// It exists because the baseline revision cannot tell cleanup whether a release
// still holds the run's change: RollbackRelease advances the counter instead of
// restoring it, so a release that was rolled back once still differs from the
// baseline and would be rolled back again on every later invocation (D-033).
// The residue is the missing half of that comparison, and it is only
// trustworthy because it was sampled while the state was known to be the run's
// own.
type residueArtifact struct {
	RunID       string           `json:"run_id"`
	Environment string           `json:"environment,omitempty"`
	Residue     map[string]int32 `json:"residue"`
	CollectedAt time.Time        `json:"collected_at"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the process-level CLI seam. It returns an exit status rather than
// calling os.Exit so black-box command tests can exercise all output channels.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "cleanup" {
		return runCleanup(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	return runStages(args, stdout, stderr)
}

// parseRunArgs parses and validates the run flags, returning a usage error for
// the caller to report. parseRunOptions owns any error it already printed.
func parseRunArgs(args []string, stderr io.Writer) (runOptions, []string, error) {
	options, err := parseRunOptions(args, stderr)
	if err != nil {
		return runOptions{}, nil, err
	}
	if options.envConfig == "" {
		return runOptions{}, nil, errors.New("--env-config is required")
	}
	selected, err := parseStages(options.stages)
	if err != nil {
		return runOptions{}, nil, err
	}
	if options.timeout <= 0 || options.totalTimeout <= 0 {
		return runOptions{}, nil, errors.New("--timeout and --total-timeout must be positive")
	}
	return options, selected, nil
}

func runStages(args []string, stdout, stderr io.Writer) int {
	options, selected, err := parseRunArgs(args, stderr)
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}

	config, err := e2e.LoadConfig(options.envConfig)
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	runID, err := runID()
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	lock, err := acquireOptionalLock(runID)
	if err != nil {
		return reportLockError(stderr, err)
	}
	if lock != nil {
		defer func() {
			if releaseErr := lock.Release(); releaseErr != nil {
				// The run result has already been persisted; this is a diagnostic
				// only because changing the already selected process status here
				// would make successful runs non-deterministic.
				slog.New(slog.NewTextHandler(stderr, nil)).Error("release e2e lock", "error", releaseErr)
			}
		}()
	}
	if err := prepareOutputDir(options.outputDir, selected); err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("e2e run started", "run_id", runID, "stages", strings.Join(selected, ","))

	// The live read/write stage implementations live in test/e2e/livewire. The
	// canonical graph is assembled before any artifact is written, so a graph
	// that cannot be assembled is a fail-closed runtime error (exit 1) that
	// leaves no baseline or stage artifact behind, rather than a run that
	// silently reports a vacuous pass. Assembling the whole graph means a run
	// needs a usable config even for a partial stage selection; that is
	// deliberate, because the graph is the single definition of the canonical
	// stage set and its dependency edges (TASK-066 fail-closed contract).
	stageSpecs, err := livewire.SpecsForRun(config, runID)
	if err != nil {
		logger.Error("assemble e2e stage graph", "error", err)
		return int(exitRuntime)
	}

	baseline, baselineDigest, ok := collectBaseline(config, options, runID, logger)
	if !ok {
		return int(exitRuntime)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The canonical graph assembled above supplies every stage body, so no
	// selected stage can report a vacuous pass: a real adapter that cannot
	// observe a healthy dependency fails the stage, and `make e2e-*` and CI go
	// red (exit 1) instead of fabricating a green run (TASK-066 Step 8
	// fail-closed contract).
	report, runErr := runHarness(ctx, config, options, selected, stageSpecs, logger)

	// Sample the residue the moment the run stops, whatever it decided. This is
	// the only moment the changed state is known to be the run's own, and it is
	// what lets a later cleanup tell the run's residue from its own earlier
	// rollback. A failed sample is logged, not fatal: cleanup then degrades to
	// the baseline revision comparison, which still restores (D-033).
	collectResidue(config, options, runID, logger)

	stageArtifacts, ok := writeStageArtifacts(report, options, runID, logger)
	if !ok {
		return int(exitRuntime)
	}

	return writeRunArtifact(stdout, logger, runOutcome{
		options:        options,
		runID:          runID,
		selected:       selected,
		baseline:       baseline,
		baselineDigest: baselineDigest,
		report:         report,
		stageArtifacts: stageArtifacts,
		runErr:         runErr,
	})
}

// collectBaseline samples the pre-run recovery target — each release's current
// revision and each emergency workload's replica count — writes baseline.json,
// and returns the artifact with its stable digest. The bool reports whether the
// caller may continue; a false value has already been logged.
//
// Both samples are recovery aids: a failed sample is recorded and the run
// continues, because the corresponding "baseline carries none" degradation is a
// documented contract (cleanup reports it as skipped_revision_restore or
// skipped_replicas_restore) and losing a recovery aid must not fail a run whose
// stages could still succeed.
func collectBaseline(config *e2e.Config, options runOptions, runID string, logger *slog.Logger) (baselineArtifact, string, bool) {
	baseline := baselineArtifact{
		RunID:          runID,
		Environment:    config.Environment,
		EnvironmentID:  config.EnvironmentID,
		FixtureVersion: config.Seed.FixtureVersion,
		SnapshotFull:   options.snapshotFull,
	}
	replicaBaselineCtx, cancelReplicaBaseline := context.WithTimeout(context.Background(), baselineReplicaTimeout)
	replicas, replicaErr := livewire.BaselineReplicas(replicaBaselineCtx, config)
	cancelReplicaBaseline()
	if replicaErr != nil {
		logger.Warn("baseline replica collection failed; cleanup replicas restore will degrade", "error", safeErrorMessage(replicaErr))
	}
	baseline.Identity.WorkloadReplicas = replicas
	// The revision sample is the other half of the recovery target: cleanup rolls
	// a definition back to the revision recorded here, so a baseline without it
	// can only skip that restore.
	revisionBaselineCtx, cancelRevisionBaseline := context.WithTimeout(context.Background(), baselineReplicaTimeout)
	revisions, revisionErr := livewire.BaselineRevisions(revisionBaselineCtx, config)
	cancelRevisionBaseline()
	if revisionErr != nil {
		logger.Warn("baseline revision collection failed; cleanup revision restore will degrade", "error", safeErrorMessage(revisionErr))
	}
	baseline.Identity.ReleaseInventories = revisions
	// The embedded snapshot time is assigned after the literal because go1.26
	// disallows promoted fields in a composite literal of the outer type.
	baseline.CollectedAt = time.Now().UTC()
	baselineDigest, err := e2e.StableDigest(baseline)
	if err != nil {
		logger.Error("collect baseline", "error", err)
		return baseline, "", false
	}
	if err := e2e.WriteJSONAtomic(filepath.Join(options.outputDir, "baseline.json"), baseline); err != nil {
		logger.Error("write baseline artifact", "error", err)
		return baseline, "", false
	}
	return baseline, baselineDigest, true
}

// collectResidue samples each release's revision after the run and writes
// residue.json.
//
// It reuses the baseline read path, because the baseline and the residue are the
// same observation taken at two moments; a second projection would only let them
// drift apart. Nothing fails here: a run whose stages already decided its exit
// code must not be turned red by a recovery aid it could do without.
func collectResidue(config *e2e.Config, options runOptions, runID string, logger *slog.Logger) {
	residueCtx, cancelResidue := context.WithTimeout(context.Background(), baselineReplicaTimeout)
	rows, err := livewire.BaselineRevisions(residueCtx, config)
	cancelResidue()
	if err != nil {
		logger.Warn("post-run residue collection failed; cleanup rollback degrades to the baseline revision comparison",
			"error", safeErrorMessage(err))
		return
	}
	residue := residueArtifact{
		RunID:       runID,
		Environment: config.Environment,
		Residue:     e2e.ResidueFromInventory(rows),
	}
	residue.CollectedAt = time.Now().UTC()
	if err := e2e.WriteJSONAtomic(filepath.Join(options.outputDir, residueFileName), residue); err != nil {
		logger.Warn("write residue artifact failed; cleanup rollback degrades to the baseline revision comparison", "error", err)
	}
}

// loadCleanupResidue parses the post-run sample of what the run left behind.
//
// Without it cleanup still restores, but it cannot tell "the run moved this" from
// "an earlier cleanup already put it back", so it rolls back again on every
// invocation and advances the revision counter each time (D-033).
//
// A residue naming a different run is refused: it describes state this cleanup
// was never asked about, and comparing against it would let a stale artifact
// decide whether to roll a release back.
func loadCleanupResidue(residueFile, runID string, logger *slog.Logger) map[string]int32 {
	residueData, readErr := os.ReadFile(residueFile)
	switch {
	case readErr != nil && !errors.Is(readErr, os.ErrNotExist):
		logger.Warn("residue unreadable; cleanup rollback degrades to the baseline revision comparison", "error", readErr)
		return nil
	case readErr != nil:
		return nil
	}
	var residue residueArtifact
	if err := json.Unmarshal(residueData, &residue); err != nil {
		logger.Warn("residue invalid; cleanup rollback degrades to the baseline revision comparison", "error", err)
		return nil
	}
	if residue.RunID != "" && runID != "" && residue.RunID != runID {
		logger.Warn("residue belongs to a different run; ignoring it",
			"residue_run_id", residue.RunID, "baseline_run_id", runID)
		return nil
	}
	return residue.Residue
}

// runHarness executes the canonical graph and normalizes the reported exit code
// to a value the CLI contract allows.
func runHarness(
	ctx context.Context,
	config *e2e.Config,
	options runOptions,
	selected []string,
	stageSpecs []e2e.StageSpec,
	logger *slog.Logger,
) (e2e.Report, error) {
	report, runErr := e2e.New(*config).Run(ctx, e2e.Scenario{
		SelectedStages: selected,
		Stages:         stageSpecs,
		StageTimeout:   options.timeout,
		TotalTimeout:   options.totalTimeout,
		Parallel:       options.parallel,
	})
	if runErr != nil {
		logger.Error("e2e run failed", "error", runErr)
		if report.ExitCode == 0 {
			report.ExitCode = int(exitRuntime)
		}
	}
	if report.ExitCode < 0 || report.ExitCode > int(exitUsage) {
		report.ExitCode = int(exitRuntime)
	}
	return report, runErr
}

// writeStageArtifacts persists one artifact per stage result and returns the
// index entries run.json carries. The bool reports whether writing succeeded.
func writeStageArtifacts(
	report e2e.Report,
	options runOptions,
	runID string,
	logger *slog.Logger,
) ([]e2e.StageArtifact, bool) {
	stageArtifacts := make([]e2e.StageArtifact, 0, len(report.Results))
	for index := range report.Results {
		stage := report.Results[index].Stage
		if stage == "" {
			stage = report.Results[index].Name
		}
		if err := e2e.WriteJSONAtomic(filepath.Join(options.outputDir, stage+".json"), report.Results[index]); err != nil {
			logger.Error("write stage artifact", "stage", stage, "error", err)
			return nil, false
		}
		stageArtifacts = append(stageArtifacts, e2e.StageArtifact{
			Stage:  stage,
			Status: report.Results[index].Status,
			File:   stage + ".json",
		})
		if report.Results[index].Status != e2e.StageFail {
			continue
		}
		logger.Warn("e2e stage failed", "stage", stage, "root_cause", report.Results[index].RootCause)
		if !options.keepFailure {
			continue
		}
		if err := writeDiagnostic(options.outputDir, runID, report.Results[index]); err != nil {
			logger.Error("write failure diagnostic", "stage", stage, "error", err)
			return nil, false
		}
	}
	return stageArtifacts, true
}

// runOutcome carries everything the final run artifact needs.
type runOutcome struct {
	options        runOptions
	runID          string
	selected       []string
	baseline       baselineArtifact
	baselineDigest string
	report         e2e.Report
	stageArtifacts []e2e.StageArtifact
	runErr         error
}

// writeRunArtifact persists run.json, prints the summary, and returns the
// process exit status.
func writeRunArtifact(stdout io.Writer, logger *slog.Logger, outcome runOutcome) int {
	report := outcome.report
	runArtifact := e2e.RunArtifact{
		RunID:          outcome.runID,
		SelectedStages: append([]string(nil), outcome.selected...),
		BaselineDigest: outcome.baselineDigest,
		StartedAt:      startedAt(report, outcome.baseline.CollectedAt),
		FinishedAt:     time.Now().UTC(),
		Pass:           report.Passed,
		Fail:           report.Failed,
		Skip:           report.Skipped,
		ExitCode:       report.ExitCode,
		Stages:         outcome.stageArtifacts,
	}
	if outcome.runErr != nil {
		runArtifact.Fatal = &e2e.ErrorCause{
			Code:      "e2e_run_failed",
			Component: "harness",
			Message:   safeErrorMessage(outcome.runErr),
		}
	}
	if err := e2e.WriteJSONAtomic(filepath.Join(outcome.options.outputDir, "run.json"), runArtifact); err != nil {
		logger.Error("write run artifact", "error", err)
		return int(exitRuntime)
	}

	writeSummary(stdout, runArtifact, report.Results)
	logger.Info("e2e run finished", "run_id", outcome.runID, "exit_code", report.ExitCode)
	return report.ExitCode
}

func runCleanup(args []string, stdout, stderr io.Writer) int {
	options, err := parseCleanupOptions(args, stderr)
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	if options.envConfig == "" {
		writeUsageError(stderr, errors.New("--env-config is required"))
		return int(exitUsage)
	}
	if strings.TrimSpace(options.outputDir) == "" {
		writeUsageError(stderr, errors.New("--output-dir must not be empty"))
		return int(exitUsage)
	}
	cleanupID, err := runID()
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	lock, err := acquireOptionalLock("cleanup-" + cleanupID)
	if err != nil {
		return reportLockError(stderr, err)
	}
	if lock != nil {
		defer func() {
			if releaseErr := lock.Release(); releaseErr != nil {
				slog.New(slog.NewTextHandler(stderr, nil)).Error("release e2e lock", "error", releaseErr)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	recoveryTarget := loadCleanupBaseline(options.baselineFile, logger)

	// Recover through the formal Connect APIs as e2e-runner. Generated clients
	// and the access token stay behind the test/e2e surface; this command never
	// patches Kubernetes, writes the database, or shells out (AC-066-34/38).
	recovery, err := newCleanupRecovery(options.envConfig)
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	cleanupCtx, cancel := e2e.CleanupDeadline(ctx, 0)
	defer cancel()
	runnerUserID, err := recovery.Login(cleanupCtx)
	if err != nil {
		logger.Error("e2e cleanup login failed", "error", safeErrorMessage(err))
		fmt.Fprintf(stdout, "E2E cleanup failed: e2e-runner login unavailable (run make e2e-cleanup after the environment is up)\n")
		return int(exitRuntime)
	}
	report, err := e2e.RunCleanup(cleanupCtx, recoveryTarget, runnerUserID, recovery, logger)
	if err != nil {
		logger.Error("e2e cleanup recovery failed", "error", safeErrorMessage(err))
		fmt.Fprintf(stdout, "E2E cleanup failed: %s\n", safeErrorMessage(err))
		return int(exitRuntime)
	}
	// Recovery and verification are separate phases because they answer separate
	// questions: RunCleanup reports which recovery operations completed, and this
	// reads the environment back to see whether the workload actually returned to
	// the baseline. Merging them into one report keeps the artifact a single
	// truthful account (AC-066-34).
	verification := e2e.VerifyRestore(cleanupCtx, recoveryTarget, cleanupObserver(options.envConfig, logger), logger)
	// The release half reads the same inventory the recovery pass used. It is a
	// separate call rather than a return value because it must read after the
	// rollbacks, and it goes through the recovery seam so a read failure degrades
	// to unverified instead of to a silent pass.
	verification.MergeVerification(e2e.VerifyRevisions(cleanupCtx, recoveryTarget, recovery, logger))
	report.MergeVerification(verification)
	writeCleanupSummary(stdout, report)
	return int(exitSuccess)
}

// cleanupObserver builds the read-only observer the outcome check reads through.
//
// A failure to build one is not fatal: the check then reports the workload as
// unverified, which is the truth, rather than skipping the check silently.
func cleanupObserver(envConfig string, logger *slog.Logger) e2e.RecoveryObserver {
	cfg, err := e2e.LoadConfig(envConfig)
	if err != nil {
		logger.Warn("cleanup verification cannot read the environment; workloads will report as unverified", "error", safeErrorMessage(err))
		return nil
	}
	provider, err := livewire.NewClusterContextsClientProvider(cfg)
	if err != nil {
		logger.Warn("cleanup verification cannot build a cluster client provider; workloads will report as unverified", "error", safeErrorMessage(err))
		return nil
	}
	observer, err := livewire.NewCleanupReplicaObserver(provider)
	if err != nil {
		logger.Warn("cleanup verification cannot build the replica observer; workloads will report as unverified", "error", safeErrorMessage(err))
		return nil
	}
	return observer
}

// loadCleanupBaseline parses {--baseline-file} as the recovery target. A
// missing or invalid baseline degrades cleanup to residual-only (cancel
// runner-owned non-terminal operations only) with an explicit warning, never
// silently (AC-066-23/28, D-026 D4).
func loadCleanupBaseline(baselineFile string, logger *slog.Logger) *e2e.BaselineRecovery {
	baselineData, readErr := os.ReadFile(baselineFile)
	switch {
	case readErr != nil && !errors.Is(readErr, os.ErrNotExist):
		logger.Warn("baseline unreadable; cleanup is residual-only", "error", readErr)
		return nil
	case readErr != nil:
		logger.Warn("baseline not found; cleanup is residual-only", "file", baselineFile)
		return nil
	}
	var baseline baselineArtifact
	if err := json.Unmarshal(baselineData, &baseline); err != nil {
		logger.Warn("baseline invalid; cleanup is residual-only", "error", err)
		return nil
	}
	target := e2e.BaselineRecoveryFromSnapshots(baseline.FixtureSnapshot)
	// The residue lives beside the baseline: the same run writes both, and
	// cleanup is always run against one run's artifacts.
	target.Residue = loadCleanupResidue(filepath.Join(filepath.Dir(baselineFile), residueFileName), baseline.RunID, logger)
	if len(target.Residue) == 0 {
		logger.Warn("no usable run residue; cleanup rollback degrades to the baseline revision comparison",
			"run_id", baseline.RunID)
	}
	// Degrade only when the baseline carries no recovery target at all. A
	// baseline with replicas but no revisions is still usable: discarding it
	// whole is what kept the replica restore degraded to a warning even after
	// collection started recording replica counts (AC-066-34).
	if len(target.Revisions) == 0 && len(target.Replicas) == 0 {
		logger.Warn("baseline carries no recovery targets; cleanup is residual-only", "run_id", baseline.RunID)
		return nil
	}
	if len(target.Revisions) == 0 {
		logger.Warn("baseline carries no revision recovery targets; revision restore will be skipped", "run_id", baseline.RunID)
	}
	if len(target.Replicas) == 0 {
		logger.Warn("baseline carries no workload replicas; replica restore will be skipped", "run_id", baseline.RunID)
	}
	logger.Info("baseline loaded for cleanup", "run_id", baseline.RunID, "revision_targets", len(target.Revisions), "replica_targets", len(target.Replicas))
	return &target
}

// newCleanupRecovery builds the formal Connect recovery implementation from
// the env-config.
func newCleanupRecovery(envConfig string) (*e2e.LiveRecovery, error) {
	config, err := e2e.LoadConfig(envConfig)
	if err != nil {
		return nil, err
	}
	clients, err := e2e.NewClientBundle(config)
	if err != nil {
		return nil, err
	}
	recovery, err := e2e.NewLiveRecovery(config, clients)
	if err != nil {
		return nil, err
	}
	return recovery, nil
}

func writeCleanupSummary(stdout io.Writer, report e2e.CleanupReport) {
	fmt.Fprintf(stdout,
		"E2E cleanup: cancelled=%d rolled_back=%d skipped_revision_restore=%d residual=%d residual_replicas=%d unverified_replicas=%d residual_revisions=%d unverified_revisions=%d replicas_restore_skipped=%v baseline_missing=%v\n",
		len(report.CancelledOperationIDs),
		len(report.RolledBackDefinitions),
		len(report.SkippedRevisionRestore),
		len(report.ResidualNonTerminal),
		len(report.ResidualReplicas),
		len(report.UnverifiedReplicas),
		len(report.ResidualRevisions),
		len(report.UnverifiedRevisions),
		report.SkippedReplicasRestore,
		report.BaselineMissing,
	)
	for _, id := range report.CancelledOperationIDs {
		fmt.Fprintf(stdout, "- cancelled operation %s\n", id)
	}
	for _, definition := range report.RolledBackDefinitions {
		fmt.Fprintf(stdout, "- rolled back definition %s\n", definition)
	}
	for _, definition := range report.SkippedRevisionRestore {
		fmt.Fprintf(stdout, "- skipped revision restore for %s (no baseline target)\n", definition)
	}
	for _, id := range report.ResidualNonTerminal {
		fmt.Fprintf(stdout, "- residual requires manual cleanup: %s\n", id)
	}
	for _, workload := range report.ResidualReplicas {
		fmt.Fprintf(stdout, "- residual replicas: %s did not return to the baseline count\n", workload)
	}
	for _, workload := range report.UnverifiedReplicas {
		fmt.Fprintf(stdout, "- unverified replicas: %s could not be read after recovery\n", workload)
	}
	for _, definition := range report.ResidualRevisions {
		fmt.Fprintf(stdout, "- residual revision: %s still carries the run residue\n", definition)
	}
	for _, definition := range report.UnverifiedRevisions {
		fmt.Fprintf(stdout, "- unverified revision: %s could not be compared with the run residue\n", definition)
	}
}

func parseRunOptions(args []string, stderr io.Writer) (runOptions, error) {
	options := runOptions{}
	flags := flag.NewFlagSet("e2e", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printRunUsage(stderr) }
	flags.StringVar(&options.stages, "stages", "all", "comma-separated stages (default: all)")
	flags.DurationVar(&options.timeout, "timeout", 5*time.Minute, "per-stage timeout")
	flags.DurationVar(&options.totalTimeout, "total-timeout", 25*time.Minute, "total run timeout")
	flags.StringVar(&options.outputDir, "output-dir", "./e2e-results", "JSON artifact directory")
	flags.BoolVar(&options.parallel, "parallel", false, "run inventory and artifact concurrently")
	flags.BoolVar(&options.keepFailure, "keep-on-failure", false, "retain failure diagnostics")
	flags.BoolVar(&options.snapshotFull, "snapshot-full", false, "request full public baseline snapshot")
	flags.StringVar(&options.envConfig, "env-config", "", "E2E environment config YAML (required)")
	if err := flags.Parse(args); err != nil {
		return runOptions{}, err
	}
	if flags.NArg() != 0 {
		return runOptions{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	return options, nil
}

func parseCleanupOptions(args []string, stderr io.Writer) (cleanupOptions, error) {
	options := cleanupOptions{}
	flags := flag.NewFlagSet("e2e cleanup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printCleanupUsage(stderr) }
	flags.StringVar(&options.envConfig, "env-config", "", "E2E environment config YAML (required)")
	flags.StringVar(&options.outputDir, "output-dir", "./e2e-results", "JSON artifact directory")
	flags.StringVar(&options.baselineFile, "baseline-file", "", "baseline artifact path")
	if err := flags.Parse(args); err != nil {
		return cleanupOptions{}, err
	}
	if flags.NArg() != 0 {
		return cleanupOptions{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if options.baselineFile == "" {
		options.baselineFile = filepath.Join(options.outputDir, "baseline.json")
	}
	return options, nil
}

func parseStages(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%w: empty stage selection", e2e.ErrInvalidStageSelection)
	}
	parts := strings.Split(value, ",")
	if len(parts) == 1 && strings.TrimSpace(parts[0]) == "all" {
		return e2e.CanonicalStageNames(), nil
	}
	canonical := e2e.CanonicalStageNames()
	known := make(map[string]struct{}, len(canonical))
	for _, name := range canonical {
		known[name] = struct{}{}
	}
	requested := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "all" {
			return nil, fmt.Errorf("%w: all cannot be combined with named stages", e2e.ErrInvalidStageSelection)
		}
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("%w: unknown stage %q", e2e.ErrInvalidStageSelection, name)
		}
		if _, ok := requested[name]; ok {
			return nil, fmt.Errorf("%w: duplicate stage %q", e2e.ErrInvalidStageSelection, name)
		}
		requested[name] = struct{}{}
	}
	selected := make([]string, 0, len(requested))
	for _, name := range canonical {
		if _, ok := requested[name]; ok {
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: empty stage selection", e2e.ErrInvalidStageSelection)
	}
	return selected, nil
}

func prepareOutputDir(outputDir string, stages []string) error {
	if strings.TrimSpace(outputDir) == "" {
		return errors.New("--output-dir must not be empty")
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := e2e.PrepareOutputDir(outputDir, stages); err != nil {
		return err
	}
	return nil
}

func acquireOptionalLock(runID string) (*e2e.ProcessLock, error) {
	path := strings.TrimSpace(os.Getenv("E2E_LOCK_FILE"))
	if path == "" {
		return nil, nil
	}
	return e2e.AcquireProcessLock(path, runID)
}

func reportLockError(stderr io.Writer, err error) int {
	var conflict *e2e.LockConflictError
	if errors.As(err, &conflict) {
		fmt.Fprintf(stderr, "environment_locked: %v\n", err)
		return int(exitLock)
	}
	fmt.Fprintf(stderr, "e2e: lock: %v\n", err)
	return int(exitRuntime)
}

func runID() (string, error) {
	value, err := e2e.RunIDFromEnv()
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	return "local-" + time.Now().UTC().Format("20060102-150405") + "-" + strconv.Itoa(os.Getpid()), nil
}

func writeDiagnostic(outputDir, runID string, result e2e.StageResult) error {
	return e2e.WriteJSONAtomic(
		filepath.Join(outputDir, "diagnostics", runID, result.Stage, "result.json"),
		result,
	)
}

func startedAt(report e2e.Report, fallback time.Time) time.Time {
	for index := range report.Results {
		if !report.Results[index].StartedAt.IsZero() {
			return report.Results[index].StartedAt
		}
	}
	return fallback
}

func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" || len(message) > 256 || strings.ContainsAny(message, "\r\n\t") {
		return "e2e run failed"
	}
	for _, token := range []string{"password", "secret", "token", "jwt", "postgres://", "postgresql://", "mysql://"} {
		if strings.Contains(strings.ToLower(message), token) {
			return "e2e run failed"
		}
	}
	return message
}

func writeSummary(stdout io.Writer, artifact e2e.RunArtifact, results []e2e.StageResult) {
	fmt.Fprintf(stdout, "E2E run %s: pass=%d fail=%d skip=%d exit=%d\n", artifact.RunID, artifact.Pass, artifact.Fail, artifact.Skip, artifact.ExitCode)
	for index := range results {
		fmt.Fprintf(stdout, "- %s: %s (%dms)\n", results[index].Stage, results[index].Status, results[index].DurationMs)
	}
}

func writeUsageError(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "e2e: %v\n", err)
	printRunUsage(stderr)
}

func printRunUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: e2e [run] --env-config <path> [flags]")
	fmt.Fprintln(writer, "       e2e cleanup --env-config <path> [--output-dir <dir>] [--baseline-file <path>]")
}

func printCleanupUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: e2e cleanup --env-config <path> [--output-dir <dir>] [--baseline-file <path>]")
}
