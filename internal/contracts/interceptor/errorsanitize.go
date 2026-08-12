package interceptor

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

// AuthorizationReason is the capability implemented by stable authorization
// error types (internal/auth). Errors carrying it are intentional business
// outcomes (permission denied, policy unavailable, invalid actor context) and
// MUST NOT be downgraded even when their cause chain is wrapped.
type AuthorizationReason interface {
	AuthorizationReason() string
}

// NewErrorSanitizeInterceptor returns a Connect interceptor that sanitizes
// handler errors at the service boundary (AC-010-04):
//   - CodeInternal errors are reduced to a generic "internal error" message;
//     the full detail (SQL, stack traces, credentials) is logged server-side
//     with the request_id and never reaches the client.
//   - CodeUnavailable errors whose %w-wrapped cause is not a known stable
//     business error (store sentinels / structured store errors / stable
//     authorization errors) are downgraded to CodeInternal with the generic
//     message: "unavailable" is reserved for dependency failures (REQ-010
//     error model), so an internal detail leaking through the wrap chain is
//     treated as an internal failure. Wrapped stable business errors keep
//     their code, message, and details.
//   - other business errors keep their code, message, and details.
//   - request_id from ctx is attached as error metadata when missing.
//
// It must be wrapped by NewRequestIDInterceptor so ctx carries the request_id.
func NewErrorSanitizeInterceptor(logger *slog.Logger) connect.Interceptor {
	return errorSanitizeInterceptor{logger: logger}
}

type errorSanitizeInterceptor struct {
	logger *slog.Logger
}

func (i errorSanitizeInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err == nil {
			return resp, nil
		}
		return resp, i.sanitize(ctx, req.Spec().Procedure, err)
	}
}

func (i errorSanitizeInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i errorSanitizeInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := next(ctx, conn); err != nil {
			return i.sanitize(ctx, conn.Spec().Procedure, err)
		}
		return nil
	}
}

func (i errorSanitizeInterceptor) sanitize(ctx context.Context, procedure string, err error) error {
	rid := contracts.RequestID(ctx)

	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if connectErr.Code() != connect.CodeInternal {
			// 业务错误：稳定 code + message。仅 CodeUnavailable 需要核验
			// %w 包装链——REQ-010 将 unavailable 定义为依赖故障，链上若
			// 混入内部细节（SQL/连接错误等）必须泛化；命中已知稳定业务
			// 错误（store sentinel / 结构化 store 错误 / 授权错误族）则保留。
			if connectErr.Code() == connect.CodeUnavailable &&
				hasInternalWrap(connectErr.Unwrap()) {
				i.logDetail(procedure, rid, err)
				return genericInternalError(rid)
			}
			if rid != "" {
				connectErr.Meta().Set(contracts.RequestIDHeader, rid)
			}
			return connectErr
		}
		// 内部错误：完整细节仅日志，返回通用 message。
		i.logDetail(procedure, rid, err)
		return genericInternalError(rid)
	}

	i.logDetail(procedure, rid, err)
	return genericInternalError(rid)
}

// hasInternalWrap reports whether the cause chain of a business error carries
// internal detail. The chain is walked with errors.As-style unwrapping (any
// error implementing Unwrap()). A chain is stable when it contains a known
// business sentinel or structured error; a chain with no wrapped cause (plain
// errors.New / contracts.NewAppError) is a stable business message. Only a
// wrapped chain that matches none of the known business markers is treated as
// an internal leak.
func hasInternalWrap(cause error) bool {
	if cause == nil {
		return false
	}

	// 已知稳定业务 sentinel：命中即稳定，即使外层有 %w 包装。
	for _, sentinel := range []error{
		store.ErrNotFound,
		store.ErrOptimisticLock,
		store.ErrDuplicateKey,
		store.ErrReleaseBusy,
		store.ErrInvalidCursor,
		store.ErrBindingRevoked,
		store.ErrApprovalPending,
		store.ErrIdempotencyConflict,
		store.ErrInvalidState,
		store.ErrDefinitionOwnerUnresolved,
		store.ErrNotAuthorized,
		store.ErrEmergencyConflict,
		store.ErrAuthorizationStale,
	} {
		if errors.Is(cause, sentinel) {
			return false
		}
	}

	// 已知结构化 store 错误（errors.As 可穿透包装链）。
	if matchesStructured[*store.StateVersionConflictError](cause) ||
		matchesStructured[*store.OperationStateVersionConflictError](cause) ||
		matchesStructured[*store.InvalidValuesStateError](cause) ||
		matchesStructured[*store.ApprovalPendingError](cause) {
		return false
	}

	// 授权错误族：显式业务结果（permission denied / policy unavailable /
	// invalid actor context），即使 Cause 字段有内部包装也不降级。
	var authErr AuthorizationReason
	if errors.As(cause, &authErr) {
		return false
	}

	// 无包装链（errors.New 纯文本 / NewAppError）→ 稳定业务 message。
	if !hasUnwrap(cause) {
		return false
	}

	// 存在 %w 包装且未命中任何已知稳定错误 → 内部细节泄漏。
	return true
}

// matchesStructured reports whether cause's error chain contains an error
// assignable to *T (errors.As semantics).
func matchesStructured[T error](cause error) bool {
	var target T
	return errors.As(cause, &target)
}

// hasUnwrap reports whether err exposes an Unwrap() error method (a %w-style
// wrapper that errors.Is/errors.As can traverse).
func hasUnwrap(err error) bool {
	_, ok := err.(interface{ Unwrap() error })
	return ok
}

func (i errorSanitizeInterceptor) logDetail(procedure, rid string, err error) {
	if i.logger == nil {
		return
	}
	i.logger.Error("error sanitized at boundary",
		"request_id", rid, "procedure", procedure, "detail", err)
}

func genericInternalError(rid string) *connect.Error {
	sanitized := connect.NewError(connect.CodeInternal, errors.New("internal error"))
	if rid != "" {
		sanitized.Meta().Set(contracts.RequestIDHeader, rid)
	}
	return sanitized
}
