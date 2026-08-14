package devfixture

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
)

// phaseAccounts ensures the viewer account exists and the credentials file
// is valid. Admin/deployer provisioning happens in the login preamble; this
// phase commits the reader account (REQ-065 development accounts).
func (r *runner) phaseAccounts(ctx context.Context) error {
	return r.ensureReaderUser(ctx)
}

// phaseTrust establishes the Dev Trust Root: an Ed25519 key owned by the
// seed (0600 on disk in local mode, env-injected in CI mode) whose public
// key is activated through TrustService CreateTrustRoot. The root is
// created exactly once per environment — GetTrustPolicy is probed first and
// the server rejects duplicate key_ids (overlap_conflict).
func (r *runner) phaseTrust(ctx context.Context) error {
	key, err := r.loadOrGenerateTrustRootKey(r.cfg)
	if err != nil {
		return err
	}
	r.state.trustRootKey = key

	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("dev trust root key is not an ed25519 public key")
	}
	pubPEM, err := encodePublicKey(pub)
	if err != nil {
		return err
	}

	policyReq := connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: r.cfg.TrustEnvironment})
	policyResp, err := r.clients.trust.GetTrustPolicy(ctx, policyReq)
	if err != nil {
		return fmt.Errorf("get trust policy for %s: %w", r.cfg.TrustEnvironment, err)
	}
	for _, root := range policyResp.Msg.GetPolicy().GetRoots() {
		if root.GetKeyId() == devTrustRootKeyID {
			r.cfg.log().Info("dev trust root already active", "environment", r.cfg.TrustEnvironment, "root_id", root.GetId())
			return nil
		}
	}

	createReq := connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment:    r.cfg.TrustEnvironment,
		KeyId:          devTrustRootKeyID,
		PublicKeyPem:   pubPEM,
		Issuer:         devTrustRootIssuer,
		SubjectPattern: "release-manager",
		ValidFrom:      timestamppb.New(r.cfg.now()),
		Operator:       "devseed",
	})
	response, err := r.clients.trust.CreateTrustRoot(ctx, createReq)
	if err != nil {
		return fmt.Errorf("create trust root: %w", err)
	}
	r.cfg.log().Info("dev trust root activated",
		"environment", r.cfg.TrustEnvironment,
		"root_id", response.Msg.GetRoot().GetId(),
		"key_id", devTrustRootKeyID,
	)
	return nil
}

// checkCommittedTrust verifies the dev trust root is still active for the
// environment and (local mode) the private key file still exists.
func (r *runner) checkCommittedTrust(ctx context.Context) error {
	policyReq := connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: r.cfg.TrustEnvironment})
	policyResp, err := r.clients.trust.GetTrustPolicy(ctx, policyReq)
	if err != nil {
		return fmt.Errorf("get trust policy for %s: %w", r.cfg.TrustEnvironment, err)
	}
	found := false
	for _, root := range policyResp.Msg.GetPolicy().GetRoots() {
		if root.GetKeyId() == devTrustRootKeyID && root.GetState() == trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("dev trust root %q not active in environment %s", devTrustRootKeyID, r.cfg.TrustEnvironment)
	}
	if r.cfg.Mode == ModeLocal {
		if _, err := r.loadOrGenerateTrustRootKey(r.cfg); err != nil {
			return fmt.Errorf("dev trust root key file: %w", err)
		}
	}
	return nil
}

// signBundleDigest signs the canonical bundle digest with the Dev Trust Root
// private key. The signature is attached to CreateOperation (SignatureRef)
// and verified by the orchestrator's Ed25519 verifier against the live root.
func signBundleDigest(key ed25519.PrivateKey, digest string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(digest)))
}

func encodePublicKey(key ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// waitForTerminal polls GetOperation until the operation reaches a terminal
// state and returns it.
func (r *runner) waitForTerminal(ctx context.Context, operationID string) (orchestratorv1.OperationStatus, error) {
	deadline := time.NewTimer(r.cfg.InstallTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(r.cfg.InstallPollPeriod)
	defer ticker.Stop()

	for {
		req := connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.GetOperation(ctx, req)
		if err != nil {
			return orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED, fmt.Errorf("get operation %s: %w", operationID, err)
		}
		state := response.Msg.GetOperation().GetState()
		if terminalOperationState(state) {
			return state, nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED, fmt.Errorf("operation %s did not reach a terminal state within %s (last state %s)", operationID, r.cfg.InstallTimeout, state)
		case <-ctx.Done():
			return orchestratorv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED, ctx.Err()
		}
	}
}

func terminalOperationState(state orchestratorv1.OperationStatus) bool {
	switch state {
	case orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED,
		orchestratorv1.OperationStatus_OPERATION_STATUS_FAILED,
		orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED,
		orchestratorv1.OperationStatus_OPERATION_STATUS_TIMEOUT:
		return true
	default:
		return false
	}
}
