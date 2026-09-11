package e2e

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// CanonicalDependencies is the stable dependency graph used by the deep
// runner. Selection remains explicit: dependencies are never added to a run.
var CanonicalDependencies = map[string][]string{
	StageControlPlane: {},
	StageInventory:    {StageControlPlane},
	StageArtifactName: {StageControlPlane},
	StageRelease:      {StageInventory},
	StageIsolation:    {StageInventory},
	StageEmergency:    {StageControlPlane},
	StageRestart:      {StageControlPlane},
}

var canonicalStageOrder = []string{
	StageControlPlane,
	StageInventory,
	StageArtifactName,
	StageRelease,
	StageIsolation,
	StageEmergency,
	StageRestart,
}

// CanonicalStageNames returns the canonical registration order.
func CanonicalStageNames() []string { return append([]string(nil), canonicalStageOrder...) }

// Runner is the historical linear runner surface. It remains deterministic
// for existing tests; new code should use Harness for dependency selection.
type Runner struct {
	Stages       []Stage
	StageTimeout time.Duration
	TotalTimeout time.Duration
	SkipOnFail   bool
	Logger       *slog.Logger
}

// NewRunner creates a Runner with compatibility defaults.
func NewRunner(stages ...Stage) *Runner {
	return &Runner{
		Stages:       append([]Stage(nil), stages...),
		StageTimeout: 5 * time.Minute,
		SkipOnFail:   true,
		Logger:       slog.Default(),
	}
}

// RunResult aggregates the results of a run.
type RunResult struct {
	Results      []StageResult
	TotalElapsed time.Duration
	Skipped      int
	Failed       int
	Passed       int
	Fatal        *ErrorCause
	ExitCode     int
}

// Report is the public deep-runner report. It aliases the compatibility
// aggregate so callers can inspect the same stable results without conversion.
type Report = RunResult

// AllPassed reports whether every selected stage passed.
func (r *RunResult) AllPassed() bool {
	return r != nil && r.Failed == 0 && r.Skipped == 0
}

// Result returns a named result, if present.
func (r *RunResult) Result(name string) (StageResult, bool) {
	if r == nil {
		return StageResult{}, false
	}
	for index := range r.Results {
		if r.Results[index].Stage == name || r.Results[index].Name == name {
			return r.Results[index], true
		}
	}
	return StageResult{}, false
}

// Run executes the historical linear stage list.
func (r *Runner) Run(ctx context.Context, fixture *Fixture) *RunResult {
	started := time.Now()
	result := &RunResult{Results: make([]StageResult, 0, len(r.Stages))}
	if fixture == nil {
		fixture = NewFixture()
	}
	root, cancel := contextWithTimeout(ctx, r.TotalTimeout)
	defer cancel()

	previousFailed := false
	for _, stage := range r.Stages {
		if stage == nil {
			result.Results = append(result.Results, skippedResult("", "nil stage"))
			result.Skipped++
			continue
		}
		if previousFailed && r.SkipOnFail {
			result.Results = append(result.Results, skippedResult(stage.Name(), "previous stage failed"))
			result.Skipped++
			continue
		}
		if err := root.Err(); err != nil {
			result.Results = append(result.Results, skippedResult(stage.Name(), timeoutRootCause(err)))
			result.Skipped++
			continue
		}

		stageResult := executeStage(root, stage, fixture, r.StageTimeout, r.logger())
		result.Results = append(result.Results, stageResult)
		switch stageResult.Status {
		case StagePass:
			result.Passed++
		case StageFail:
			result.Failed++
			previousFailed = true
		case StageSkip:
			result.Skipped++
		}
	}
	result.TotalElapsed = time.Since(started)
	result.ExitCode = reportExitCode(result)
	return result
}

func (r *Runner) logger() *slog.Logger {
	if r != nil && r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// ErrInvalidStageSelection indicates duplicate or unknown selected stages.
var ErrInvalidStageSelection = errors.New("invalid stage selection")

// PanicError records a recovered stage panic without exposing a stack in the
// serialized result. The stack remains available to in-process diagnostics.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	if e == nil {
		return "stage panic"
	}
	return "stage panic"
}

// RunError reports a panic or other orchestration error while retaining the
// partial report returned by the run.
type RunError struct {
	Stage string
	Err   error
}

func (e *RunError) Error() string {
	if e == nil || e.Err == nil {
		return "e2e run failed"
	}
	if e.Stage == "" {
		return fmt.Sprintf("e2e run failed: %v", e.Err)
	}
	return fmt.Sprintf("e2e run failed at %s: %v", e.Stage, e.Err)
}

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Harness executes selected stages in the canonical dependency graph.
type Harness struct {
	Stages       []StageSpec
	StageTimeout time.Duration
	TotalTimeout time.Duration
	Parallel     bool
	Logger       *slog.Logger
	config       *Config
}

// Scenario is the thin external run description. Scope, callbacks, cleanup,
// assertions, and policy are intentionally opaque here; this Step 2 runner
// only schedules the supplied stage graph and does not expose clients.
type Scenario struct {
	Name    string
	Scope   any
	Stage   any
	Cleanup any
	Assert  any
	Policy  any

	Stages         []StageSpec
	StageSpecs     []StageSpec
	SelectedStages []string
	Selected       []string
	Selection      []string
	Dependencies   map[string][]string
	Fixture        *Fixture
	StageTimeout   time.Duration
	TotalTimeout   time.Duration
	Parallel       bool
	RunID          string
}

// New constructs the deep Harness from the public Config seam. Config
// validation remains the responsibility of the caller/config package; this
// constructor does not perform I/O or expose environment clients. Its
// canonical stages fail closed until real implementations are supplied.
func New(cfg Config) *Harness {
	return &Harness{
		Stages:       canonicalDefaultSpecs(),
		StageTimeout: 5 * time.Minute,
		Logger:       slog.Default(),
		config:       &cfg,
	}
}

// NewHarness is a test-friendly constructor accepting stage implementations.
func NewHarness(inputs ...any) *Harness {
	h := &Harness{
		StageTimeout: 5 * time.Minute,
		Logger:       slog.Default(),
	}
	for _, input := range inputs {
		switch value := input.(type) {
		case Config:
			configCopy := value
			h.config = &configCopy
		case *Config:
			if value != nil {
				configCopy := *value
				h.config = &configCopy
			}
		case StageSpec:
			h.Stages = append(h.Stages, value)
		case []StageSpec:
			h.Stages = append(h.Stages, value...)
		case Stage:
			if value != nil {
				h.Stages = append(h.Stages, StageSpec{Name: value.Name(), Stage: value})
			}
		case []Stage:
			for _, stage := range value {
				if stage != nil {
					h.Stages = append(h.Stages, StageSpec{Name: stage.Name(), Stage: stage})
				}
			}
		case map[string]Stage:
			h.Stages = append(h.Stages, specsFromMap(value)...)
		case Scenario:
			h.Stages = append(h.Stages, normalizeScenarioStages(value)...)
		}
	}
	if len(h.Stages) == 0 {
		h.Stages = canonicalDefaultSpecs()
	}
	return h
}

// NewCanonicalHarness creates a Harness from canonical stage implementations.
func NewCanonicalHarness(stages map[string]Stage) *Harness {
	return NewHarness(specsFromMap(stages))
}

// CanonicalStageSpecs creates canonical definitions from stage implementations.
func CanonicalStageSpecs(stages map[string]Stage) []StageSpec { return specsFromMap(stages) }

// Run executes a Scenario and returns a partial report plus an error for
// startup validation failures. Stage failures are represented in the report;
// they do not turn the scheduling API itself into an error.
func (h *Harness) Run(ctx context.Context, scenario Scenario) (Report, error) {
	if h == nil {
		return Report{}, errors.New("nil e2e harness")
	}
	return h.run(ctx, scenario)
}

// RunScenario executes a Scenario with canonical fail-closed stage defaults.
func RunScenario(ctx context.Context, scenario Scenario) (Report, error) {
	return NewHarness().Run(ctx, scenario)
}

// RunScenario executes a Scenario using this Harness's graph.
func (h *Harness) RunScenario(ctx context.Context, scenario Scenario) (Report, error) {
	return h.Run(ctx, scenario)
}

func (h *Harness) run(ctx context.Context, scenario Scenario) (Report, error) {
	started := time.Now()
	specs := normalizeScenarioStages(scenario)
	if len(specs) == 0 {
		specs = append([]StageSpec(nil), h.Stages...)
	}
	if len(specs) == 0 {
		specs = canonicalDefaultSpecs()
	}
	if len(scenario.Dependencies) != 0 {
		for i := range specs {
			if deps, ok := scenario.Dependencies[specs[i].Name]; ok {
				specs[i].Dependencies = append([]string(nil), deps...)
				specs[i].DependsOn = nil
			}
		}
	}
	ordered, byName := orderSpecs(specs)
	selected, err := selectedNames(scenario, ordered, byName)
	if err != nil {
		return Report{TotalElapsed: time.Since(started)}, err
	}
	fixture := scenario.Fixture
	if fixture == nil {
		fixture = NewFixture()
	}
	stageTimeout := scenario.StageTimeout
	if stageTimeout == 0 {
		stageTimeout = h.StageTimeout
	}
	totalTimeout := scenario.TotalTimeout
	if totalTimeout == 0 {
		totalTimeout = h.TotalTimeout
	}
	parallel := scenario.Parallel || h.Parallel
	root, cancel := contextWithTimeout(ctx, totalTimeout)
	defer cancel()

	results := executeGraph(root, ordered, byName, selected, fixture, stageTimeout, parallel, h.logger())
	report := Report{
		Results:      results,
		TotalElapsed: time.Since(started),
	}
	for index := range results {
		switch results[index].Status {
		case StagePass:
			report.Passed++
		case StageFail:
			report.Failed++
		case StageSkip:
			report.Skipped++
		}
	}
	for index := range report.Results {
		var panicErr *PanicError
		if errors.As(report.Results[index].Error, &panicErr) {
			report.ExitCode = 1
			return report, &RunError{Stage: report.Results[index].Stage, Err: panicErr}
		}
	}
	report.ExitCode = reportExitCode(&report)
	return report, nil
}

func (h *Harness) logger() *slog.Logger {
	if h != nil && h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func executeGraph(
	ctx context.Context,
	ordered []StageSpec,
	byName map[string]StageSpec,
	selected map[string]bool,
	fixture *Fixture,
	defaultTimeout time.Duration,
	parallel bool,
	logger *slog.Logger,
) []StageResult {
	results := make(map[string]StageResult, len(selected))
	pending := make(map[string]bool, len(selected))
	for name := range selected {
		pending[name] = true
	}

	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			for _, spec := range ordered {
				if pending[spec.Name] {
					results[spec.Name] = skippedResult(spec.Name, timeoutRootCause(err))
					delete(pending, spec.Name)
				}
			}
			break
		}

		ready := make([]StageSpec, 0, len(pending))
		progress := false
		for _, spec := range ordered {
			if !pending[spec.Name] {
				continue
			}
			depSkip, depPending := dependencyState(spec, selected, results, byName)
			if depSkip != "" {
				results[spec.Name] = skippedResult(spec.Name, depSkip)
				delete(pending, spec.Name)
				progress = true
				continue
			}
			if !depPending {
				ready = append(ready, spec)
			}
		}
		if len(ready) == 0 {
			if progress {
				continue
			}
			for _, spec := range ordered {
				if pending[spec.Name] {
					results[spec.Name] = skippedResult(spec.Name, "stage_skipped: dependency cycle")
					delete(pending, spec.Name)
				}
			}
			break
		}

		batch := ready[:1]
		if parallel {
			parallelBatch := make([]StageSpec, 0, 2)
			for _, spec := range ready {
				if spec.Name == StageInventory || spec.Name == StageArtifactName {
					parallelBatch = append(parallelBatch, spec)
				}
			}
			if len(parallelBatch) >= 2 {
				batch = parallelBatch
			}
		}
		batchResults := executeBatch(ctx, batch, fixture, defaultTimeout, logger)
		for index := range batchResults {
			results[batchResults[index].Stage] = batchResults[index]
			delete(pending, batchResults[index].Stage)
		}
	}

	out := make([]StageResult, 0, len(selected))
	for _, spec := range ordered {
		if selected[spec.Name] {
			out = append(out, results[spec.Name])
		}
	}
	return out
}

func dependencyState(
	spec StageSpec,
	selected map[string]bool,
	results map[string]StageResult,
	byName map[string]StageSpec,
) (string, bool) {
	pending := false
	for _, dependency := range spec.dependencies() {
		if !selected[dependency] {
			// Explicit selection deliberately does not pull prerequisites in.
			continue
		}
		if _, defined := byName[dependency]; !defined {
			return fmt.Sprintf("stage_skipped: dependency %q is undefined", dependency), false
		}
		result, done := results[dependency]
		if !done {
			pending = true
			continue
		}
		if result.Status != StagePass {
			return fmt.Sprintf("stage_skipped: dependency %s %s", dependency, result.Status), false
		}
	}
	return "", pending
}

func executeBatch(
	ctx context.Context,
	batch []StageSpec,
	fixture *Fixture,
	defaultTimeout time.Duration,
	logger *slog.Logger,
) []StageResult {
	if len(batch) == 1 {
		result := executeStage(ctx, batch[0].stage(), fixture, effectiveTimeout(batch[0], defaultTimeout), logger)
		result.Stage = batch[0].Name
		result.Name = batch[0].Name
		return []StageResult{result}
	}
	results := make(chan StageResult, len(batch))
	var wg sync.WaitGroup
	for _, spec := range batch {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- executeStage(ctx, spec.stage(), fixture, effectiveTimeout(spec, defaultTimeout), logger)
		}()
	}
	wg.Wait()
	close(results)
	out := make([]StageResult, 0, len(batch))
	for result := range results {
		out = append(out, result)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stage < out[j].Stage })
	return out
}

func executeStage(
	ctx context.Context,
	stage Stage,
	fixture *Fixture,
	timeout time.Duration,
	logger *slog.Logger,
) StageResult {
	started := time.Now()
	name := ""
	if stage != nil {
		name = stage.Name()
	}
	result := StageResult{
		Stage:     name,
		Name:      name,
		StartedAt: started,
		Status:    StageFail,
	}
	if stage == nil {
		return finishStageResult(result, errors.New("nil stage"), "nil stage", started)
	}

	stageCtx, cancel := contextWithTimeout(ctx, timeout)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				errCh <- &PanicError{Value: recovered, Stack: debug.Stack()}
			}
		}()
		errCh <- stage.Run(stageCtx, fixture)
	}()

	var err error
	select {
	case err = <-errCh:
	case <-stageCtx.Done():
		err = stageCtx.Err()
	}
	if err == nil {
		result.Status = StagePass
		result.Duration = time.Since(started)
		result.DurationMs = result.Duration.Milliseconds()
		return result
	}

	rootCause := "stage_failed"
	cause := err.Error()
	errorCode := ""
	var panicErr *PanicError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		rootCause = "e2e_timeout"
		cause = "stage timeout"
		errorCode = "e2e_timeout"
	case errors.Is(err, context.Canceled):
		rootCause = "e2e_canceled"
		cause = "stage canceled"
		errorCode = "e2e_canceled"
	case errors.As(err, &panicErr):
		rootCause = "panic"
		cause = "stage panic"
		errorCode = "panic"
	default:
		if code := stageFailureCode(err); code != "" {
			rootCause = code
			errorCode = code
		}
	}
	if logger != nil {
		logger.Debug("e2e stage failed", "stage", name, "root_cause", rootCause, "error_code", errorCode)
	}
	result.ErrorCode = errorCode
	return finishStageResult(result, err, cause, started)
}

// stageFailureCode recovers the stable machine-readable code from a stage
// failure.
//
// Two accessor shapes are accepted. ErrorCode() is the contract the stage
// package implements, because StageError exposes its code as a field and a Go
// method cannot share a field's name. Code() is the older shape kept for error
// types declared inside this package, such as NotImplementedError.
//
// errors.As is used rather than a direct type assertion so a stage that wraps
// its failure still reports the underlying code instead of the generic
// "stage_failed".
func stageFailureCode(err error) string {
	var coder interface{ ErrorCode() string }
	if errors.As(err, &coder) {
		if code := coder.ErrorCode(); code != "" {
			return code
		}
	}
	var legacy interface{ Code() string }
	if errors.As(err, &legacy) {
		if code := legacy.Code(); code != "" {
			return code
		}
	}
	return ""
}

func finishStageResult(result StageResult, err error, cause string, started time.Time) StageResult {
	result.Error = err
	result.Cause = cause
	result.RootCause = cause
	result.Duration = time.Since(started)
	result.DurationMs = result.Duration.Milliseconds()
	if result.ErrorCode == "" {
		result.ErrorCode = "stage_failed"
	}
	return result
}

func skippedResult(name, cause string) StageResult {
	now := time.Now()
	return StageResult{
		Stage:     name,
		Name:      name,
		Status:    StageSkip,
		StartedAt: now,
		RootCause: cause,
		Cause:     cause,
		ErrorCode: "stage_skipped",
	}
}

func contextWithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

func timeoutRootCause(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "e2e_timeout: total timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "e2e_timeout: context canceled"
	}
	return err.Error()
}

func effectiveTimeout(spec StageSpec, defaultTimeout time.Duration) time.Duration {
	if spec.Timeout > 0 {
		return spec.Timeout
	}
	if spec.Name == StageRestart {
		return 10 * time.Minute
	}
	return defaultTimeout
}

func normalizeScenarioStages(scenario Scenario) []StageSpec {
	if len(scenario.Stages) != 0 {
		return append([]StageSpec(nil), scenario.Stages...)
	}
	if len(scenario.StageSpecs) != 0 {
		return append([]StageSpec(nil), scenario.StageSpecs...)
	}
	switch value := scenario.Stage.(type) {
	case StageSpec:
		return []StageSpec{value}
	case []StageSpec:
		return append([]StageSpec(nil), value...)
	case Stage:
		if value != nil {
			return []StageSpec{{Name: value.Name(), Stage: value}}
		}
	}
	return nil
}

func selectedNames(
	scenario Scenario,
	ordered []StageSpec,
	byName map[string]StageSpec,
) (map[string]bool, error) {
	selection := scenario.SelectedStages
	if len(selection) == 0 {
		selection = scenario.Selected
	}
	if len(selection) == 0 {
		selection = scenario.Selection
	}
	selected := make(map[string]bool)
	if len(selection) == 0 {
		for _, spec := range ordered {
			selected[spec.Name] = true
		}
		return selected, nil
	}
	for _, name := range selection {
		if name == "" {
			return nil, fmt.Errorf("%w: empty stage name", ErrInvalidStageSelection)
		}
		if selected[name] {
			return nil, fmt.Errorf("%w: duplicate stage %q", ErrInvalidStageSelection, name)
		}
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("%w: unknown stage %q", ErrInvalidStageSelection, name)
		}
		selected[name] = true
	}
	return selected, nil
}

func orderSpecs(specs []StageSpec) ([]StageSpec, map[string]StageSpec) {
	byName := make(map[string]StageSpec, len(specs))
	for _, spec := range specs {
		if spec.Name == "" && spec.Stage != nil {
			spec.Name = spec.Stage.Name()
		}
		if spec.Name == "" {
			continue
		}
		if len(spec.Dependencies) == 0 && len(spec.DependsOn) == 0 {
			if deps, ok := CanonicalDependencies[spec.Name]; ok {
				spec.Dependencies = append([]string(nil), deps...)
			}
		}
		byName[spec.Name] = spec
	}

	ordered := make([]StageSpec, 0, len(byName))
	visited := make(map[string]bool, len(byName))
	visiting := make(map[string]bool, len(byName))
	var visit func(string)
	visit = func(name string) {
		if visited[name] || visiting[name] {
			return
		}
		visiting[name] = true
		spec, ok := byName[name]
		if ok {
			deps := spec.dependencies()
			sort.Strings(deps)
			for _, dep := range deps {
				visit(dep)
			}
			ordered = append(ordered, spec)
		}
		delete(visiting, name)
		visited[name] = true
	}
	for _, name := range canonicalStageOrder {
		visit(name)
	}
	remaining := make([]string, 0, len(byName))
	for name := range byName {
		if !visited[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		visit(name)
	}
	return ordered, byName
}

func specsFromMap(stages map[string]Stage) []StageSpec {
	out := make([]StageSpec, 0, len(stages))
	seen := make(map[string]bool, len(stages))
	for _, name := range canonicalStageOrder {
		if stage, ok := stages[name]; ok {
			out = append(out, StageSpec{
				Name:         name,
				Stage:        stage,
				Dependencies: append([]string(nil), CanonicalDependencies[name]...),
			})
			seen[name] = true
		}
	}
	remaining := make([]string, 0, len(stages))
	for name := range stages {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		out = append(out, StageSpec{Name: name, Stage: stages[name]})
	}
	return out
}

// canonicalDefaultSpecs returns the canonical dependency graph used when a
// scenario supplies no stage implementations (for example `New(Config)` and
// `RunScenario`). Every canonical stage fails closed with ErrStageNotImplemented:
// a real implementation must be provided through Scenario.Stages before a run
// can pass. This keeps `cmd/e2e run` honest while write/read-only live stages
// are still being delivered — a stage body that does nothing must never report
// a vacuous pass (TASK-066 Step 8 fail-closed contract).
func canonicalDefaultSpecs() []StageSpec {
	out := make([]StageSpec, 0, len(canonicalStageOrder))
	for _, name := range canonicalStageOrder {
		out = append(out, UnimplementedSpec(name, CanonicalDependencies[name]...))
	}
	return out
}

func reportExitCode(result *RunResult) int {
	if result == nil {
		return 2
	}
	if result.Failed > 0 || result.Fatal != nil {
		return 1
	}
	if result.Passed == 0 {
		return 2
	}
	return 0
}
