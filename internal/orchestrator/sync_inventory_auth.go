// Package orchestrator implements the release orchestration Connect service.
package orchestrator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
)

// NewSyncInventoryCertAuthInterceptor authenticates and authorizes
// SyncInventory calls on the agent gateway (D-107=A, TASK-080): the gateway
// listener's verified client certificate is required (no/invalid certificate
// → CodeUnauthenticated); the ADR-018 certificate serial (ca.CertSerial) is
// looked up via store.Operators().GetByCertSerial (unknown serial →
// CodeUnauthenticated); the request's operator_id/customer_id/cluster_id must
// match the registered Operator record (any mismatch → CodePermissionDenied);
// revoked/superseded operators are denied (CodePermissionDenied), aligned
// with the CommandStream identity path. The TLS state is injected by the
// gateway's gatewayTLSStateMiddleware (cmd/orchestrator).
func NewSyncInventoryCertAuthInterceptor(st store.Store, logger *slog.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			cert := verifiedClientCert(operator.TLSStateFromContext(ctx))
			if cert == nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("client certificate required"))
			}
			op, err := st.Operators().GetByCertSerial(ctx, ca.CertSerial(cert))
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					logger.Warn("sync inventory auth: unregistered operator certificate",
						"cert_serial", ca.CertSerial(cert))
					return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("operator certificate not registered"))
				}
				logger.Error("sync inventory auth: operator lookup failed", "error", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("lookup operator by cert serial: %w", err))
			}

			syncReq, ok := req.Any().(*orchestratorv1.SyncInventoryRequest)
			if !ok {
				return nil, connect.NewError(connect.CodeInternal, errors.New("unexpected request type for sync inventory"))
			}
			if syncReq.GetOperatorId() != op.ID ||
				syncReq.GetCustomerId() != op.CustomerID ||
				syncReq.GetClusterId() != op.ClusterID {
				logger.Warn("sync inventory auth: certificate identity mismatch",
					"operator_id", op.ID,
					"request_operator_id", syncReq.GetOperatorId(),
					"request_customer_id", syncReq.GetCustomerId(),
					"request_cluster_id", syncReq.GetClusterId(),
				)
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("certificate identity does not match operator"))
			}
			switch op.Status {
			case store.OperatorSuperseded:
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("operator superseded: re-enroll required"))
			case store.OperatorRevoked:
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("operator revoked"))
			}
			return next(ctx, req)
		})
	}
}

// verifiedClientCert returns the verified leaf certificate from the gateway
// TLS connection state, or nil when no client certificate was presented and
// verified (mixed mTLS contract: the listener uses VerifyClientCertIfGiven).
func verifiedClientCert(state *tls.ConnectionState) *x509.Certificate {
	if state == nil || len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		return nil
	}
	return state.VerifiedChains[0][0]
}
