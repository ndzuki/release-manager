package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

// listOperationsQuery is the validated, decoded form of one ListOperations
// request.
type listOperationsQuery struct {
	definitionID string
	status       store.OperationStatus
	pageSize     int
	hasCursor    bool
	cursorTime   time.Time
	cursorID     string
}

// ListOperations returns one release definition's operation history, newest
// first, keyset-paginated on (created_at, id). REQ-056 uses it to pick a
// successful ROLLBACK target; TASK-095 replaced the handler that always
// answered CodeUnimplemented.
func (s *Service) ListOperations(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListOperationsRequest],
) (*connect.Response[orchestratorv1.ListOperationsResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, operationsError(connect.CodeUnauthenticated, "authentication_required", "authentication required")
	}
	query, err := parseListOperationsQuery(req.Msg)
	if err != nil {
		return nil, err
	}

	definition, err := s.store.Definitions().Get(ctx, query.definitionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, operationsError(connect.CodeNotFound, "release_definition_not_found", "release definition not found")
	}
	if err != nil {
		return nil, s.stableInternalError("get release definition", err)
	}
	if err := s.authorizeReadDefinition(ctx, definition, actor); err != nil {
		return nil, err
	}

	operations, err := s.store.Operations().List(ctx, definition.ID)
	if err != nil {
		return nil, s.stableInternalError("list operations", err)
	}
	operations = filterOperationsByStatus(operations, query.status)
	sortOperationsNewestFirst(operations)

	start, end := operationsPageBounds(operations, query)
	response := &orchestratorv1.ListOperationsResponse{
		Operations: make([]*orchestratorv1.OperationSummary, 0, end-start),
	}
	for _, operation := range operations[start:end] {
		response.Operations = append(response.Operations, toOperationSummary(operation))
	}
	if end < len(operations) && end > start {
		last := operations[end-1]
		response.NextCursor = contracts.EncodeCursor(last.CreatedAt, last.ID)
	}
	return connect.NewResponse(response), nil
}

// parseListOperationsQuery applies the REQ-056 request validation: a definition
// is required, the status filter must be a stored status, the limit must not be
// negative, and a non-empty cursor must decode.
func parseListOperationsQuery(msg *orchestratorv1.ListOperationsRequest) (listOperationsQuery, error) {
	query := listOperationsQuery{definitionID: msg.GetReleaseDefinitionId()}
	if query.definitionID == "" {
		return query, operationsError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	status, err := parseOperationStatusFilter(msg.GetStatusFilter())
	if err != nil {
		return query, err
	}
	query.status = status
	if msg.GetLimit() < 0 {
		return query, operationsError(connect.CodeInvalidArgument, "invalid_page_size", "limit must not be negative")
	}
	query.pageSize = int(contracts.NormalizePageSize(msg.GetLimit()))
	if cursor := msg.GetCursor(); cursor != "" {
		cursorTime, cursorID, err := contracts.DecodeCursor(cursor)
		if err != nil {
			return query, operationsError(connect.CodeInvalidArgument, "invalid_cursor", "operation cursor is invalid or expired")
		}
		query.hasCursor = true
		query.cursorTime = cursorTime
		query.cursorID = cursorID
	}
	return query, nil
}

func filterOperationsByStatus(operations []*store.Operation, status store.OperationStatus) []*store.Operation {
	if status == "" {
		return operations
	}
	filtered := make([]*store.Operation, 0, len(operations))
	for _, operation := range operations {
		if operation.Status == status {
			filtered = append(filtered, operation)
		}
	}
	return filtered
}

// sortOperationsNewestFirst orders rows newest first with the id as a
// deterministic tiebreaker, so the keyset cursor cannot skip or repeat a row
// with an identical created_at.
func sortOperationsNewestFirst(operations []*store.Operation) {
	sort.SliceStable(operations, func(i, j int) bool {
		if !operations[i].CreatedAt.Equal(operations[j].CreatedAt) {
			return operations[i].CreatedAt.After(operations[j].CreatedAt)
		}
		return operations[i].ID > operations[j].ID
	})
}

// operationsPageBounds resolves the [start, end) window for the page. A cursor
// positions the window at the first row strictly older than the cursor row; an
// unknown cursor yields an empty page rather than a duplicate one.
func operationsPageBounds(operations []*store.Operation, query listOperationsQuery) (start, end int) {
	if query.hasCursor {
		start = operationsAfterCursor(operations, query.cursorTime, query.cursorID)
	}
	return start, min(start+query.pageSize, len(operations))
}

func operationsAfterCursor(operations []*store.Operation, cursorTime time.Time, cursorID string) int {
	for index, operation := range operations {
		if operation.CreatedAt.Before(cursorTime) ||
			(operation.CreatedAt.Equal(cursorTime) && operation.ID < cursorID) {
			return index
		}
	}
	return len(operations)
}

func toOperationSummary(operation *store.Operation) *orchestratorv1.OperationSummary {
	return &orchestratorv1.OperationSummary{
		OperationId:   operation.ID,
		OperationType: string(operation.OperationType),
		State:         string(operation.Status),
		Revision:      operationSummaryRevision(operation),
		CreatedAt:     timestamppb.New(operation.CreatedAt),
	}
}

// operationSummaryRevision reports the Helm revision the operation acts on:
// the ROLLBACK target when one is set, otherwise the revision the operation was
// computed against.
func operationSummaryRevision(operation *store.Operation) int32 {
	revision := operation.ExpectedRevision
	if operation.TargetRevision > 0 {
		revision = operation.TargetRevision
	}
	return int32(revision) //nolint:gosec // Helm revisions are bounded integers
}

// parseOperationStatusFilter maps the request filter onto a stored operation
// status. An empty filter means "all statuses".
func parseOperationStatusFilter(filter string) (store.OperationStatus, error) {
	if filter == "" {
		return "", nil
	}
	status := store.OperationStatus(filter)
	switch status {
	case store.StatusPending, store.StatusPreflight, store.StatusQueued, store.StatusRunning,
		store.StatusCancelling, store.StatusSucceeded, store.StatusFailed,
		store.StatusCancelled, store.StatusTimeout:
		return status, nil
	default:
		return "", operationsError(connect.CodeInvalidArgument, "invalid_status_filter",
			fmt.Sprintf("unsupported operation status filter %q", filter))
	}
}

func operationsError(code connect.Code, reason, message string) *connect.Error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", reason)
	return err
}
