package app

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
)

// MaintenanceInterceptor rejects write RPCs while preserving explicitly
// allowlisted read procedures. Connect unary RPCs use POST for reads and writes,
// so procedure classification is required. See ADR-070-maintenance-cutover-authority-boundary.
func MaintenanceInterceptor(enabled bool, readOnly map[string]struct{}, logger *slog.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if enabled {
				if _, ok := readOnly[req.Spec().Procedure]; !ok {
					if logger != nil {
						logger.Warn("maintenance write rejected", "procedure", req.Spec().Procedure)
					}
					return nil, connect.NewError(connect.CodeUnavailable, errors.New("maintenance"))
				}
			}
			return next(ctx, req)
		}
	}
}
