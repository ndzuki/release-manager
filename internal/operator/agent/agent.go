// Package agent executes commands received from the operator control stream.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"strings"
	"time"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	operatork8s "github.com/ndzuki/release-manager/internal/operator/k8s"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/ndzuki/release-manager/internal/operator/observer"
	"github.com/ndzuki/release-manager/internal/operator/secretmetadata"
	"google.golang.org/protobuf/encoding/protojson"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

const defaultInstallTimeout = 5 * time.Minute

// Stream is the operator-side command stream contract used by Agent.
type Stream interface {
	Send(*operatorv1.CommandStreamRequest) error
	Receive() (*operatorv1.CommandStreamResponse, error)
	CloseRequest() error
	CloseResponse() error
}

// StreamClient creates operator command streams.
type StreamClient interface {
	CommandStream(context.Context) Stream
}

// InventoryNotifier schedules a targeted inventory update after a successful operation.
type InventoryNotifier interface {
	NotifyOperationComplete(namespace, releaseName, operationID, definitionID string)
}

// InventorySyncExecutor performs one full inventory snapshot on demand.
type InventorySyncExecutor interface {
	SyncNow(context.Context) error
}

// EmergencyExecutor applies one typed Kubernetes emergency command.
type EmergencyExecutor interface {
	Execute(context.Context, *operatorv1.EmergencyCommand) (string, error)
}

// Agent receives durable commands, executes Helm operations, and returns cached results on redelivery.
type Agent struct {
	client            StreamClient
	engine            helmengine.Engine
	store             localstore.Store
	notifier          InventoryNotifier
	syncExecutor      InventorySyncExecutor
	secrets           corev1client.CoreV1Interface
	emergencyExecutor EmergencyExecutor
	secretLister      secretmetadata.Lister
	observer          observer.RolloutObserver
	kubeClient        kubernetes.Interface
	sessionID         string
	operatorID        string
	logger            *slog.Logger
	installFlags      InstallFlags
	registryPlainHTTP bool
}

// InstallFlags contains operator-wide defaults for INSTALL commands.
type InstallFlags struct {
	Atomic  bool
	Timeout time.Duration
}

// Config contains Agent dependencies and session identity.
type Config struct {
	Client            StreamClient
	Engine            helmengine.Engine
	Store             localstore.Store
	Notifier          InventoryNotifier
	SyncExecutor      InventorySyncExecutor
	Secrets           corev1client.CoreV1Interface
	EmergencyExecutor EmergencyExecutor
	SecretLister      secretmetadata.Lister
	// Observer watches workload rollouts after standard operations and
	// reports ready/desired counters (REQ-077). Nil disables rollout
	// progress reporting; when set, KubeClient must also be set.
	Observer     observer.RolloutObserver
	KubeClient   kubernetes.Interface
	SessionID    string
	OperatorID   string
	Logger       *slog.Logger
	InstallFlags InstallFlags
	// RegistryPlainHTTP allows OCI chart pulls from a plain HTTP registry
	// (dev fixture only; production keeps HTTPS — see helmengine options).
	RegistryPlainHTTP bool
}

// Result is persisted locally and sent to the orchestrator for idempotent replay.
type Result struct {
	OperationID     string                    `json:"operation_id"`
	CommandID       string                    `json:"command_id"`
	DefinitionID    string                    `json:"definition_id"`
	Status          string                    `json:"status"`
	Upgrade         *operatorv1.UpgradeResult `json:"upgrade,omitempty"`
	Code            string                    `json:"code,omitempty"`
	Message         string                    `json:"message,omitempty"`
	Release         *helmengine.Release       `json:"release,omitempty"`
	Secrets         []secretmetadata.Secret   `json:"secrets,omitempty"`
	InventorySync   bool                      `json:"inventory_sync_hint"`
	ResourceSummary ResourceSummary           `json:"resource_summary"`
}

// ResourceSummary contains non-sensitive output metadata.
type ResourceSummary struct {
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// New creates an Agent.
func New(cfg Config) (*Agent, error) {
	switch {
	case cfg.Client == nil:
		return nil, errors.New("agent client is required")
	case cfg.Engine == nil:
		return nil, errors.New("agent engine is required")
	case cfg.Store == nil:
		return nil, errors.New("agent store is required")
	case cfg.SessionID == "":
		return nil, errors.New("agent session_id is required")
	case cfg.OperatorID == "":
		return nil, errors.New("agent operator_id is required")
	case cfg.Observer != nil && cfg.KubeClient == nil:
		return nil, errors.New("agent observer requires kube client")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.InstallFlags.Timeout <= 0 {
		cfg.InstallFlags.Timeout = defaultInstallTimeout
	}

	return &Agent{
		client:            cfg.Client,
		engine:            cfg.Engine,
		store:             cfg.Store,
		notifier:          cfg.Notifier,
		syncExecutor:      cfg.SyncExecutor,
		secrets:           cfg.Secrets,
		emergencyExecutor: cfg.EmergencyExecutor,
		secretLister:      cfg.SecretLister,
		observer:          cfg.Observer,
		kubeClient:        cfg.KubeClient,
		sessionID:         cfg.SessionID,
		operatorID:        cfg.OperatorID,
		logger:            logger,
		installFlags:      cfg.InstallFlags,
		registryPlainHTTP: cfg.RegistryPlainHTTP,
	}, nil
}

// Run connects to CommandStream and processes commands until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	// TASK-084 AC-084-03: every connection derives its own context so a
	// revoked stream cancels the in-flight Helm call (engine Upgrade/Status
	// honour ctx) and the resulting helm_cancelled typed result is persisted
	// locally before the stream dies — the reconnect replay then delivers the
	// agent acknowledgement (ADR-005 local persistence + replay).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lastSequence, err := a.store.LastSequence(ctx)
	if err != nil {
		return fmt.Errorf("load last command sequence: %w", err)
	}

	stream := a.client.CommandStream(ctx)
	defer stream.CloseResponse() //nolint:errcheck // stream is already terminating

	if err := stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{
				SessionId:        a.sessionID,
				OperatorId:       a.operatorID,
				LastSeenSequence: lastSequence,
			},
		},
	}); err != nil {
		return fmt.Errorf("send operator hello: %w", err)
	}

	if err := a.replayActive(ctx, stream); err != nil {
		return err
	}

	for {
		response, err := stream.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive operator command: %w", err)
		}

		switch {
		case response.GetCommand() != nil:
			if err := a.handleCommand(ctx, stream, response.GetCommand()); err != nil {
				return err
			}
		case response.GetEmergencyCommand() != nil:
			if err := a.handleEmergencyCommand(ctx, stream, response.GetEmergencyCommand()); err != nil {
				return err
			}
		case response.GetResyncRequest() != nil:
			lastSequence, err := a.store.LastSequence(ctx)
			if err != nil {
				return fmt.Errorf("load sequence for resync: %w", err)
			}
			if err := stream.Send(&operatorv1.CommandStreamRequest{
				Payload: &operatorv1.CommandStreamRequest_ResyncResponse{
					ResyncResponse: &operatorv1.ResyncResponse{OperatorLastSequence: lastSequence},
				},
			}); err != nil {
				return fmt.Errorf("send resync response: %w", err)
			}
		case response.GetDuplicateResponse() != nil:
			a.logger.Debug("received duplicate command result",
				"command_id", response.GetDuplicateResponse().GetCommandId(),
			)
		case response.GetSessionEvent() != nil:
			return fmt.Errorf("operator session %s: %s",
				response.GetSessionEvent().GetType(),
				response.GetSessionEvent().GetMessage(),
			)
		}
	}
}

func (a *Agent) replayActive(ctx context.Context, stream Stream) error {
	entries, err := a.store.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active commands: %w", err)
	}
	for _, entry := range entries {
		var executeErr error
		if entry.OperationType == "EMERGENCY" {
			executeErr = a.replayEmergencyEntry(ctx, stream, entry)
		} else {
			executeErr = a.executeEntry(ctx, stream, entry)
		}
		if executeErr != nil {
			return fmt.Errorf("replay command %q: %w", entry.CommandID, executeErr)
		}
	}
	return nil
}

func (a *Agent) replayEmergencyEntry(ctx context.Context, stream Stream, entry *localstore.CommandEntry) error {
	var command operatorv1.EmergencyCommand
	if err := proto.Unmarshal(entry.Payload, &command); err != nil {
		return a.finishEmergencyFailure(ctx, stream, entry, "invalid_command", "invalid emergency command payload")
	}
	if entry.Status == localstore.StatusRunning {
		return a.executeEmergencyEntry(ctx, stream, entry)
	}
	return stream.Send(emergencyAckRequest(command.GetCommandId(), operatorv1.AckType_ACK_TYPE_PERSISTED))
}

func (a *Agent) handleEmergencyCommand(ctx context.Context, stream Stream, command *operatorv1.EmergencyCommand) error {
	if command == nil || command.GetCommandId() == "" {
		return errors.New("received emergency command without command_id")
	}
	existing, err := a.store.Get(ctx, command.GetCommandId())
	if err == nil {
		if localstore.IsTerminal(existing.Status) {
			return a.sendCachedEmergencyResult(stream, command, existing)
		}
		return a.executeEmergencyEntry(ctx, stream, existing)
	}
	if !errors.Is(err, localstore.ErrNotFound) {
		return fmt.Errorf("lookup emergency command %q: %w", command.GetCommandId(), err)
	}
	payload, err := proto.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal emergency command %q: %w", command.GetCommandId(), err)
	}
	entry := &localstore.CommandEntry{
		CommandID: command.GetCommandId(), OperationID: command.GetOperationId(),
		OperationType: "EMERGENCY", Payload: payload, Status: localstore.StatusPending,
	}
	if err := a.store.Save(ctx, entry); err != nil {
		return fmt.Errorf("persist emergency command %q: %w", command.GetCommandId(), err)
	}
	if err := stream.Send(emergencyAckRequest(command.GetCommandId(), operatorv1.AckType_ACK_TYPE_PERSISTED)); err != nil {
		return fmt.Errorf("ack persisted emergency command %q: %w", command.GetCommandId(), err)
	}
	return a.executeEmergencyEntry(ctx, stream, entry)
}

func (a *Agent) executeEmergencyEntry(ctx context.Context, stream Stream, entry *localstore.CommandEntry) error {
	var command operatorv1.EmergencyCommand
	if err := proto.Unmarshal(entry.Payload, &command); err != nil {
		return a.finishEmergencyFailure(ctx, stream, entry, "invalid_command", "invalid emergency command payload")
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusRunning, ""); err != nil {
		return fmt.Errorf("mark emergency command %q running: %w", entry.CommandID, err)
	}
	if a.emergencyExecutor == nil {
		return a.finishEmergencyFailure(ctx, stream, entry, "emergency_executor_unavailable", "emergency executor is unavailable")
	}
	resultJSON, execErr := a.emergencyExecutor.Execute(ctx, &command)
	if execErr != nil {
		return a.finishEmergencyFailure(ctx, stream, entry, emergencyErrorCode(execErr), execErr.Error())
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusSucceeded, resultJSON); err != nil {
		return fmt.Errorf("persist emergency result %q: %w", entry.CommandID, err)
	}
	return stream.Send(emergencyResultRequest(&command, "succeeded", "", "", resultJSON))
}

func (a *Agent) finishEmergencyFailure(ctx context.Context, stream Stream, entry *localstore.CommandEntry, code, message string) error {
	persisted := emergencyStoredResult{Status: "failed", ErrorCode: code, Message: message}
	encoded, err := json.Marshal(persisted)
	if err != nil {
		return fmt.Errorf("marshal emergency failure: %w", err)
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusFailed, string(encoded)); err != nil {
		return fmt.Errorf("persist emergency failure: %w", err)
	}
	command := &operatorv1.EmergencyCommand{CommandId: entry.CommandID, OperationId: entry.OperationID}
	return stream.Send(emergencyResultRequest(command, "failed", code, message, ""))
}

func (a *Agent) sendCachedEmergencyResult(stream Stream, command *operatorv1.EmergencyCommand, entry *localstore.CommandEntry) error {
	if entry.Status == localstore.StatusSucceeded {
		return stream.Send(emergencyResultRequest(command, "succeeded", "", "", entry.ResultJSON))
	}
	var cached emergencyStoredResult
	if err := json.Unmarshal([]byte(entry.ResultJSON), &cached); err != nil {
		return fmt.Errorf("decode cached emergency result %q: %w", command.GetCommandId(), err)
	}
	return stream.Send(emergencyResultRequest(command, cached.Status, cached.ErrorCode, cached.Message, ""))
}

type emergencyStoredResult struct {
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func emergencyErrorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return "emergency_update_failed"
}

func (a *Agent) handleCommand(ctx context.Context, stream Stream, command *operatorv1.Command) error {
	if command.GetCommandId() == "" {
		return errors.New("received command without command_id")
	}

	existing, err := a.store.Get(ctx, command.GetCommandId())
	if err == nil {
		if localstore.IsTerminal(existing.Status) {
			return a.sendCachedResult(stream, command, existing.ResultJSON)
		}
		return a.executeEntry(ctx, stream, existing)
	}
	if !errors.Is(err, localstore.ErrNotFound) {
		return fmt.Errorf("lookup command %q: %w", command.GetCommandId(), err)
	}

	payload, err := protojson.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal command %q: %w", command.GetCommandId(), err)
	}
	entry := &localstore.CommandEntry{
		CommandID:     command.GetCommandId(),
		OutboxID:      command.GetOutboxId(),
		OperationID:   command.GetOperationId(),
		OperationType: command.GetOperationType(),
		Sequence:      command.GetSequence(),
		Payload:       payload,
		Status:        localstore.StatusPending,
	}
	if err := a.store.Save(ctx, entry); err != nil {
		return fmt.Errorf("persist command %q: %w", command.GetCommandId(), err)
	}

	if err := stream.Send(ackRequest(command, operatorv1.AckType_ACK_TYPE_PERSISTED)); err != nil {
		return fmt.Errorf("ack persisted command %q: %w", command.GetCommandId(), err)
	}
	return a.executeEntry(ctx, stream, entry)
}

func (a *Agent) executeEntry(ctx context.Context, stream Stream, entry *localstore.CommandEntry) error {
	var command operatorv1.Command
	if err := protojson.Unmarshal(entry.Payload, &command); err != nil {
		if legacyErr := json.Unmarshal(entry.Payload, &command); legacyErr != nil {
			return a.finishFailure(ctx, stream, entry, Result{
				OperationID: entry.OperationID,
				CommandID:   entry.CommandID,
				Status:      "failed",
				Code:        "invalid_command",
				Message:     "invalid command payload",
			})
		}
	}

	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusRunning, ""); err != nil {
		return fmt.Errorf("mark command %q running: %w", entry.CommandID, err)
	}

	reporter := newRolloutReporter(stream, command.GetOperationId(), a.logger)
	result := a.execute(ctx, &command, reporter)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal command result %q: %w", entry.CommandID, err)
	}

	localStatus := localstore.StatusSucceeded
	if result.Status == "failed" {
		localStatus = localstore.StatusFailed
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localStatus, string(resultJSON)); err != nil {
		return fmt.Errorf("persist command result %q: %w", entry.CommandID, err)
	}

	if result.Status == "succeeded" && result.Release != nil && a.notifier != nil {
		a.notifier.NotifyOperationComplete(
			result.Release.Namespace,
			result.Release.Name,
			result.OperationID,
			result.DefinitionID,
		)
	}

	if command.GetOperationType() == "UPGRADE" {
		return stream.Send(commandResultRequest(&command, result))
	}
	return stream.Send(resultRequest(&command, result, resultJSON))
}

func (a *Agent) execute(ctx context.Context, command *operatorv1.Command, reporter *rolloutReporter) Result {
	result := Result{
		OperationID:  command.GetOperationId(),
		CommandID:    command.GetCommandId(),
		Status:       "failed",
		DefinitionID: command.GetDefinitionId(),
	}

	switch command.GetOperationType() {
	case "INSTALL":
		return a.executeInstall(ctx, command, reporter)
	case "UPGRADE":
		return a.executeUpgrade(ctx, command, result, reporter)
	case "ROLLBACK":
		return a.executeRollback(ctx, command, reporter)
	case "INVENTORY_SYNC":
		return a.executeInventorySync(ctx, command)
	case commandtype.SecretMetadataList:
		return a.executeSecretMetadataList(ctx, command)
	default:
		result.Code = "unsupported_command"
		result.Message = fmt.Sprintf("unsupported command type %q", command.GetOperationType())
		return result
	}
}

func (a *Agent) executeInventorySync(ctx context.Context, command *operatorv1.Command) Result {
	result := Result{
		OperationID: command.GetOperationId(),
		CommandID:   command.GetCommandId(),
		Status:      "failed",
	}
	if a.syncExecutor == nil {
		result.Code = "inventory_sync_unavailable"
		result.Message = "inventory sync executor is unavailable"
		return result
	}
	if err := a.syncExecutor.SyncNow(ctx); err != nil {
		result.Code = "inventory_sync_failed"
		result.Message = err.Error()
		return result
	}
	result.Status = "succeeded"
	result.InventorySync = true
	return result
}

func (a *Agent) executeSecretMetadataList(ctx context.Context, command *operatorv1.Command) Result {
	result := Result{
		OperationID:  command.GetOperationId(),
		CommandID:    command.GetCommandId(),
		DefinitionID: command.GetDefinitionId(),
		Status:       "failed",
	}
	if a.secretLister == nil {
		result.Code = "secret_metadata_unavailable"
		result.Message = "secret metadata lister is unavailable"
		return result
	}
	secrets, err := a.secretLister.List(ctx, command.GetNamespace())
	if err != nil {
		result.Code = "secret_metadata_list_failed"
		result.Message = err.Error()
		return result
	}
	result.Status = "succeeded"
	result.Secrets = secrets
	return result
}

// applyBundleImageOverrides sets each bundle image at its values path. Only
// FULL_REFERENCE is supported by the dev fixture; the value is the immutable
// ref@digest the chart should deploy. Mirrors the orchestrator's
// normalizeImageReference semantics so the rendered manifest matches what
// preflight verified.
func applyBundleImageOverrides(values map[string]interface{}, images []*commonv1.BundleImage) error {
	for _, image := range images {
		if image.GetRef() == "" || image.GetDigest() == "" || image.GetValuesPath() == "" {
			return fmt.Errorf("bundle image ref, digest and values_path are required")
		}
		fullRef := image.GetRef()
		if !strings.Contains(fullRef, "@") {
			fullRef = image.GetRef() + "@" + image.GetDigest()
		}
		if err := setValuesPath(values, image.GetValuesPath(), fullRef); err != nil {
			return err
		}
	}
	return nil
}

// setValuesPath sets a JSON-style dotted path on a values map (creates
// intermediate objects).
func setValuesPath(values map[string]interface{}, path, value string) error {
	parts := strings.Split(path, ".")
	if path == "" || value == "" {
		return fmt.Errorf("image override path and value are required")
	}
	current := values
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]interface{})
		if !ok {
			child := map[string]interface{}{}
			current[part] = child
			next = child
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
	return nil
}

func (a *Agent) executeInstall(ctx context.Context, command *operatorv1.Command, reporter *rolloutReporter) Result {
	result := Result{
		OperationID:  command.GetOperationId(),
		CommandID:    command.GetCommandId(),
		Status:       "failed",
		DefinitionID: command.GetDefinitionId(),
	}
	if command.GetBundle() == nil || command.GetBundle().GetChartRef() == "" {
		result.Code = "invalid_command"
		result.Message = "chart_ref is required"
		return result
	}
	if command.GetNamespace() == "" || command.GetReleaseName() == "" {
		result.Code = "invalid_command"
		result.Message = "namespace and release_name are required"
		return result
	}

	values := map[string]interface{}{}
	if len(command.GetValues()) > 0 {
		if err := json.Unmarshal(command.GetValues(), &values); err != nil {
			result.Code = "invalid_command"
			result.Message = "values must be canonical JSON"
			return result
		}
	}
	// The bundle's image overrides (values_path + FULL_REFERENCE ref@digest)
	// must be applied to the installed values: the orchestrator's effective
	// values only merge the approved document + patch, the image override is
	// carried in the bundle. Without it the chart deploys its static
	// image.repository (real smoke 2026-08-27: bare localhost:5001/
	// release-fixture → ErrImagePull :latest).
	if err := applyBundleImageOverrides(values, command.GetBundle().GetImages()); err != nil {
		result.Code = "invalid_command"
		result.Message = err.Error()
		return result
	}

	timeout := a.installFlags.Timeout
	if command.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(command.GetTimeoutSeconds()) * time.Second
	}

	started := time.Now()
	// Idempotent install (real smoke 2026-08-27): the preflight pipeline
	// dispatches artifact/render/cluster as INSTALL-typed commands and the
	// wire Command does not carry the stage, so the agent runs a real install
	// for each. The first stage installs the release; later stages find it
	// already deployed. Check Status first and replay a deployed release as
	// success — a genuine Install on an existing release would fail with
	// ErrAlreadyExists. ErrAlreadyExists from Install still maps to
	// release_already_exists (a true conflict), so the error contract is
	// preserved.
	if existing, statusErr := a.engine.Status(ctx, helmengine.StatusOptions{
		Namespace:   command.GetNamespace(),
		ReleaseName: command.GetReleaseName(),
	}); statusErr == nil && existing != nil && existing.Status == "deployed" {
		a.logger.Info("release already deployed; install replayed as success",
			"namespace", command.GetNamespace(), "release", command.GetReleaseName(), "command", command.GetCommandId())
		result.Status = "succeeded"
		result.InventorySync = true
		if existing.ManifestDigest != "" {
			result.ResourceSummary.ManifestDigest = existing.ManifestDigest
		}
		return result
	}
	release, err := a.engine.Install(ctx, helmengine.InstallOptions{
		Namespace:       command.GetNamespace(),
		ReleaseName:     command.GetReleaseName(),
		ChartPath:       command.GetBundle().GetChartRef(),
		ChartVersion:    command.GetBundle().GetChartVersion(),
		Values:          values,
		Atomic:          a.installFlags.Atomic,
		CreateNamespace: command.GetCreateNamespace(),
		PlainHTTP:       a.registryPlainHTTP,
		Timeout:         timeout,
	})
	if err != nil {
		result.Code = installErrorCode(err)
		result.Message = err.Error()
		return result
	}

	// REQ-077 Q2: observe synchronously within the remaining time budget —
	// progress (including the terminal flush) reaches the stream before the
	// Result below terminalizes the operation.
	a.observeRollout(ctx, command.GetOperationId(), release.Workloads, timeout-time.Since(started), reporter)

	result.Status = "succeeded"
	result.Release = release
	result.InventorySync = true
	result.ResourceSummary.ManifestDigest = release.ManifestDigest
	return result
}

func releaseSnapshot(release *helmengine.Release) *operatorv1.ReleaseSnapshot {
	if release == nil {
		return nil
	}
	provenance := operatorv1.ReleaseProvenance_RELEASE_PROVENANCE_LEGACY
	if release.Provenance == "managed" {
		provenance = operatorv1.ReleaseProvenance_RELEASE_PROVENANCE_MANAGED
	}
	return &operatorv1.ReleaseSnapshot{
		HelmRevision:          uint64(release.Revision), //nolint:gosec // Helm revisions are positive SDK ints.
		BundleDigest:          release.BundleDigest,
		ChartDigest:           release.ChartDigest,
		EffectiveValuesDigest: release.EffectiveValuesDigest,
		ManifestDigest:        release.ManifestDigest,
		Provenance:            provenance,
		Status:                release.Status,
	}
}

func (a *Agent) executeUpgrade(ctx context.Context, command *operatorv1.Command, result Result, reporter *rolloutReporter) Result {
	upgrade, valuesMap, fromRelease, secretDigest, ok := a.resolveUpgradeInputs(ctx, command, &result)
	if !ok {
		// TASK-084 AC-084-02: resolve failures also carry the typed payload
		// with the empty snapshot semantics — the gateway must never see an
		// UPGRADE result without CommandResult.upgrade.
		ensureUpgradeResult(&result, nil)
		return result
	}

	timeout := 5 * time.Minute
	if upgrade.GetTimeout() != nil {
		timeout = upgrade.GetTimeout().AsDuration()
	}
	started := time.Now()

	current, err := a.engine.Status(ctx, helmengine.StatusOptions{
		// TASK-084 AC-084-01: the typed UpgradeCommand is the single authority
		// for the Helm release identity of this execute entry. The top-level
		// wire fields are empty for UPGRADE and previously produced an
		// invalid Helm release on this second Status (D-109 repro).
		Namespace:   upgrade.GetNamespace(),
		ReleaseName: upgrade.GetReleaseName(),
	})
	if err != nil {
		result.Code = upgradeErrorCode(err)
		result.Message = err.Error()
		ensureUpgradeResult(&result, fromRelease)
		return result
	}
	if expected := int(command.GetExpectedCurrentRevision()); expected > 0 && current.Revision != expected {
		result.Code = "inventory_stale"
		result.Message = fmt.Sprintf("expected current revision %d, got %d", expected, current.Revision)
		ensureUpgradeResult(&result, fromRelease)
		return result
	}

	release, err := a.engine.Upgrade(ctx, helmengine.UpgradeOptions{
		Namespace:             upgrade.GetNamespace(),
		ReleaseName:           upgrade.GetReleaseName(),
		ChartPath:             upgrade.GetChart().GetResolvedUri(),
		ChartVersion:          upgrade.GetChart().GetVersion(),
		Values:                valuesMap,
		ExpectedRevision:      int(upgrade.GetExpectedRevision()), //nolint:gosec // validated positive SDK revision.
		Atomic:                true,
		MaxHistory:            int(upgrade.GetMaxHistory()),
		Timeout:               timeout,
		OperationID:           upgrade.GetOperationId(),
		CommandID:             upgrade.GetCommandId(),
		BundleDigest:          upgrade.GetBundle().GetBundleDigest(),
		ChartDigest:           upgrade.GetChart().GetDigest(),
		EffectiveValuesDigest: upgrade.GetEffectiveValuesDigest(),
		PlainHTTP:             a.registryPlainHTTP,
		SecretSnapshotDigest:  secretDigest,
		ResetValues:           true,
		ReuseValues:           false,
		CleanupOnFail:         false,
		WaitForJobs:           true,
		TakeOwnership:         false,
	})
	if err != nil {
		result.Code = upgradeErrorCode(err)
		result.Message = upgradeErrorMessage(result.Code)
		result.Release = release
		// TASK-084 AC-084-04: the engine's structured outcome is the only
		// authoritative rollback signal. The legacy revision-equality
		// heuristic fabricated rollback_succeeded even when no rollback had
		// happened (D-109); a non-decorated error now maps to
		// RollbackSucceeded=false (fail closed, ADR-008).
		//
		// Restored is taken verbatim — the engine established it by observing
		// the post-failure state (active deployed and rolled off the failed
		// revision). No revision re-check is applied here: the Helm SDK's
		// atomic rollback restores the pre-upgrade config under a NEW
		// revision (real-SDK evidence: upgrade revision 2, restored active
		// revision 3), so comparing active.Revision to from.Revision would
		// misclassify a real restore as not-restored.
		outcome := helmengine.OutcomeOf(err)
		attempted, active := release, release
		if outcome.Attempted != nil {
			attempted = outcome.Attempted
		}
		if outcome.Active != nil {
			active = outcome.Active
		}
		rollbackSucceeded := outcome.Restored
		result.Upgrade = &operatorv1.UpgradeResult{
			From:              releaseSnapshot(fromRelease),
			Attempted:         releaseSnapshot(attempted),
			Active:            releaseSnapshot(active),
			RollbackSucceeded: rollbackSucceeded,
			ResourceSummary: &operatorv1.ResourceSummary{
				ManifestDigest: manifestDigest(active),
			},
		}
		result.ResourceSummary.ManifestDigest = manifestDigest(active)
		return result
	}

	result.Status = "succeeded"
	result.Upgrade = &operatorv1.UpgradeResult{
		From:               releaseSnapshot(fromRelease),
		Attempted:          releaseSnapshot(release),
		Active:             releaseSnapshot(release),
		RollbackSucceeded:  false,
		RolloutTrackingRef: upgrade.GetOperationId(),
		ResourceSummary: &operatorv1.ResourceSummary{
			ManifestDigest: release.ManifestDigest,
		},
	}
	result.Release = release
	result.InventorySync = true
	result.ResourceSummary.ManifestDigest = release.ManifestDigest
	// REQ-077 Q2: observe with the command's remaining time budget.
	a.observeRollout(ctx, command.GetOperationId(), release.Workloads, timeout-time.Since(started), reporter)
	return result
}

// resolveUpgradeInputs validates the upgrade command and resolves the inputs
// shared by the execution path: effective values (digest + JSON), the current
// release (expected revision check), and the secret snapshot. Failures write
// the result code/message and report ok=false.
func (a *Agent) resolveUpgradeInputs(ctx context.Context, command *operatorv1.Command, result *Result) (upgrade *operatorv1.UpgradeCommand, valuesMap map[string]interface{}, fromRelease *helmengine.Release, secretDigest string, ok bool) {
	upgrade = command.GetUpgrade()
	if command.GetPayloadVersion() != 2 || upgrade == nil {
		result.Code = "unsupported_command_version"
		result.Message = "upgrade payload_version 2 is required"
		return nil, nil, nil, "", false
	}
	// TASK-084 AC-084-01: fail closed before any Helm SDK call when the typed
	// identity is incomplete (ADR-008) — an empty namespace/release_name must
	// never reach engine.Status/Upgrade and produce an invalid Helm release.
	if upgrade.GetNamespace() == "" || upgrade.GetReleaseName() == "" {
		result.Code = "invalid_command"
		result.Message = "upgrade namespace and release_name are required"
		return nil, nil, nil, "", false
	}
	valuesDigest := sha256Hex(upgrade.GetEffectiveValuesJson())
	if valuesDigest != strings.TrimPrefix(upgrade.GetEffectiveValuesDigest(), "sha256:") {
		result.Code = "digest_mismatch"
		result.Message = "effective values digest mismatch"
		return nil, nil, nil, "", false
	}
	valuesMap = map[string]interface{}{}
	if err := json.Unmarshal(upgrade.GetEffectiveValuesJson(), &valuesMap); err != nil {
		result.Code = "invalid_command"
		result.Message = "effective values must be canonical JSON"
		return nil, nil, nil, "", false
	}
	fromRelease, statusErr := a.engine.Status(ctx, helmengine.StatusOptions{
		Namespace:   upgrade.GetNamespace(),
		ReleaseName: upgrade.GetReleaseName(),
	})
	if statusErr != nil {
		result.Code = upgradeErrorCode(statusErr)
		result.Message = upgradeErrorMessage(result.Code)
		return nil, nil, nil, "", false
	}
	if upgrade.GetExpectedRevision() > 0 && uint64(fromRelease.Revision) != upgrade.GetExpectedRevision() { //nolint:gosec // Helm revisions are positive SDK ints.
		result.Code = "revision_conflict"
		result.Message = upgradeErrorMessage(result.Code)
		return nil, nil, nil, "", false
	}
	secretDigest, err := operatork8s.Resolve(ctx, a.secrets, upgrade.GetNamespace(), upgrade.GetSecretRefs(), valuesMap)
	if err != nil {
		result.Code = upgradeErrorCode(err)
		result.Message = upgradeErrorMessage(result.Code)
		return nil, nil, nil, "", false
	}
	return upgrade, valuesMap, fromRelease, secretDigest, true
}

func manifestDigest(release *helmengine.Release) string {
	if release == nil {
		return ""
	}
	return release.ManifestDigest
}

// ensureUpgradeResult attaches the typed UpgradeResult to an UPGRADE result
// that does not carry one yet (TASK-084 AC-084-02 / REQ-084 output contract:
// success, failure and cancellation results all carry CommandResult.upgrade).
// from may be nil and keeps the existing empty snapshot semantics; the gateway
// tolerates empty active snapshots on failed results.
func ensureUpgradeResult(result *Result, from *helmengine.Release) {
	if result.Upgrade != nil {
		return
	}
	result.Upgrade = &operatorv1.UpgradeResult{From: releaseSnapshot(from)}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func (a *Agent) executeRollback(ctx context.Context, command *operatorv1.Command, reporter *rolloutReporter) Result {
	result := Result{
		OperationID:  command.GetOperationId(),
		CommandID:    command.GetCommandId(),
		Status:       "failed",
		DefinitionID: command.GetDefinitionId(),
	}
	if command.GetTargetRevision() <= 0 {
		result.Code = "invalid_command"
		result.Message = "target_revision is required for rollback"
		return result
	}
	if command.GetNamespace() == "" || command.GetReleaseName() == "" {
		result.Code = "invalid_command"
		result.Message = "namespace and release_name are required"
		return result
	}

	history, err := a.engine.History(ctx, helmengine.HistoryOptions{
		Namespace:   command.GetNamespace(),
		ReleaseName: command.GetReleaseName(),
	})
	if err != nil {
		result.Code = rollbackErrorCode(err)
		result.Message = err.Error()
		return result
	}
	targetFound := false
	for _, entry := range history {
		if entry.Revision == int(command.GetTargetRevision()) {
			targetFound = true
			break
		}
	}
	if !targetFound {
		result.Code = "target_revision_not_found"
		result.Message = fmt.Sprintf("target revision %d not found", command.GetTargetRevision())
		return result
	}

	timeout := a.installFlags.Timeout
	if command.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(command.GetTimeoutSeconds()) * time.Second
	}
	started := time.Now()
	release, err := a.engine.Rollback(ctx, helmengine.RollbackOptions{
		Namespace: command.GetNamespace(), ReleaseName: command.GetReleaseName(),
		TargetRevision: int(command.GetTargetRevision()), Timeout: timeout,
	})
	if err != nil {
		result.Code = rollbackErrorCode(err)
		result.Message = err.Error()
		return result
	}
	result.Status = "succeeded"
	result.Release = release
	result.InventorySync = true
	result.ResourceSummary.ManifestDigest = release.ManifestDigest
	// REQ-077 Q2: observe with the command's remaining time budget.
	a.observeRollout(ctx, command.GetOperationId(), release.Workloads, timeout-time.Since(started), reporter)
	return result
}

func (a *Agent) finishFailure(ctx context.Context, stream Stream, entry *localstore.CommandEntry, result Result) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal failure result: %w", err)
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusFailed, string(resultJSON)); err != nil {
		return fmt.Errorf("persist failure result: %w", err)
	}

	command := &operatorv1.Command{
		OutboxId:      entry.OutboxID,
		CommandId:     entry.CommandID,
		OperationId:   entry.OperationID,
		Sequence:      entry.Sequence,
		OperationType: entry.OperationType,
	}
	if command.GetOperationType() == "UPGRADE" {
		// TASK-084 AC-084-02: even a locally corrupted UPGRADE payload must
		// report a typed result — the gateway rejects UPGRADE results without
		// CommandResult.upgrade and the operation would stay RUNNING forever.
		ensureUpgradeResult(&result, nil)
		return stream.Send(commandResultRequest(command, result))
	}
	return stream.Send(resultRequest(command, result, resultJSON))
}

func commandResultRequest(command *operatorv1.Command, result Result) *operatorv1.CommandStreamRequest {
	commandResult := &operatorv1.CommandResult{
		CommandId:   command.GetCommandId(),
		OperationId: command.GetOperationId(),
		Status:      result.Status,
	}
	if command.GetOperationType() == "UPGRADE" && result.Upgrade == nil {
		// TASK-084 AC-084-02 wire-edge assertion: UPGRADE results are never
		// sent without the typed payload. An empty UpgradeResult is the
		// documented empty snapshot semantics, not a gateway rejection.
		result.Upgrade = &operatorv1.UpgradeResult{}
	}
	if result.Upgrade != nil {
		commandResult.Result = &operatorv1.CommandResult_Upgrade{Upgrade: result.Upgrade}
	}
	if result.Code != "" {
		commandResult.Error = &operatorv1.ExecutionError{
			Code:      result.Code,
			Message:   result.Message,
			Retryable: result.Code == "revision_conflict" || result.Code == "release_busy",
		}
	}
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_CommandResult{CommandResult: commandResult},
	}
}

func (a *Agent) sendCachedResult(stream Stream, command *operatorv1.Command, resultJSON string) error {
	var result Result
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return fmt.Errorf("decode cached result for %q: %w", command.GetCommandId(), err)
	}
	if command.GetOperationType() == "UPGRADE" {
		return stream.Send(commandResultRequest(command, result))
	}
	return stream.Send(resultRequest(command, result, []byte(resultJSON)))
}

func ackRequest(command *operatorv1.Command, ackType operatorv1.AckType) *operatorv1.CommandStreamRequest {
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Ack{
			Ack: &operatorv1.Ack{
				OutboxId:  command.GetOutboxId(),
				CommandId: command.GetCommandId(),
				Sequence:  command.GetSequence(),
				AckType:   ackType,
			},
		},
	}
}

func emergencyAckRequest(commandID string, ackType operatorv1.AckType) *operatorv1.CommandStreamRequest {
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_EmergencyAck{EmergencyAck: &operatorv1.EmergencyAck{
			EmergencyCommandId: commandID, AckType: ackType,
		}},
	}
}

func emergencyResultRequest(command *operatorv1.EmergencyCommand, status, errorCode, message, resultJSON string) *operatorv1.CommandStreamRequest {
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_EmergencyResult{EmergencyResult: &operatorv1.EmergencyResult{
			EmergencyCommandId: command.GetCommandId(), OperationId: command.GetOperationId(), Status: status,
			ErrorCode: errorCode, Message: message, ResultJson: resultJSON,
		}},
	}
}

func resultRequest(command *operatorv1.Command, result Result, resultJSON []byte) *operatorv1.CommandStreamRequest {
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Result{
			Result: &operatorv1.Result{
				OutboxId:   command.GetOutboxId(),
				CommandId:  command.GetCommandId(),
				Status:     result.Status,
				Message:    result.Message,
				Output:     resultJSON,
				Sequence:   command.GetSequence(),
				ResultJson: string(resultJSON),
			},
		},
	}
}

func installErrorCode(err error) string {
	switch {
	case errors.Is(err, helmengine.ErrAlreadyExists):
		return "release_already_exists"
	case errors.Is(err, helmengine.ErrForbidden):
		return "forbidden"
	case errors.Is(err, helmengine.ErrTimeout):
		return "timeout"
	case errors.Is(err, helmengine.ErrCancelled):
		return "cancelled"
	default:
		return "helm_install_failed"
	}
}

func upgradeErrorCode(err error) string {
	switch {
	case errors.Is(err, helmengine.ErrNotFound):
		return "release_not_found"
	case errors.Is(err, helmengine.ErrReleaseBusy):
		return "release_busy"
	case errors.Is(err, helmengine.ErrReleaseNotDeployed):
		return "release_not_deployed"
	case errors.Is(err, helmengine.ErrConflict):
		return "revision_conflict"
	case errors.Is(err, helmengine.ErrDigestMismatch):
		return "digest_mismatch"
	case errors.Is(err, helmengine.ErrSecretRefChanged):
		return "secret_ref_changed"
	case errors.Is(err, helmengine.ErrRenderDrift):
		return "render_drift"
	case errors.Is(err, helmengine.ErrRenderFailed):
		return "render_failed"
	case errors.Is(err, helmengine.ErrSchemaFailed):
		return "schema_failed"
	case errors.Is(err, helmengine.ErrAtomicRollbackFailed):
		return "atomic_rollback_failed"
	case errors.Is(err, helmengine.ErrTimeout):
		return "helm_timeout"
	case errors.Is(err, helmengine.ErrCancelled):
		return "helm_cancelled"
	default:
		return "helm_upgrade_failed"
	}
}

func upgradeErrorMessage(code string) string {
	switch code {
	case "release_not_found":
		return "Helm release was not found"
	case "release_busy":
		return "Helm release has another operation in progress"
	case "release_not_deployed":
		return "Helm release is not deployed"
	case "revision_conflict":
		return "Helm release revision changed"
	case "digest_mismatch":
		return "frozen input digest does not match"
	case "secret_ref_changed":
		return "Secret reference changed after preflight"
	case "render_drift":
		return "rendered manifest changed after preflight"
	case "render_failed":
		return "effective values could not be rendered"
	case "schema_failed":
		return "effective values failed chart schema validation"
	case "atomic_rollback_failed":
		return "Helm upgrade and automatic rollback failed"
	case "helm_timeout":
		return "Helm upgrade timed out"
	case "helm_cancelled":
		return "Helm upgrade was cancelled"
	default:
		return "Helm upgrade failed"
	}
}

func rollbackErrorCode(err error) string {
	switch {
	case errors.Is(err, helmengine.ErrNotFound):
		return "release_not_found"
	case errors.Is(err, helmengine.ErrTimeout):
		return "timeout"
	case errors.Is(err, helmengine.ErrCancelled):
		return "cancelled"
	default:
		return "helm_rollback_failed"
	}
}

// ConnectClient adapts the generated Connect client to StreamClient.
type ConnectClient struct {
	Client operatorv1connect.OperatorServiceClient
}

// CommandStream opens a generated Connect bidirectional stream.
func (c ConnectClient) CommandStream(ctx context.Context) Stream {
	return c.Client.CommandStream(ctx)
}
