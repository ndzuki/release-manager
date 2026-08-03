package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

func bumpAuthorizationSourceVersion(ctx context.Context, execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	result, err := execer.ExecContext(ctx, `UPDATE authorization_source_version SET version = version + 1 WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("bump authorization source version: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("bump authorization source version rows affected: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("authorization source version guard missing")
	}
	return nil
}
