// Package agent executes commands received from the operator control stream.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	operatork8s "github.com/ndzuki/release-manager/internal/operator/k8s"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"google.golang.org/protobuf/encoding/protojson"
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

// Agent receives durable commands, executes Helm operations, and returns cached results on redelivery.
type Agent struct {
	client       StreamClient
	engine       helmengine.Engine
	store        localstore.Store
	notifier     InventoryNotifier
	syncExecutor InventorySyncExecutor
	secrets      corev1client.CoreV1Interface
	sessionID    string
	operatorID   string
	logger       *slog.Logger
	installFlags InstallFlags
}

// InstallFlags contains operator-wide defaults for INSTALL commands.
type InstallFlags struct {
	Atomic  bool
	Timeout time.Duration
}

// Config contains Agent dependencies and session identity.
type Config struct {
	Client       StreamClient
	Engine       helmengine.Engine
	Store        localstore.Store
	Secrets      corev1client.CoreV1Interface
	Notifier     InventoryNotifier
	SyncExecutor InventorySyncExecutor
	SessionID    string
	OperatorID   string
	Logger       *slog.Logger
	InstallFlags InstallFlags
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
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.InstallFlags.Timeout <= 0 {
		cfg.InstallFlags.Timeout = defaultInstallTimeout
	}

	return &Agent{
		client:       cfg.Client,
		engine:       cfg.Engine,
		store:        cfg.Store,
		secrets:      cfg.Secrets,
		notifier:     cfg.Notifier,
		syncExecutor: cfg.SyncExecutor,
		sessionID:    cfg.SessionID,
		operatorID:   cfg.OperatorID,
		logger:       logger,
		installFlags: cfg.InstallFlags,
	}, nil
}

// Run connects to CommandStream and processes commands until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
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
		if err := a.executeEntry(ctx, stream, entry); err != nil {
			return fmt.Errorf("replay command %q: %w", entry.CommandID, err)
		}
	}
	return nil
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

	result := a.execute(ctx, &command)
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

func (a *Agent) execute(ctx context.Context, command *operatorv1.Command) Result {
	result := Result{
		OperationID:  command.GetOperationId(),
		CommandID:    command.GetCommandId(),
		Status:       "failed",
		DefinitionID: command.GetDefinitionId(),
	}

	switch command.GetOperationType() {
	case "INSTALL":
		return a.executeInstall(ctx, command)
	case "UPGRADE":
		return a.executeUpgrade(ctx, command, result)
	case "ROLLBACK":
		return a.executeRollback(ctx, command)
	case "INVENTORY_SYNC":
		return a.executeInventorySync(ctx, command)
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

func (a *Agent) executeInstall(ctx context.Context, command *operatorv1.Command) Result {
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

	timeout := a.installFlags.Timeout
	if command.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(command.GetTimeoutSeconds()) * time.Second
	}

	release, err := a.engine.Install(ctx, helmengine.InstallOptions{
		Namespace:       command.GetNamespace(),
		ReleaseName:     command.GetReleaseName(),
		ChartPath:       command.GetBundle().GetChartRef(),
		ChartVersion:    command.GetBundle().GetChartVersion(),
		Values:          values,
		Atomic:          a.installFlags.Atomic,
		CreateNamespace: command.GetCreateNamespace(),
		Timeout:         timeout,
	})
	if err != nil {
		result.Code = installErrorCode(err)
		result.Message = err.Error()
		return result
	}

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
	}
}

func (a *Agent) executeUpgrade(ctx context.Context, command *operatorv1.Command, result Result) Result {
	upgrade := command.GetUpgrade()
	if command.GetPayloadVersion() != 2 || upgrade == nil {
		result.Code = "unsupported_command_version"
		result.Message = "upgrade payload_version 2 is required"
		return result
	}
	valuesDigest := sha256Hex(upgrade.GetEffectiveValuesJson())
	if valuesDigest != strings.TrimPrefix(upgrade.GetEffectiveValuesDigest(), "sha256:") {
		result.Code = "digest_mismatch"
		result.Message = "effective values digest mismatch"
		return result
	}
	valuesMap := map[string]interface{}{}
	if err := json.Unmarshal(upgrade.GetEffectiveValuesJson(), &valuesMap); err != nil {
		result.Code = "invalid_command"
		result.Message = "effective values must be canonical JSON"
		return result
	}
	secretDigest, err := operatork8s.Resolve(ctx, a.secrets, upgrade.GetNamespace(), upgrade.GetSecretRefs(), valuesMap)
	if err != nil {
		result.Code = upgradeErrorCode(err)
		result.Message = err.Error()
		return result
	}
	fromRelease, statusErr := a.engine.Status(ctx, helmengine.StatusOptions{
		Namespace:   upgrade.GetNamespace(),
		ReleaseName: upgrade.GetReleaseName(),
	})
	if statusErr != nil && !errors.Is(statusErr, helmengine.ErrNotFound) {
		result.Code = upgradeErrorCode(statusErr)
		result.Message = statusErr.Error()
		return result
	}

	timeout := 5 * time.Minute
	if upgrade.GetTimeout() != nil {
		timeout = upgrade.GetTimeout().AsDuration()
	}
	release, err := a.engine.Upgrade(ctx, helmengine.UpgradeOptions{
		Namespace:              upgrade.GetNamespace(),
		ReleaseName:            upgrade.GetReleaseName(),
		ChartPath:              upgrade.GetChart().GetResolvedUri(),
		ChartVersion:           upgrade.GetChart().GetVersion(),
		Values:                 valuesMap,
		ExpectedRevision:       int(upgrade.GetExpectedRevision()), //nolint:gosec // validated positive SDK revision.
		Atomic:                 true,
		MaxHistory:             int(upgrade.GetMaxHistory()),
		Timeout:                timeout,
		OperationID:            upgrade.GetOperationId(),
		CommandID:              upgrade.GetCommandId(),
		BundleDigest:           upgrade.GetBundle().GetBundleDigest(),
		ChartDigest:            upgrade.GetChart().GetDigest(),
		EffectiveValuesDigest:  upgrade.GetEffectiveValuesDigest(),
		SecretSnapshotDigest:   secretDigest,
		ExpectedManifestDigest: upgrade.GetExpectedManifestDigest(),
		ResetValues:            true,
		ReuseValues:            false,
		CleanupOnFail:          false,
		WaitForJobs:            true,
		TakeOwnership:          false,
	})
	if err != nil {
		result.Code = upgradeErrorCode(err)
		result.Message = err.Error()
		result.Release = release
		result.Upgrade = &operatorv1.UpgradeResult{
			From:              releaseSnapshot(fromRelease),
			Attempted:         releaseSnapshot(release),
			Active:            releaseSnapshot(release),
			RollbackSucceeded: release != nil && fromRelease != nil && release.Revision == fromRelease.Revision,
			ResourceSummary: &operatorv1.ResourceSummary{
				ManifestDigest: manifestDigest(release),
			},
		}
		result.ResourceSummary.ManifestDigest = manifestDigest(release)
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
	return result
}

func manifestDigest(release *helmengine.Release) string {
	if release == nil {
		return ""
	}
	return release.ManifestDigest
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func (a *Agent) executeRollback(ctx context.Context, command *operatorv1.Command) Result {
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

	timeout := a.installFlags.Timeout
	if command.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(command.GetTimeoutSeconds()) * time.Second
	}

	release, err := a.engine.Rollback(ctx, helmengine.RollbackOptions{
		Namespace:      command.GetNamespace(),
		ReleaseName:    command.GetReleaseName(),
		TargetRevision: int(command.GetTargetRevision()),
		Timeout:        timeout,
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
	case errors.Is(err, helmengine.ErrConflict):
		return "revision_conflict"
	case errors.Is(err, helmengine.ErrDigestMismatch):
		return "digest_mismatch"
	case errors.Is(err, helmengine.ErrSecretRefChanged), strings.Contains(err.Error(), "secret_ref_changed"):
		return "secret_ref_changed"
	case errors.Is(err, helmengine.ErrRenderDrift):
		return "render_drift"
	case errors.Is(err, helmengine.ErrRenderFailed), strings.Contains(err.Error(), "render_failed"):
		return "render_failed"
	case errors.Is(err, helmengine.ErrSchemaFailed), strings.Contains(err.Error(), "values don't meet"):
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

func rollbackErrorCode(err error) string {
	switch {
	case errors.Is(err, helmengine.ErrNotFound):
		return "release_not_found"
	case errors.Is(err, helmengine.ErrRevisionNotFound):
		return "target_revision_not_found"
	case errors.Is(err, helmengine.ErrArtifactUnavailable):
		return "historical_artifact_unavailable"
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
