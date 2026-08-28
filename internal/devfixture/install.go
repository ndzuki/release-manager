package devfixture

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
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

	// CreateOperation is denied to the deployer role (REQ-027 matrix /
	// D-101); the bootstrap INSTALLs act as e2e-runner (release_admin) —
	// the account the accounts phase provisioned exactly for
	// CreateOperation/RollbackRelease/emergency changes. Real smoke
	// 2026-08-27: install phase failed `permission_denied` on the deployer
	// token, which the fakes (no role enforcement) had masked.
	if err := r.ensureRunnerToken(ctx); err != nil {
		return err
	}
	// The install phase (per-definition waitBundleValidated + CreateOperation
	// + preflight + operator execution) can exceed the 15-minute JWT TTL;
	// refresh the admin token so waitBundleValidated's GetBundle does not fall
	// through to the service-token leg (`invalid service token`, real smoke
	// 2026-08-27). Refreshed again per definition below.
	if err := r.refreshTokens(ctx); err != nil {
		return err
	}

	// CreateOperation rejects a bundle in received state
	// (failed_precondition: bundle_not_ready; REQ-067 rule 9). The
	// orchestrator's validation worker transitions received→validated
	// asynchronously (30s poll) — wait for the transition instead of
	// burning the phase retry budget against the not-yet-run worker
	// (real smoke 2026-08-27: all 3 install retries failed while the
	// worker's first poll was still 20s away).
	if err := r.waitBundleValidated(ctx); err != nil {
		return err
	}

	for _, seed := range definitionSeeds {
		record := r.state.definitions[seed.logicalKey]
		if record.id == "" || record.valuesRevisionID == "" {
			return fmt.Errorf("definition %s not ready for install", seed.logicalKey)
		}
		// Each definition's CreateOperation + preflight + operator execution
		// can exceed the 15-minute JWT TTL; refresh the runner token before
		// every operation (real smoke 2026-08-28: CreateOperation `token is
		// expired` on the second definition).
		if err := r.refreshTokens(ctx); err != nil {
			return err
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

// ensureRunnerToken logs the e2e-runner (release_admin) in once per run and
// caches the token; the login is lazy because the account itself is only
// provisioned in the accounts phase (a resume into install already has it).
func (r *runner) ensureRunnerToken(ctx context.Context) error {
	if r.state.runnerToken != "" {
		return nil
	}
	login, err := r.clients.auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: e2eRunnerUser,
		Password: r.state.credentials.runner,
	}))
	if err != nil {
		return fmt.Errorf("login %s for bootstrap installs: %w", e2eRunnerUser, err)
	}
	r.state.runnerToken = login.Msg.GetAccessToken()
	return nil
}

// bundleValidationPoll / bundleValidationTimeout bound the
// waitBundleValidated loop; package vars so tests can shorten them.
var (
	bundleValidationPoll    = 5 * time.Second
	bundleValidationTimeout = 5 * time.Minute
)

// waitBundleValidated polls GetBundle until the submitted bundle reaches
// VALIDATED. The orchestrator validates asynchronously (validation worker
// poll), so an immediate CreateOperation would fail bundle_not_ready; the
// wait bounds that convergence window and fails closed with the observed
// status. GetBundle is definition-scoped for external actors (real smoke
// 2026-08-27: not_authorized: release_definition_id is required), so the
// request carries the first E2E definition.
func (r *runner) waitBundleValidated(ctx context.Context) error {
	if r.state.bundle.id == "" {
		return fmt.Errorf("bundle id not recorded; run the bundle phase first")
	}
	definitionID, err := r.resolveBundleDefinitionID(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(bundleValidationTimeout)
	for {
		req := connect.NewRequest(&orchestratorv1.GetBundleRequest{
			BundleId:            r.state.bundle.id,
			ReleaseDefinitionId: definitionID,
		})
		withAuth(req, r.state.adminToken)
		response, err := r.clients.bundle.GetBundle(ctx, req)
		if err != nil {
			return fmt.Errorf("get bundle status: %w", err)
		}
		summary := response.Msg.GetBundle().GetSummary()
		if summary != nil && summary.GetStatus() == commonv1.BundleStatus_BUNDLE_STATUS_VALIDATED {
			return nil
		}
		if time.Now().After(deadline) {
			status := commonv1.BundleStatus_BUNDLE_STATUS_UNSPECIFIED
			if summary != nil {
				status = summary.GetStatus()
			}
			return fmt.Errorf("bundle %s not validated within %s (status %s)", r.state.bundle.id, bundleValidationTimeout, status)
		}
		select {
		case <-time.After(bundleValidationPoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// resolveBundleDefinitionID returns the first E2E definition id, hydrating
// the definition ids from the live readback when the current run resumed
// without populating them in memory. It uses the ids-only readback so the
// bundle committed-check keeps working before the values phase commits.
func (r *runner) resolveBundleDefinitionID(ctx context.Context) (string, error) {
	if len(r.state.definitions) == 0 {
		r.state.definitions = map[string]definitionRecord{}
		if err := r.readbackDefinitionIDs(ctx); err != nil {
			return "", fmt.Errorf("resolve definition for bundle readback: %w", err)
		}
	}
	record := r.state.definitions[definitionSeeds[0].logicalKey]
	if record.id == "" {
		return "", fmt.Errorf("definition %s id not resolved for bundle readback", definitionSeeds[0].logicalKey)
	}
	return record.id, nil
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
			SignatureRef: &commonv1.SignatureRef{
				Digest:    r.state.bundle.digest,
				Signature: signBundleDigest(r.state.trustRootKey, r.state.bundle.digest),
				Issuer:    devTrustRootIssuer,
				Subject:   devTrustRootSubject,
			},
		})
		// TASK-067 contract: idempotency travels via the Idempotency-Key
		// header (AC-067-06/07) and the actor is derived from the bearer
		// token by the auth interceptor, not from request fields.
		// The creating actor is e2e-runner (release_admin): the deployer
		// role cannot create Operations (REQ-027 matrix / D-101; real smoke
		// 2026-08-27 permission_denied).
		req.Header().Set("Idempotency-Key", key)
		withAuth(req, r.state.runnerToken)
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
