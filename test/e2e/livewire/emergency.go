package livewire

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// workloadResourceByKind maps a workload kind onto the GVR resource spelling the
// formal emergency API accepts. The server's accepted set is closed and lives in
// internal/orchestrator (workloadGVRResources): deployments, statefulsets,
// daemonsets. Anything else fails closed here rather than being sent as an
// unsupported reference the server would reject with invalid_workload_ref.
var workloadResourceByKind = map[string]string{
	"deployment":  "deployments",
	"statefulset": "statefulsets",
	"daemonset":   "daemonsets",
}

// workloadReference renders the canonical "<gvr.resource>/<namespace>/<name>"
// reference. It returns an error for a kind the API does not accept so the
// failure names the offending workload instead of surfacing as an opaque
// invalid_workload_ref.
func workloadReference(kind, namespace, name string) (string, error) {
	resource, ok := workloadResourceByKind[strings.ToLower(strings.TrimSpace(kind))]
	if !ok {
		return "", fmt.Errorf("livewire: workload kind %q has no emergency GVR resource", kind)
	}
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return "", errors.New("livewire: workload reference requires a namespace and a name")
	}
	return resource + "/" + namespace + "/" + name, nil
}

// Targets implements stages.EmergencyWriter from ListEmergencyTargets. Each
// target carries its adapter-resolved Reference, so the stage never has to guess
// the GVR spelling.
func (c *Connector) Targets(ctx context.Context, definitionID string) ([]stages.EmergencyTarget, error) {
	clients, err := c.clientsOrFail()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(definitionID) == "" {
		return nil, errors.New("livewire: empty release definition id")
	}
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	response, err := clients.Orchestrator().ListEmergencyTargets(ctx,
		authorizedRequest(c.session.Token(), &orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: definitionID,
		}))
	if err != nil {
		return nil, fmt.Errorf("livewire: list emergency targets: %w", err)
	}
	if response == nil || response.Msg == nil {
		return nil, errors.New("livewire: empty emergency target response")
	}
	targets := make([]stages.EmergencyTarget, 0, len(response.Msg.GetTargets()))
	for _, target := range response.Msg.GetTargets() {
		if target == nil || target.GetWorkloadRef() == nil {
			continue
		}
		reference := target.GetWorkloadRef()
		rendered, err := workloadReference(reference.GetKind(), reference.GetNamespace(), reference.GetName())
		if err != nil {
			return nil, err
		}
		targets = append(targets, stages.EmergencyTarget{
			WorkloadKind:    reference.GetKind(),
			WorkloadName:    reference.GetName(),
			Namespace:       reference.GetNamespace(),
			CurrentReplicas: target.GetCurrentReplicas(),
			Cluster:         target.GetCluster(),
			Reference:       rendered,
		})
	}
	return targets, nil
}

// SetReplicas implements stages.EmergencyWriter via ExecuteEmergencyChange.
//
// Two contract details are deliberate and easy to get wrong:
//
//   - request_hash is left unset. The server derives the replay fingerprint by
//     hashing the whole message itself (hashExecuteEmergencyRequest) and never
//     reads the client field, so populating it here would only make the
//     fingerprint depend on a value the server ignores.
//   - set_replicas is mutually exclusive with container/artifact_ref (the image
//     branch), so neither is sent.
func (c *Connector) SetReplicas(ctx context.Context, request stages.EmergencySetReplicasRequest) (stages.OperationRef, error) {
	clients, err := c.clientsOrFail()
	if err != nil {
		return stages.OperationRef{}, err
	}
	if strings.TrimSpace(request.WorkloadRef) == "" {
		return stages.OperationRef{}, errors.New("livewire: set replicas requires a workload reference")
	}
	if request.Replicas < 0 {
		return stages.OperationRef{}, errors.New("livewire: set replicas requires a non-negative replica count")
	}
	if err := c.Login(ctx); err != nil {
		return stages.OperationRef{}, err
	}
	response, err := clients.Orchestrator().ExecuteEmergencyChange(ctx,
		authorizedRequest(c.session.Token(), &orchestratorv1.ExecuteEmergencyChangeRequest{
			ReleaseDefinitionId: request.DefinitionID,
			WorkloadRef:         request.WorkloadRef,
			OperationVersion:    strings.TrimSpace(request.OperationVersion),
			ConvergenceStrategy: convergenceStrategyFor(request.Convergence),
			SetReplicas:         request.Replicas,
			IdempotencyKey:      e2e.WriteIdempotencyKey("emergency-set-replicas", request.DefinitionID, strconv.FormatInt(int64(request.Replicas), 10)),
		}))
	if err != nil {
		return stages.OperationRef{}, err
	}
	if response == nil || response.Msg == nil {
		return stages.OperationRef{}, errors.New("livewire: empty emergency change response")
	}
	result := response.Msg.GetResult()
	if result != nil && !result.GetRequested() {
		return stages.OperationRef{}, errors.New("livewire: emergency change was not accepted")
	}
	operationID := response.Msg.GetOperationId()
	if strings.TrimSpace(operationID) == "" {
		return stages.OperationRef{}, errors.New("livewire: emergency change returned no operation id")
	}
	return stages.OperationRef{
		ID:           operationID,
		DefinitionID: request.DefinitionID,
		Type:         "EMERGENCY",
		Status:       resultStatus(result),
		Convergence:  convergencePolicyName(result.GetConvergencePolicy()),
	}, nil
}

// convergenceStrategyFor maps the stage's canonical policy name onto the proto
// enum. An unknown policy fails closed as UNSPECIFIED, which the server rejects
// (D13) rather than silently defaulting to a persistent change.
func convergenceStrategyFor(policy string) orchestratorv1.ConvergenceStrategy {
	switch strings.ToUpper(strings.TrimSpace(policy)) {
	case stages.EmergencyConvergenceRevertOnNextReconcile:
		return orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE
	case stages.EmergencyConvergenceRequirePromotion:
		return orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
	default:
		return orchestratorv1.ConvergenceStrategy_CONVERGENCE_STRATEGY_UNSPECIFIED
	}
}

// resultStatus renders the acceptance state of an emergency change. The change
// is queued asynchronously (REQ-079 D1), so an accepted request is reported as
// the pending lifecycle state the stage's AwaitOperation then resolves.
func resultStatus(result *orchestratorv1.EmergencyResult) string {
	if result == nil || !result.GetRequested() {
		return orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED.String()
	}
	return orchestratorv1.OperationStatus_OPERATION_STATUS_PENDING.String()
}
