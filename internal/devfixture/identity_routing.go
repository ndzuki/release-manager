package devfixture

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// phaseIdentity seeds customers, their org-customer bindings, and clusters
// (REQ-065 entity identities). Customers are probed via ListCustomers (no
// binding exists before the customer itself); clusters via GetCluster.
func (r *runner) phaseIdentity(ctx context.Context) error {
	r.state.customers = map[string]string{}
	r.state.clusters = map[string]string{}
	if err := r.seedCustomers(ctx); err != nil {
		return err
	}
	if err := r.seedOrgBindings(ctx); err != nil {
		return err
	}
	return r.seedClusters(ctx)
}

// seedCustomers creates the two seed customers when absent, keyed by their
// stable logical keys.
func (r *runner) seedCustomers(ctx context.Context) error {
	listReq := connect.NewRequest(&orchestratorv1.ListCustomersRequest{IncludeDisabled: true})
	withAuth(listReq, r.state.adminToken)
	list, err := r.clients.orch.ListCustomers(ctx, listReq)
	if err != nil {
		return fmt.Errorf("list customers: %w", err)
	}
	existing := map[string]bool{}
	for _, customer := range list.Msg.GetCustomers() {
		existing[customer.GetId()] = true
	}

	for _, seed := range customerSeeds {
		if !existing[seed.id] {
			createReq := connect.NewRequest(&orchestratorv1.CreateCustomerRequest{Id: seed.id, Name: seed.name, Slug: seed.slug})
			withAuth(createReq, r.state.adminToken)
			if _, err := r.clients.orch.CreateCustomer(ctx, createReq); err != nil {
				return fmt.Errorf("create customer %s: %w", seed.id, err)
			}
			r.cfg.log().Info("customer created", "customer", seed.logicalKey, "id", seed.id)
		}
		r.state.customers[seed.logicalKey] = seed.id
	}
	return nil
}

// seedOrgBindings binds the admin org to every seed customer unless an
// active binding already exists (ADR-006: customer-scoped calls require one).
func (r *runner) seedOrgBindings(ctx context.Context) error {
	bindingReq := connect.NewRequest(&authv1.ListBindingsRequest{OrgId: r.state.adminOrgID})
	withAuth(bindingReq, r.state.adminToken)
	bindingList, err := r.clients.binding.ListBindings(ctx, bindingReq)
	if err != nil {
		return fmt.Errorf("list bindings org %s: %w", r.state.adminOrgID, err)
	}
	bound := map[string]bool{}
	for _, binding := range bindingList.Msg.GetBindings() {
		if binding.GetStatus() == "active" {
			bound[binding.GetCustomerId()] = true
		}
	}
	for _, seed := range customerSeeds {
		if bound[seed.id] {
			continue
		}
		req := connect.NewRequest(&authv1.CreateBindingRequest{OrgId: r.state.adminOrgID, CustomerId: seed.id})
		withAuth(req, r.state.adminToken)
		_, err := r.clients.binding.CreateBinding(ctx, req)
		if err != nil && connect.CodeOf(err) != connect.CodeAlreadyExists {
			return fmt.Errorf("create binding org %s customer %s: %w", r.state.adminOrgID, seed.id, err)
		}
		r.cfg.log().Info("org bound to customer", "customer", seed.logicalKey, "org", r.state.adminOrgID)
	}
	return nil
}

// seedClusters creates the four seed clusters (one per customer) when
// absent, tracking their server ids.
func (r *runner) seedClusters(ctx context.Context) error {
	for _, seed := range clusterSeeds {
		customerID := r.state.customers[seed.customerKey]
		getReq := connect.NewRequest(&orchestratorv1.GetClusterRequest{ClusterId: seed.id})
		withAuth(getReq, r.state.deployerToken)
		_, err := r.clients.orch.GetCluster(ctx, getReq)
		if err == nil {
			r.state.clusters[seed.id] = seed.id
			continue
		}
		if connect.CodeOf(err) != connect.CodeNotFound {
			return fmt.Errorf("get cluster %s: %w", seed.id, err)
		}
		createReq := connect.NewRequest(&orchestratorv1.CreateClusterRequest{
			Id: seed.id, Name: seed.name, CustomerId: customerID, KubeconfigRef: "kind://release-manager-dev",
		})
		withAuth(createReq, r.state.deployerToken)
		if _, err := r.clients.orch.CreateCluster(ctx, createReq); err != nil {
			return fmt.Errorf("create cluster %s: %w", seed.id, err)
		}
		r.cfg.log().Info("cluster created", "cluster", seed.id, "customer", seed.customerKey)
		r.state.clusters[seed.id] = seed.id
	}
	return nil
}

// checkCommittedIdentity verifies every customer and cluster still exists
// with a matching name (logical-key check only; server ids are not compared).
func (r *runner) checkCommittedIdentity(ctx context.Context) error {
	listReq := connect.NewRequest(&orchestratorv1.ListCustomersRequest{IncludeDisabled: true})
	withAuth(listReq, r.state.adminToken)
	list, err := r.clients.orch.ListCustomers(ctx, listReq)
	if err != nil {
		return fmt.Errorf("list customers: %w", err)
	}
	found := map[string]string{} // id → name
	for _, customer := range list.Msg.GetCustomers() {
		found[customer.GetId()] = customer.GetName()
	}
	for _, seed := range customerSeeds {
		name, ok := found[seed.id]
		if !ok {
			return fmt.Errorf("customer %s (%s) missing", seed.logicalKey, seed.id)
		}
		if name != seed.name {
			return fmt.Errorf("customer %s name mismatch: got %q want %q", seed.logicalKey, name, seed.name)
		}
	}
	for _, seed := range clusterSeeds {
		getReq := connect.NewRequest(&orchestratorv1.GetClusterRequest{ClusterId: seed.id})
		withAuth(getReq, r.state.deployerToken)
		response, err := r.clients.orch.GetCluster(ctx, getReq)
		if err != nil {
			return fmt.Errorf("cluster %s: %w", seed.id, err)
		}
		if response.Msg.GetCluster().GetName() != seed.name {
			return fmt.Errorf("cluster %s name mismatch: got %q want %q", seed.id, response.Msg.GetCluster().GetName(), seed.name)
		}
	}
	return nil
}

// phaseRouting configures the eight cluster artifact routes (REQ-065 table).
// ConfigureClusterRoute is idempotent on the route id.
func (r *runner) phaseRouting(ctx context.Context) error {
	for _, seed := range routeSeeds {
		req := connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
			Id: seed.id, ClusterId: seed.clusterKey,
			ArtifactType: seed.artifactType, Mode: seed.mode,
			SourcePrefix: seed.sourcePrefix, TargetPrefix: seed.targetPrefix,
		})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.ConfigureClusterRoute(ctx, req)
		if err != nil {
			return fmt.Errorf("configure route %s: %w", seed.id, err)
		}
		if response.Msg.GetRoute().GetId() == seed.id {
			r.cfg.log().Info("route configured", "route", seed.id)
		}
	}
	return nil
}

// checkCommittedRouting verifies each expected route exists with a matching
// mode and prefixes via GetClusterRoutes.
func (r *runner) checkCommittedRouting(ctx context.Context) error {
	byCluster := map[string][]routeSeed{}
	for _, seed := range routeSeeds {
		byCluster[seed.clusterKey] = append(byCluster[seed.clusterKey], seed)
	}
	for clusterID, expected := range byCluster {
		req := connect.NewRequest(&orchestratorv1.GetClusterRoutesRequest{ClusterId: clusterID})
		withAuth(req, r.state.deployerToken)
		response, err := r.clients.orch.GetClusterRoutes(ctx, req)
		if err != nil {
			return fmt.Errorf("get routes for %s: %w", clusterID, err)
		}
		seen := map[string]*orchestratorv1.ClusterRoute{}
		for _, route := range response.Msg.GetRoutes() {
			seen[route.GetId()] = route
		}
		for _, seed := range expected {
			route, ok := seen[seed.id]
			if !ok {
				return fmt.Errorf("route %s missing on cluster %s", seed.id, clusterID)
			}
			if route.GetMode() != seed.mode || route.GetSourcePrefix() != seed.sourcePrefix || route.GetTargetPrefix() != seed.targetPrefix {
				return fmt.Errorf("route %s configuration mismatch", seed.id)
			}
		}
	}
	return nil
}
