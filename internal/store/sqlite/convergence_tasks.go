package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type convergenceTaskStore struct{ db *sql.DB }

func (s *convergenceTaskStore) Create(ctx context.Context, task *store.ConvergenceTask) error {
	return insertConvergenceTask(ctx, s.db, task)
}

func insertConvergenceTask(ctx context.Context, execer operationExecer, task *store.ConvergenceTask) error {
	if task.SubmittedAt.IsZero() {
		task.SubmittedAt = time.Now().UTC()
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = task.SubmittedAt
	}
	if task.Status == "" {
		task.Status = "pending_promotion"
	}
	var convergedAt *string
	if task.ConvergedAt != nil {
		value := task.ConvergedAt.UTC().Format(time.RFC3339Nano)
		convergedAt = &value
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO convergence_tasks (
			id, operation_id, release_definition_id, action, target_summary,
			reason, promotion_paths, status, active_revision_id,
			active_revision_status, last_rejection_reason, submitted_at,
			converged_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.OperationID, task.ReleaseDefinitionID, string(task.Action), task.TargetSummary,
		task.Reason, []byte(task.PromotionPaths), task.Status, task.ActiveRevisionID,
		task.ActiveRevisionStatus, task.LastRejectionReason,
		task.SubmittedAt.UTC().Format(time.RFC3339Nano), convergedAt,
		task.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert convergence task: %w", err)
	}
	return nil
}

func (s *convergenceTaskStore) Get(ctx context.Context, id string) (*store.ConvergenceTask, error) {
	return getConvergenceTask(ctx, s.db, id)
}

func getConvergenceTask(ctx context.Context, queryer operationQueryer, id string) (*store.ConvergenceTask, error) {
	return scanConvergenceTask(queryer.QueryRowContext(ctx, convergenceTaskSelect+` WHERE id = ?`, id))
}

func (s *convergenceTaskStore) ListByDefinition(ctx context.Context, definitionID, statusFilter string) ([]*store.ConvergenceTask, error) {
	query := convergenceTaskSelect + ` WHERE release_definition_id = ?`
	args := []any{definitionID}
	if statusFilter != "" {
		query += ` AND status = ?`
		args = append(args, statusFilter)
	}
	query += ` ORDER BY submitted_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list convergence tasks: %w", err)
	}
	defer rows.Close()
	tasks := make([]*store.ConvergenceTask, 0)
	for rows.Next() {
		task, err := scanConvergenceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate convergence tasks: %w", err)
	}
	return tasks, nil
}

func (s *convergenceTaskStore) GetByOperationID(ctx context.Context, operationID string) (*store.ConvergenceTask, error) {
	return getConvergenceTaskByOperation(ctx, s.db, operationID)
}

func getConvergenceTaskByOperation(ctx context.Context, queryer operationQueryer, operationID string) (*store.ConvergenceTask, error) {
	return scanConvergenceTask(queryer.QueryRowContext(ctx, convergenceTaskSelect+` WHERE operation_id = ?`, operationID))
}

func (s *convergenceTaskStore) HasPendingPromotionForDefinition(ctx context.Context, definitionID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM convergence_tasks
		WHERE release_definition_id = ? AND status = 'pending_promotion'
	`, definitionID).Scan(&count); err != nil {
		return false, fmt.Errorf("count pending convergence tasks: %w", err)
	}
	return count > 0, nil
}

func (s *convergenceTaskStore) HasPendingPromotionPath(ctx context.Context, definitionID string, promotionPaths []string) (bool, error) {
	if len(promotionPaths) == 0 {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT promotion_paths FROM convergence_tasks
		WHERE release_definition_id = ? AND status = 'pending_promotion'
	`, definitionID)
	if err != nil {
		return false, fmt.Errorf("list pending promotion paths: %w", err)
	}
	defer rows.Close()
	wanted := make(map[string]struct{}, len(promotionPaths))
	for _, path := range promotionPaths {
		wanted[path] = struct{}{}
	}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return false, fmt.Errorf("scan pending promotion paths: %w", err)
		}
		var existing []string
		if err := json.Unmarshal(encoded, &existing); err != nil {
			return false, fmt.Errorf("decode pending promotion paths: %w", err)
		}
		for _, path := range existing {
			if _, ok := wanted[path]; ok {
				return true, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate pending promotion paths: %w", err)
	}
	return false, nil
}

func (s *convergenceTaskStore) MarkConverged(ctx context.Context, id, revisionID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		UPDATE convergence_tasks
		SET status = 'converged', active_revision_id = ?, active_revision_status = 'approved', converged_at = ?
		WHERE id = ? AND status = 'pending_promotion'
	`, revisionID, now, id)
	if err != nil {
		return fmt.Errorf("mark convergence task converged: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("convergence rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *convergenceTaskStore) BindRevision(ctx context.Context, id, revisionID, revisionStatus string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE convergence_tasks SET active_revision_id = ?, active_revision_status = ? WHERE id = ?
	`, revisionID, revisionStatus, id)
	if err != nil {
		return fmt.Errorf("bind convergence revision: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("bind revision rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

const convergenceTaskSelect = `
	SELECT id, operation_id, release_definition_id, action, target_summary,
		reason, promotion_paths, status, active_revision_id,
		active_revision_status, last_rejection_reason, submitted_at,
		converged_at, created_at
	FROM convergence_tasks`

func scanConvergenceTask(row interface{ Scan(...any) error }) (*store.ConvergenceTask, error) {
	var task store.ConvergenceTask
	var action string
	var promotionPaths []byte
	var activeRevisionID, activeRevisionStatus, lastRejectionReason, convergedAt sql.NullString
	var submittedAt, createdAt string
	if err := row.Scan(
		&task.ID, &task.OperationID, &task.ReleaseDefinitionID, &action, &task.TargetSummary,
		&task.Reason, &promotionPaths, &task.Status, &activeRevisionID,
		&activeRevisionStatus, &lastRejectionReason, &submittedAt,
		&convergedAt, &createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan convergence task: %w", err)
	}
	task.Action = store.EmergencyAction(action)
	task.PromotionPaths = append(json.RawMessage(nil), promotionPaths...)
	if activeRevisionID.Valid {
		task.ActiveRevisionID = &activeRevisionID.String
	}
	if activeRevisionStatus.Valid {
		task.ActiveRevisionStatus = &activeRevisionStatus.String
	}
	if lastRejectionReason.Valid {
		task.LastRejectionReason = &lastRejectionReason.String
	}
	var err error
	task.SubmittedAt, err = time.Parse(time.RFC3339Nano, submittedAt)
	if err != nil {
		return nil, fmt.Errorf("parse convergence submitted_at: %w", err)
	}
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse convergence created_at: %w", err)
	}
	if convergedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, convergedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse convergence converged_at: %w", parseErr)
		}
		task.ConvergedAt = &parsed
	}
	return &task, nil
}
