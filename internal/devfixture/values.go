package devfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"connectrpc.com/connect"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// definitionCluster is the single dev cluster hosting the E2E targets
// (REQ-065: all four definitions live on dev-customer-a-direct).
const definitionCluster = "dev-customer-a-direct"

// definitionCustomerID is the deterministic customer id owning the E2E
// cluster (dev-customer-a = 11111111-1111-4111-8111-111111111111).
const definitionCustomerID = "11111111-1111-4111-8111-111111111111"

// valuesDigest returns the bare sha256 hex digest of a values document,
// matching the server's values.Digest semantics (no "sha256:" prefix).
func valuesDigest(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

// phaseValues creates the four E2E release definitions and drives one
// approved values revision per definition through the formal
// Submit + Approve workflow (REQ-065: no self-approval — the deployer
// creates/submits, the admin approves).
func (r *runner) phaseValues(ctx context.Context) error {
	r.state.definitions = map[string]definitionRecord{}

	for _, seed := range definitionSeeds {
		definitionID, err := r.ensureDefinition(ctx, seed)
		if err != nil {
			return err
		}
		revisionID, err := r.ensureValuesRevision(ctx, seed, definitionID)
		if err != nil {
			return err
		}
		r.state.definitions[seed.logicalKey] = definitionRecord{id: definitionID, valuesRevisionID: revisionID}
	}
	return nil
}

// ensureDefinition creates or reuses one release definition, keyed by its
// release name (ListReleaseDefinitions probe first).
func (r *runner) ensureDefinition(ctx context.Context, seed definitionSeed) (string, error) {
	listReq := connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{
		CustomerId: definitionCustomerID, ClusterId: definitionCluster, IncludeDisabled: true,
	})
	withAuth(listReq, r.state.deployerToken)
	list, err := r.clients.orch.ListReleaseDefinitions(ctx, listReq)
	if err != nil {
		return "", fmt.Errorf("list definitions: %w", err)
	}
	for _, definition := range list.Msg.GetDefinitions() {
		if definition.GetReleaseName() == seed.releaseName {
			r.cfg.log().Info("release definition exists", "definition", seed.logicalKey, "id", definition.GetId())
			return definition.GetId(), nil
		}
	}
	createReq := connect.NewRequest(&orchestratorv1.CreateReleaseDefinitionRequest{
		CustomerId:  definitionCustomerID,
		ClusterId:   definitionCluster,
		Namespace:   seed.namespace,
		ReleaseName: seed.releaseName,
		// ChartName must match the bundle chart_ref basename
		// (oci://localhost:5001/release-fixture → release-fixture) or the
		// orchestrator rejects INSTALL with chart_mismatch (REQ-067 rule 10).
		ChartName: "release-fixture",
		Enabled:   true,
		// The creating actor's organization becomes the definition owner
		// (REQ-040); values approval gates on it (REQ-068).
		Actor: &commonv1.ActorContext{UserId: r.state.deployerUserID, Organization: r.state.adminOrgID},
	})
	withAuth(createReq, r.state.deployerToken)
	createReq.Header().Set("Idempotency-Key", idempotencyKey("values", seed.logicalKey+"-definition"))
	response, err := r.clients.orch.CreateReleaseDefinition(ctx, createReq)
	if err != nil {
		return "", fmt.Errorf("create release definition %s: %w", seed.logicalKey, err)
	}
	r.cfg.log().Info("release definition created", "definition", seed.logicalKey, "id", response.Msg.GetDefinition().GetId())
	return response.Msg.GetDefinition().GetId(), nil
}

// ensureValuesRevision creates or reuses the minimal approved values
// revision ({"replicaCount":1}) for one definition, then drives the matching
// draft through Submit and Approve. The idempotency keys use the stable
// definition logical key — never the server definition id, which does not
// exist yet on a retried create (批次5 D9, AC-065-41).
func (r *runner) ensureValuesRevision(ctx context.Context, seed definitionSeed, definitionID string) (string, error) {
	valuesJSON := []byte(valuesDocument)
	expectedDigest := valuesDigest(valuesJSON)

	listReq := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{ReleaseDefinitionId: definitionID})
	withAuth(listReq, r.state.deployerToken)
	listResponse, err := r.clients.orch.ListValuesRevisions(ctx, listReq)
	if err != nil {
		return "", fmt.Errorf("list values revisions for %s: %w", definitionID, err)
	}
	var pending *commonv1.ValuesRevision
	for _, revision := range listResponse.Msg.GetItems() {
		if revision.GetDigest() != expectedDigest {
			continue
		}
		switch revision.GetStatus() {
		case commonv1.ValuesStatus_VALUES_STATUS_APPROVED:
			r.cfg.log().Info("values revision already approved", "revision_id", revision.GetId())
			return revision.GetId(), nil
		case commonv1.ValuesStatus_VALUES_STATUS_DRAFT, commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL:
			pending = revision
		}
		if pending != nil {
			break
		}
	}
	if pending == nil {
		createReq := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
			ReleaseDefinitionId: definitionID,
			Document:            string(valuesJSON),
		})
		createReq.Header().Set("Idempotency-Key", idempotencyKey("values", seed.logicalKey))
		withAuth(createReq, r.state.deployerToken)
		created, err := r.clients.orch.CreateValuesRevision(ctx, createReq)
		if err != nil {
			return "", fmt.Errorf("create values revision for %s: %w", definitionID, err)
		}
		pending = created.Msg.GetRevision()
		r.cfg.log().Info("values revision created", "revision_id", pending.GetId())
	}
	if pending.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_DRAFT {
		submitReq := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
			RevisionId: pending.GetId(), ExpectedStateVersion: pending.GetStateVersion(), Comment: "seeded by devseed",
		})
		withAuth(submitReq, r.state.deployerToken)
		// The request hash covers the state version, so the same key +
		// identical body replays idempotently across phase-internal retries.
		submitReq.Header().Set("Idempotency-Key", idempotencyKey("values", seed.logicalKey+"-submit"))
		submitted, err := retrySnapshotWarmup(ctx, func() (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
			return r.clients.orch.SubmitValuesRevision(ctx, submitReq)
		})
		if err != nil {
			return "", fmt.Errorf("submit values revision %s: %w", pending.GetId(), err)
		}
		pending = submitted.Msg.GetRevision()
		r.cfg.log().Info("values revision submitted", "revision_id", pending.GetId())
	}

	approveReq := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: pending.GetId(), ExpectedStateVersion: pending.GetStateVersion(), Comment: "seeded by devseed",
	})
	withAuth(approveReq, r.state.adminToken)
	approveReq.Header().Set("Idempotency-Key", idempotencyKey("values", seed.logicalKey+"-approve"))
	approved, err := retrySnapshotWarmup(ctx, func() (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
		return r.clients.orch.ApproveValuesRevision(ctx, approveReq)
	})
	if err != nil {
		return "", fmt.Errorf("approve values revision %s: %w", pending.GetId(), err)
	}
	r.cfg.log().Info("values revision approved", "revision_id", approved.Msg.GetRevision().GetId())
	return approved.Msg.GetRevision().GetId(), nil
}

// checkCommittedValues verifies every definition and its approved values
// revision still exist (logical-key checks: release name + values digest).
func (r *runner) checkCommittedValues(ctx context.Context) error {
	listReq := connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{
		CustomerId: definitionCustomerID, ClusterId: definitionCluster, IncludeDisabled: true,
	})
	withAuth(listReq, r.state.deployerToken)
	list, err := r.clients.orch.ListReleaseDefinitions(ctx, listReq)
	if err != nil {
		return fmt.Errorf("list definitions: %w", err)
	}
	byName := map[string]string{}
	for _, definition := range list.Msg.GetDefinitions() {
		byName[definition.GetReleaseName()] = definition.GetId()
	}
	expectedDigest := valuesDigest([]byte(valuesDocument))
	for _, seed := range definitionSeeds {
		definitionID, ok := byName[seed.releaseName]
		if !ok {
			return fmt.Errorf("definition %s (%s) missing", seed.logicalKey, seed.releaseName)
		}
		if err := r.checkApprovedValues(ctx, definitionID, expectedDigest); err != nil {
			return fmt.Errorf("definition %s: %w", seed.logicalKey, err)
		}
	}
	return nil
}

// checkApprovedValues finds an approved revision with the expected digest.
func (r *runner) checkApprovedValues(ctx context.Context, definitionID, expectedDigest string) error {
	listReq := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{ReleaseDefinitionId: definitionID})
	withAuth(listReq, r.state.deployerToken)
	listResponse, err := r.clients.orch.ListValuesRevisions(ctx, listReq)
	if err != nil {
		return fmt.Errorf("list values revisions for %s: %w", definitionID, err)
	}
	for _, revision := range listResponse.Msg.GetItems() {
		if revision.GetDigest() == expectedDigest && revision.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_APPROVED {
			return nil
		}
	}
	return fmt.Errorf("approved values revision with digest %s missing", expectedDigest)
}

// manifestDefinitionIDs reconstructs the definition records from the live
// readback (used by verify and the up-to-date rebuild path): definition ids
// plus their approved values revision ids.
func (r *runner) readbackDefinitions(ctx context.Context) error {
	if err := r.readbackDefinitionIDs(ctx); err != nil {
		return err
	}
	// Resolve approved values revision ids for the manifest.
	for _, seed := range definitionSeeds {
		record := r.state.definitions[seed.logicalKey]
		if record.id == "" {
			continue
		}
		revisionID, err := r.findApprovedRevisionID(ctx, record.id)
		if err != nil {
			return fmt.Errorf("definition %s: %w", seed.logicalKey, err)
		}
		record.valuesRevisionID = revisionID
		r.state.definitions[seed.logicalKey] = record
	}
	return nil
}

// readbackDefinitionIDs hydrates only the definition ids from the live
// readback. Unlike readbackDefinitions it does not resolve approved values
// revisions, so callers that need a definition scope but may run before the
// values phase commits (e.g. the bundle committed-check) do not depend on a
// values revision existing yet.
func (r *runner) readbackDefinitionIDs(ctx context.Context) error {
	listReq := connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{
		CustomerId: definitionCustomerID, ClusterId: definitionCluster, IncludeDisabled: true,
	})
	withAuth(listReq, r.state.deployerToken)
	list, err := r.clients.orch.ListReleaseDefinitions(ctx, listReq)
	if err != nil {
		return fmt.Errorf("list definitions: %w", err)
	}
	for _, definition := range list.Msg.GetDefinitions() {
		for _, seed := range definitionSeeds {
			if definition.GetReleaseName() == seed.releaseName {
				record := r.state.definitions[seed.logicalKey]
				record.id = definition.GetId()
				r.state.definitions[seed.logicalKey] = record
			}
		}
	}
	return nil
}

func (r *runner) findApprovedRevisionID(ctx context.Context, definitionID string) (string, error) {
	listReq := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{ReleaseDefinitionId: definitionID})
	withAuth(listReq, r.state.deployerToken)
	listResponse, err := r.clients.orch.ListValuesRevisions(ctx, listReq)
	if err != nil {
		return "", fmt.Errorf("list values revisions for %s: %w", definitionID, err)
	}
	expectedDigest := valuesDigest([]byte(valuesDocument))
	for _, revision := range listResponse.Msg.GetItems() {
		if revision.GetDigest() == expectedDigest && revision.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_APPROVED {
			return revision.GetId(), nil
		}
	}
	return "", fmt.Errorf("approved values revision missing for %s", definitionID)
}
