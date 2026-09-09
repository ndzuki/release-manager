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
)

const (
	exitSuccess exitCode = iota
	exitRuntime
	exitUsage
)

const exitLock exitCode = 3

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

func runStages(args []string, stdout, stderr io.Writer) int {
	options, err := parseRunOptions(args, stderr)
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	if options.envConfig == "" {
		writeUsageError(stderr, errors.New("--env-config is required"))
		return int(exitUsage)
	}

	selected, err := parseStages(options.stages)
	if err != nil {
		writeUsageError(stderr, err)
		return int(exitUsage)
	}
	if options.timeout <= 0 || options.totalTimeout <= 0 {
		writeUsageError(stderr, errors.New("--timeout and --total-timeout must be positive"))
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

	baseline := baselineArtifact{
		RunID:          runID,
		Environment:    config.Environment,
		EnvironmentID:  config.EnvironmentID,
		FixtureVersion: config.Seed.FixtureVersion,
		SnapshotFull:   options.snapshotFull,
	}
	// The embedded snapshot time is assigned after the literal because go1.26
	// disallows promoted fields in a composite literal of the outer type.
	baseline.CollectedAt = time.Now().UTC()
	baselineDigest, err := e2e.StableDigest(baseline)
	if err != nil {
		logger.Error("collect baseline", "error", err)
		return int(exitRuntime)
	}
	if err := e2e.WriteJSONAtomic(filepath.Join(options.outputDir, "baseline.json"), baseline); err != nil {
		logger.Error("write baseline artifact", "error", err)
		return int(exitRuntime)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The deep stage implementations are still being delivered behind the
	// test/e2e package. New(cfg) supplies the canonical dependency graph with
	// deterministic no-op stage bodies, which keeps this CLI seam usable without
	// shelling out to kubectl/helm or inventing a second runtime implementation.
	harness := e2e.New(*config)
	report, runErr := harness.Run(ctx, e2e.Scenario{
		SelectedStages: selected,
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

	stageArtifacts := make([]e2e.StageArtifact, 0, len(report.Results))
	for _, result := range report.Results {
		stage := result.Stage
		if stage == "" {
			stage = result.Name
		}
		if err := e2e.WriteJSONAtomic(filepath.Join(options.outputDir, stage+".json"), result); err != nil {
			logger.Error("write stage artifact", "stage", stage, "error", err)
			return int(exitRuntime)
		}
		stageArtifacts = append(stageArtifacts, e2e.StageArtifact{
			Stage:  stage,
			Status: result.Status,
			File:   stage + ".json",
		})
		if result.Status == e2e.StageFail {
			logger.Warn("e2e stage failed", "stage", stage, "root_cause", result.RootCause)
			if options.keepFailure {
				if err := writeDiagnostic(options.outputDir, runID, result); err != nil {
					logger.Error("write failure diagnostic", "stage", stage, "error", err)
					return int(exitRuntime)
				}
			}
		}
	}

	runArtifact := e2e.RunArtifact{
		RunID:          runID,
		SelectedStages: append([]string(nil), selected...),
		BaselineDigest: baselineDigest,
		StartedAt:      startedAt(report, baseline.CollectedAt),
		FinishedAt:     time.Now().UTC(),
		Pass:           report.Passed,
		Fail:           report.Failed,
		Skip:           report.Skipped,
		ExitCode:       report.ExitCode,
		Stages:         stageArtifacts,
	}
	if runErr != nil {
		runArtifact.Fatal = &e2e.ErrorCause{
			Code:      "e2e_run_failed",
			Component: "harness",
			Message:   safeErrorMessage(runErr),
		}
	}
	if err := e2e.WriteJSONAtomic(filepath.Join(options.outputDir, "run.json"), runArtifact); err != nil {
		logger.Error("write run artifact", "error", err)
		return int(exitRuntime)
	}

	writeSummary(stdout, runArtifact, report.Results)
	logger.Info("e2e run finished", "run_id", runID, "exit_code", report.ExitCode)
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
	writeCleanupSummary(stdout, report)
	return int(exitSuccess)
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
	if len(target.Revisions) == 0 {
		logger.Warn("baseline carries no revision recovery targets; cleanup is residual-only", "run_id", baseline.RunID)
		return nil
	}
	logger.Info("baseline loaded for cleanup", "run_id", baseline.RunID, "revision_targets", len(target.Revisions))
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
		"E2E cleanup: cancelled=%d rolled_back=%d skipped_revision_restore=%d residual=%d replicas_restore_skipped=%v baseline_missing=%v\n",
		len(report.CancelledOperationIDs),
		len(report.RolledBackDefinitions),
		len(report.SkippedRevisionRestore),
		len(report.ResidualNonTerminal),
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
	for _, result := range report.Results {
		if !result.StartedAt.IsZero() {
			return result.StartedAt
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
	for _, result := range results {
		fmt.Fprintf(stdout, "- %s: %s (%dms)\n", result.Stage, result.Status, result.DurationMs)
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
