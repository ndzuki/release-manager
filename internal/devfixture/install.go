package devfixture

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// phaseInstall creates one bootstrap INSTALL operation per E2E target
// through the formal CreateOperation + preflight + outbox pipeline and waits
// for each to reach the succeeded terminal state (REQ-065 output contract).
// Each operation carries an Idempotency-Key and a SignatureRef signed by the
// Dev Trust Root, so re-runs replay the same operation instead of creating
// duplicates; a previously failed operation is retried under a fresh key.
func (r *runner) phaseInstall(ctx context.Context) error {
	if r.state.trustRootKey == nil {
		key, err := r.loadOrGenerateTrustRootKey(r.cfg)
		if err != nil {
			return err
		}
		r.state.trustRootKey = key
	}
	r.state.operations = nil

	for _, seed := range definitionSeeds {
		record := r.state.definitions[seed.logicalKey]
		if record.id == "" || record.valuesRevisionID == "" {
			return fmt.Errorf("definition %s not ready for install", seed.logicalKey)
		}
		operationID, err := r.ensureInstallOperation(ctx, seed, record)
		if err != nil {
			return err
		}
		r.cfg.log().Info("bootstrap install succeeded", "definition", seed.logicalKey, "operation", operationID)
		r.state.operations = append(r.state.operations, operationID)
	}
	return nil
}

// ensureInstallOperation creates the INSTALL operation and waits for its
// terminal state. The deterministic idempotency key replays an existing
// operation (resume after interruption); a terminal replay that is not
// succeeded is retried once under a fresh key so a failed bootstrap does
// not brick the fixture.
func (r *runner) ensureInstallOperation(ctx context.Context, seed definitionSeed, record definitionRecord) (string, error) {
	baseKey := "devseed-install-" + seed.releaseName
	previousTerminal := orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED
	for attempt := 0; attempt < 2; attempt++ {
		key := baseKey
		if attempt > 0 {
			key = fmt.Sprintf("%s-retry-%d", baseKey, attempt)
		}
		req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
			OperationType:       "INSTALL",
			BundleId:            r.state.bundle.id,
			ReleaseDefinitionId: record.id,
			ValuesRevisionId:    record.valuesRevisionID,
			IdempotencyKey:      key,
			Actor:               &commonv1.ActorContext{UserId: r.state.deployerUserID, Organization: r.state.adminOrgID},
			SignatureRef: &commonv1.SignatureRef{
				Digest:    r.state.bundle.digest,
				Signature: signBundleDigest(r.state.trustRootKey, r.state.bundle.digest),
				Issuer:    devTrustRootIssuer,
				Subject:   devTrustRootSubject,
			},
		})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.CreateOperation(ctx, req)
		if err != nil {
			return "", fmt.Errorf("create INSTALL operation for %s: %w", seed.logicalKey, err)
		}
		terminal, err := r.waitForTerminal(ctx, response.Msg.GetOperationId())
		if err != nil {
			return "", err
		}
		if terminal == orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED {
			return response.Msg.GetOperationId(), nil
		}
		previousTerminal = terminal
	}
	if previousTerminal == orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED {
		return "", fmt.Errorf("bootstrap install %s did not reach a terminal state", seed.logicalKey)
	}
	return "", fmt.Errorf("bootstrap install %s terminated as %s after retry", seed.logicalKey, previousTerminal)
}

// checkCommittedInstall verifies every recorded operation reached the
// succeeded terminal state.
func (r *runner) checkCommittedInstall(ctx context.Context, state phaseState) error {
	if len(state.Operations) == 0 {
		return fmt.Errorf("no install operations recorded")
	}
	for _, operationID := range state.Operations {
		req := connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.GetOperation(ctx, req)
		if err != nil {
			return fmt.Errorf("get operation %s: %w", operationID, err)
		}
		if response.Msg.GetOperation().GetState() != orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED {
			return fmt.Errorf("operation %s is not succeeded", operationID)
		}
	}
	return nil
}
