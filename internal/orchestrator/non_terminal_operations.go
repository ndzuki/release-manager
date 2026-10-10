package orchestrator

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

// listNonTerminalOperationsQuery is the validated, decoded form of one
// ListNonTerminalOperations request.
type listNonTerminalOperationsQuery struct {
	customerID string
	pageSize   int
	hasCursor  bool
	cursorTime time.Time
	cursorID   string
}

// ListNonTerminalOperations returns the cross-release feed of non-terminal
// operations, oldest first, keyset-paginated on (created_at, id) (TASK-276).
//
// It is the aggregate read the console's Operation hub and home "to do" widget
// need; ListOperations cannot serve them because it is scoped to one release
// definition. The store's unscoped ListNonTerminal cannot either: it has no
// tenant scope and no bound, so exposing it would leak across customers and
// return an unbounded result set. This handler closes both gaps: the page is
// limited to the customers the actor's organization holds an ACTIVE binding
// with, and pagination is bounded.
func (s *Service) ListNonTerminalOperations(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListNonTerminalOperationsRequest],
) (*connect.Response[orchestratorv1.ListNonTerminalOperationsResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, operationsError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
	}
	query, err := parseListNonTerminalOperationsQuery(req.Msg)
	if err != nil {
		return nil, err
	}
	customerIDs, err := s.nonTerminalOperationScope(ctx, actor, query.customerID)
	if err != nil {
		return nil, err
	}

	page, err := s.store.Operations().ListNonTerminalScoped(ctx, store.NonTerminalOperationQuery{
		CustomerIDs: customerIDs,
		PageSize:    query.pageSize,
		HasCursor:   query.hasCursor,
		CursorTime:  query.cursorTime,
		CursorID:    query.cursorID,
	})
	if err != nil {
		return nil, s.stableInternalError("list non-terminal operations", err)
	}

	response := &orchestratorv1.ListNonTerminalOperationsResponse{
		Operations: make([]*orchestratorv1.NonTerminalOperationSummary, 0, len(page.Rows)),
	}
	for _, row := range page.Rows {
		response.Operations = append(response.Operations, toNonTerminalOperationSummary(row))
	}
	if page.HasMore && len(page.Rows) > 0 {
		last := page.Rows[len(page.Rows)-1].Operation
		response.NextPageToken = contracts.EncodeCursor(last.CreatedAt, last.ID)
	}
	return connect.NewResponse(response), nil
}

// parseListNonTerminalOperationsQuery validates the request shape: a negative
// page_size is refused (a >100 value is clamped by contracts.NormalizePageSize,
// matching ListOperations), and a non-empty token must decode.
func parseListNonTerminalOperationsQuery(
	msg *orchestratorv1.ListNonTerminalOperationsRequest,
) (listNonTerminalOperationsQuery, error) {
	query := listNonTerminalOperationsQuery{customerID: msg.GetCustomerId()}
	if msg.GetPageSize() < 0 {
		return query, operationsError(connect.CodeInvalidArgument, "invalid_page_size", "page_size must not be negative")
	}
	query.pageSize = int(contracts.NormalizePageSize(msg.GetPageSize()))
	if token := msg.GetPageToken(); token != "" {
		cursorTime, cursorID, err := contracts.DecodeCursor(token)
		if err != nil {
			return query, operationsError(connect.CodeInvalidArgument, "invalid_page_token", "page_token is invalid or expired")
		}
		query.hasCursor = true
		query.cursorTime = cursorTime
		query.cursorID = cursorID
	}
	return query, nil
}

// nonTerminalOperationScope resolves which customers' operations the actor may
// see. The organization always comes from the session (ADR-006), never from the
// request; the optional customer_id only narrows that scope and must itself be
// under an active binding.
func (s *Service) nonTerminalOperationScope(
	ctx context.Context,
	actor authctx.Actor,
	customerID string,
) ([]string, error) {
	if _, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, operationsError(connect.CodePermissionDenied, "membership_inactive", "actor has no active membership")
		}
		return nil, s.stableInternalError("membership lookup", err)
	}
	if customerID != "" {
		if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, customerID); err != nil {
			return nil, operationsError(connect.CodePermissionDenied, "binding_revoked", "organization-customer binding is revoked")
		}
		return []string{customerID}, nil
	}
	bindings, err := s.store.Bindings().ListByOrg(ctx, actor.OrganizationID)
	if err != nil {
		return nil, s.stableInternalError("list organization bindings", err)
	}
	customerIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Status == store.BindingActive {
			customerIDs = append(customerIDs, binding.CustomerID)
		}
	}
	return customerIDs, nil
}

func toNonTerminalOperationSummary(row *store.NonTerminalOperationRow) *orchestratorv1.NonTerminalOperationSummary {
	operation := row.Operation
	return &orchestratorv1.NonTerminalOperationSummary{
		OperationId:           operation.ID,
		ReleaseDefinitionId:   operation.ReleaseDefinitionID,
		ReleaseDefinitionName: row.DefinitionName,
		CustomerId:            row.CustomerID,
		CustomerName:          row.CustomerName,
		OperationType:         string(operation.OperationType),
		State:                 string(operation.Status),
		CreatedAt:             timestamppb.New(operation.CreatedAt),
		UpdatedAt:             timestamppb.New(operation.UpdatedAt),
		Emergency:             operation.OperationType == store.OperationEmergency,
		Revision:              operationSummaryRevision(operation),
	}
}
