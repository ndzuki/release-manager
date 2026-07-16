//nolint:dupl // Customer and Cluster handlers share the same CRUD pattern
package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateCluster creates a new cluster under a customer.
func (s *Service) CreateCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateClusterRequest],
) (*connect.Response[orchestratorv1.CreateClusterResponse], error) {
	msg := req.Msg

	// Verify customer exists and is active.
	cust, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", msg.GetCustomerId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cust.Status == store.CustomerDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer %q is disabled", msg.GetCustomerId()))
	}

	id := msg.GetId()
	if id == "" {
		id = uuid.New().String()
	}

	c := &store.Cluster{
		ID:            id,
		Name:          msg.GetName(),
		CustomerID:    msg.GetCustomerId(),
		KubeconfigRef: msg.GetKubeconfigRef(),
		Status:        store.ClusterActive,
	}

	if err := s.store.Clusters().Create(ctx, c); err != nil {
		s.logger.Error("create cluster failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create cluster: %w", err))
	}

	s.logger.Info("cluster created", "id", c.ID, "customer_id", c.CustomerID)
	return connect.NewResponse(&orchestratorv1.CreateClusterResponse{
		Cluster: toProtoCluster(c),
	}), nil
}

// GetCluster retrieves a cluster by ID.
func (s *Service) GetCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetClusterRequest],
) (*connect.Response[orchestratorv1.GetClusterResponse], error) {
	c, err := s.store.Clusters().Get(ctx, req.Msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", req.Msg.GetClusterId()))
		}
		s.logger.Error("get cluster failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&orchestratorv1.GetClusterResponse{
		Cluster: toProtoCluster(c),
	}), nil
}

// ListClusters lists all clusters, optionally filtered by customer_id.
func (s *Service) ListClusters(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListClustersRequest],
) (*connect.Response[orchestratorv1.ListClustersResponse], error) {
	customerID := req.Msg.GetCustomerId()

	var clusters []*store.Cluster
	var err error
	if customerID != "" {
		clusters, err = s.store.Clusters().List(ctx, customerID)
	} else {
		clusters, err = s.store.Clusters().ListAll(ctx)
	}
	if err != nil {
		s.logger.Error("list clusters failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoClusters := make([]*commonv1.Cluster, 0, len(clusters))
	for _, c := range clusters {
		protoClusters = append(protoClusters, toProtoCluster(c))
	}

	return connect.NewResponse(&orchestratorv1.ListClustersResponse{
		Clusters: protoClusters,
	}), nil
}

// DisableCluster disables a cluster. Disabled clusters cannot be release targets.
func (s *Service) DisableCluster(
	ctx context.Context,
	req *connect.Request[orchestratorv1.DisableClusterRequest],
) (*connect.Response[orchestratorv1.DisableClusterResponse], error) {
	c, err := s.store.Clusters().Get(ctx, req.Msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", req.Msg.GetClusterId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	c.Status = store.ClusterDisabled
	if err := s.store.Clusters().Update(ctx, c); err != nil {
		s.logger.Error("disable cluster failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("disable cluster: %w", err))
	}

	s.logger.Warn("cluster disabled", "id", c.ID, "customer_id", c.CustomerID)
	return connect.NewResponse(&orchestratorv1.DisableClusterResponse{}), nil
}

// toProtoCluster converts a store.Cluster to a commonv1.Cluster proto message.
func toProtoCluster(c *store.Cluster) *commonv1.Cluster {
	return &commonv1.Cluster{
		Id:            c.ID,
		Name:          c.Name,
		CustomerId:    c.CustomerID,
		KubeconfigRef: c.KubeconfigRef,
	}
}
