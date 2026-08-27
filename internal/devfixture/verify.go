package devfixture

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// phaseVerify performs the full readback (AC-065-24): every entity is
// re-read and its count/digest compared against the canonical fixture, then
// the runtime manifest (data/dev-fixture.json) and the all-committed
// progress are written.
func (r *runner) phaseVerify(ctx context.Context) error {
	if err := r.verifyAccounts(ctx); err != nil {
		return err
	}
	if err := r.verifyCustomers(ctx); err != nil {
		return err
	}
	if err := r.verifyClusters(ctx); err != nil {
		return err
	}
	if err := r.verifyRoutes(ctx); err != nil {
		return err
	}
	if err := r.verifyDefinitionsAndValues(ctx); err != nil {
		return err
	}
	if err := r.verifyBundle(ctx); err != nil {
		return err
	}
	if err := r.verifyEnrollment(ctx); err != nil {
		return err
	}
	if err := r.verifyOperations(ctx); err != nil {
		return err
	}
	r.manifest = r.buildManifest()
	if err := r.saveManifest(r.manifest); err != nil {
		return err
	}
	r.cfg.log().Info("fixture verified",
		"customers", len(customerSeeds),
		"clusters", len(clusterSeeds),
		"routes", len(routeSeeds),
		"definitions", len(definitionSeeds),
		"bundle", r.state.bundle.digest,
	)
	return nil
}

// verifyAccounts verifies the four development accounts exist with their
// roles (AC-065-34): dev-admin (platform_admin), dev-deployer (deployer),
// dev-reader (viewer) and e2e-runner (release_admin — the REQ-066 E2E write
// identity). The roles are read back through GetLocalUser so the assertion
// covers the server-authoritative role binding.
func (r *runner) verifyAccounts(ctx context.Context) error {
	accounts := []struct {
		name     string
		expected string
	}{
		{r.cfg.AdminUser, "platform_admin"},
		{r.cfg.DeployerUser, "deployer"},
		{readerUser, "viewer"},
		{e2eRunnerUser, "release_admin"},
	}
	for _, account := range accounts {
		req := connect.NewRequest(&authv1.GetLocalUserRequest{Username: account.name})
		withAuth(req, r.state.adminToken)
		response, err := r.clients.auth.GetLocalUser(ctx, req)
		if err != nil {
			return fmt.Errorf("verify account %s: %w", account.name, err)
		}
		user := response.Msg.GetUser()
		if user == nil || user.GetUsername() != account.name || user.GetStatus() != "active" {
			return fmt.Errorf("verify: account %s missing or not active", account.name)
		}
		if !slices.Contains(user.GetRoles(), account.expected) {
			return fmt.Errorf("verify: account %s roles %v do not contain %q", account.name, user.GetRoles(), account.expected)
		}
	}
	return nil
}

func (r *runner) verifyCustomers(ctx context.Context) error {
	listReq := connect.NewRequest(&orchestratorv1.ListCustomersRequest{IncludeDisabled: true})
	withAuth(listReq, r.state.adminToken)
	list, err := r.clients.orch.ListCustomers(ctx, listReq)
	if err != nil {
		return fmt.Errorf("verify customers: %w", err)
	}
	byID := map[string]string{}
	for _, customer := range list.Msg.GetCustomers() {
		byID[customer.GetId()] = customer.GetName()
	}
	if r.state.customers == nil {
		r.state.customers = map[string]string{}
	}
	for _, seed := range customerSeeds {
		name, ok := byID[seed.id]
		if !ok {
			return fmt.Errorf("verify: customer %s missing", seed.logicalKey)
		}
		if name != seed.name {
			return fmt.Errorf("verify: customer %s name mismatch", seed.logicalKey)
		}
		r.state.customers[seed.logicalKey] = seed.id
	}
	return nil
}

func (r *runner) verifyClusters(ctx context.Context) error {
	if r.state.clusters == nil {
		r.state.clusters = map[string]string{}
	}
	for _, seed := range clusterSeeds {
		req := connect.NewRequest(&orchestratorv1.GetClusterRequest{ClusterId: seed.id})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.GetCluster(ctx, req)
		if err != nil {
			return fmt.Errorf("verify cluster %s: %w", seed.id, err)
		}
		if response.Msg.GetCluster().GetName() != seed.name {
			return fmt.Errorf("verify: cluster %s name mismatch", seed.id)
		}
		r.state.clusters[seed.id] = seed.id
	}
	return nil
}

func (r *runner) verifyRoutes(ctx context.Context) error {
	for _, seed := range routeSeeds {
		req := connect.NewRequest(&orchestratorv1.GetClusterRoutesRequest{ClusterId: seed.clusterKey})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.GetClusterRoutes(ctx, req)
		if err != nil {
			return fmt.Errorf("verify routes for %s: %w", seed.clusterKey, err)
		}
		found := false
		for _, route := range response.Msg.GetRoutes() {
			if route.GetId() == seed.id &&
				route.GetMode() == seed.mode &&
				route.GetSourcePrefix() == seed.sourcePrefix &&
				route.GetTargetPrefix() == seed.targetPrefix {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("verify: route %s missing or mismatched on %s", seed.id, seed.clusterKey)
		}
	}
	return nil
}

func (r *runner) verifyDefinitionsAndValues(ctx context.Context) error {
	if err := r.readbackDefinitions(ctx); err != nil {
		return err
	}
	for _, seed := range definitionSeeds {
		record := r.state.definitions[seed.logicalKey]
		if record.id == "" || record.valuesRevisionID == "" {
			return fmt.Errorf("verify: definition %s incomplete", seed.logicalKey)
		}
	}
	return nil
}

func (r *runner) verifyBundle(ctx context.Context) error {
	if r.state.bundle.id == "" {
		return fmt.Errorf("verify: bundle id not recorded")
	}
	// D-016 残余 (AC-065-33 后半链): the verify readback hits the real
	// orchestrator auth interceptor — attach the admin bearer (real smoke
	// 2026-08-25: verify bundle ... unauthenticated) and the definition
	// scope (real smoke 2026-08-27: release_definition_id is required).
	definitionID, err := r.resolveBundleDefinitionID(ctx)
	if err != nil {
		return fmt.Errorf("verify bundle %s: %w", r.state.bundle.id, err)
	}
	req := connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId:            r.state.bundle.id,
		ReleaseDefinitionId: definitionID,
	})
	withAuth(req, r.state.adminToken)
	response, err := r.clients.bundle.GetBundle(ctx, req)
	if err != nil {
		return fmt.Errorf("verify bundle %s: %w", r.state.bundle.id, err)
	}
	summary := response.Msg.GetBundle().GetSummary()
	if summary == nil || summary.GetName() != bundleName {
		return fmt.Errorf("verify: bundle %s identity mismatch", r.state.bundle.id)
	}
	if digest := bundleDigestString(summary.GetDigest()); digest != r.state.bundle.digest {
		return fmt.Errorf("verify: bundle digest mismatch: got %q want %q", digest, r.state.bundle.digest)
	}
	return nil
}

// verifyEnrollment verifies the enrollment side of the fixture: the token
// file exists (canonical local artifact) and every seed cluster has an
// operator agent whose session reached ONLINE (AC-065-01/18). The online
// wait is bounded by OperatorOnlineTimeout (AC-065-28); a missing or
// non-online session reports the failing cluster name so dev-up surfaces
// `operator_not_online` with the faulting cluster.
func (r *runner) verifyEnrollment(ctx context.Context) error {
	for _, seed := range clusterSeeds {
		info, err := os.Stat(r.enrollmentTokenPath(seed.id))
		if err != nil {
			return fmt.Errorf("verify enrollment token for %s: %w", seed.id, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("verify: enrollment token for %s is empty", seed.id)
		}
		if err := r.waitClusterOperatorOnline(ctx, seed); err != nil {
			return err
		}
	}
	return nil
}

// operatorOnlinePollPeriod is the interval between ListOperators probes
// during the online wait; a package var so tests can shorten it.
var operatorOnlinePollPeriod = 3 * time.Second

// waitClusterOperatorOnline polls ListOperators until an ONLINE session
// appears or the OperatorOnlineTimeout deadline expires. The operator agent
// bootstraps asynchronously after its cluster is applied, so the first
// probes are expected to report offline/suspect; the timeout is the
// deterministic outer bound (AC-065-18/28).
func (r *runner) waitClusterOperatorOnline(ctx context.Context, seed clusterSeed) error {
	deadline := time.Now().Add(r.cfg.OperatorOnlineTimeout)
	for {
		err := r.requireClusterOperatorOnline(ctx, seed)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-time.After(operatorOnlinePollPeriod):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// requireClusterOperatorOnline lists operators for the cluster and fails
// unless at least one has an ONLINE session (AC-065-18).
func (r *runner) requireClusterOperatorOnline(ctx context.Context, seed clusterSeed) error {
	listReq := connect.NewRequest(&orchestratorv1.ListOperatorsRequest{
		ClusterId: seed.id,
		PageSize:  100,
	})
	withAuth(listReq, r.state.deployerToken)
	response, err := r.clients.orch.ListOperators(ctx, listReq)
	if err != nil {
		return fmt.Errorf("operator_not_online: list operators for cluster %s: %w", seed.id, err)
	}
	for _, operator := range response.Msg.GetOperators() {
		if operator.GetSessionStatus() == orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_ONLINE {
			return nil
		}
	}
	statuses := make([]string, 0, len(response.Msg.GetOperators()))
	for _, operator := range response.Msg.GetOperators() {
		statuses = append(statuses, operator.GetSessionStatus().String())
	}
	return fmt.Errorf("operator_not_online: no online operator session for cluster %s (sessions: %v)",
		seed.id, statuses)
}

func (r *runner) verifyOperations(ctx context.Context) error {
	for _, operationID := range r.state.operations {
		req := connect.NewRequest(&orchestratorv1.GetOperationRequest{OperationId: operationID})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.GetOperation(ctx, req)
		if err != nil {
			return fmt.Errorf("verify operation %s: %w", operationID, err)
		}
		if response.Msg.GetOperation().GetState() != orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED {
			return fmt.Errorf("verify: operation %s is not succeeded", operationID)
		}
	}
	return nil
}

// buildManifest assembles the runtime identity manifest from the verified
// readback (stable logical keys → server-generated ids).
func (r *runner) buildManifest() *Manifest {
	customers := make(map[string]CustomerRef, len(customerSeeds))
	for _, seed := range customerSeeds {
		customers[seed.logicalKey] = CustomerRef{ID: seed.id, Name: seed.name}
	}
	clusters := make(map[string]ClusterRef, len(clusterSeeds))
	for _, seed := range clusterSeeds {
		clusters[seed.id] = ClusterRef{ID: seed.id, Name: seed.name}
	}
	definitions := make(map[string]DefinitionRef, len(definitionSeeds))
	for _, seed := range definitionSeeds {
		record := r.state.definitions[seed.logicalKey]
		definitions[seed.logicalKey] = DefinitionRef{
			ID:               record.id,
			ValuesRevisionID: record.valuesRevisionID,
			BundleID:         r.state.bundle.id,
		}
	}
	return &Manifest{
		FixtureVersion: r.cfg.FixtureVersion,
		GeneratedAt:    r.cfg.nowRFC3339(),
		Customers:      customers,
		Clusters:       clusters,
		Definitions:    definitions,
		Bundle:         BundleRef{ID: r.state.bundle.id, Digest: r.state.bundle.digest},
	}
}
