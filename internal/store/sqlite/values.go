package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type valuesStore struct{ db *sql.DB }

func (s *valuesStore) Create(ctx context.Context, vr *store.ValuesRevision) error {
	if vr.StateVersion == 0 {
		vr.StateVersion = 1
	}
	if vr.CreatedAt.IsZero() {
		vr.CreatedAt = time.Now().UTC()
	}
	if vr.UpdatedAt.IsZero() {
		vr.UpdatedAt = vr.CreatedAt
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, revision, version, state_version, status, "values",
			digest, parent_revision_id, secret_refs, created_by, created_by_user_id,
			approved_by, approved_at, rejected_by, rejection_reason, submitted_at, decided_at,
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
			id, release_definition_id, revision, version, status, "values",
			digest, parent_revision_id, secret_refs, created_by,
			approved_by, approved_at, rejected_by, rejection_reason,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, '', '', ?, ?, ?, ?)
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, vr.StateVersion, vr.StateVersion, string(vr.Status), vr.Values,
		vr.Digest, vr.ParentRevisionID, vr.SecretRefs, vr.CreatedByUserID, vr.CreatedByUserID,
		formatOptionalTime(vr.SubmittedAt), formatOptionalTime(vr.DecidedAt),
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, vr.Version, string(vr.Status), vr.Values,
		vr.Digest, vr.ParentRevisionID, vr.SecretRefs, vr.CreatedBy,
		vr.ApprovedBy, formatOptionalTime(vr.ApprovedAt), vr.RejectedBy, vr.RejectionReason,
		vr.CreatedAt.UTC().Format(time.RFC3339Nano), vr.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert values revision: %w", err)
	}
	return nil
}

// CreateIfParentVersion atomically validates the parent revision version and inserts a draft.
func (s *valuesStore) CreateIfParentVersion(ctx context.Context, vr *store.ValuesRevision, expectedParentVersion int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create values revision: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	if vr.ParentRevisionID != "" {
		if expectedParentVersion <= 0 {
			return store.ErrOptimisticLock
		}
		var currentVersion int
		var status, parentDefinitionID string
		err = tx.QueryRowContext(ctx, `SELECT version, status, release_definition_id FROM values_revisions WHERE id = ?`, vr.ParentRevisionID).Scan(&currentVersion, &status, &parentDefinitionID)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrOptimisticLock
		}
		if err != nil {
			return fmt.Errorf("read parent values revision: %w", err)
		}
		if parentDefinitionID != vr.ReleaseDefinitionID || currentVersion != expectedParentVersion {
			return store.ErrOptimisticLock
		}
		if status != string(store.ValuesStatusApproved) && status != string(store.ValuesStatusSuperseded) {
			return store.ErrOptimisticLock
		}
	} else if expectedParentVersion != 0 {
		return store.ErrOptimisticLock
	}
	var activeDrafts int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM values_revisions
		WHERE release_definition_id = ? AND parent_revision_id = ? AND status = ?
	`, vr.ReleaseDefinitionID, vr.ParentRevisionID, string(store.ValuesStatusDraft)).Scan(&activeDrafts); err != nil {
		return fmt.Errorf("count active values drafts: %w", err)
	}
	if activeDrafts > 0 {
		return store.ErrOptimisticLock
	}

	var maxRevision sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM values_revisions WHERE release_definition_id = ?`, vr.ReleaseDefinitionID).Scan(&maxRevision); err != nil {
		return fmt.Errorf("get next values revision number: %w", err)
	}
	if maxRevision.Valid {
		vr.Revision = int(maxRevision.Int64) + 1
	} else {
		vr.Revision = 1
	}
	if vr.Version == 0 {
		vr.Version = 1
	}
	if vr.CreatedAt.IsZero() {
		vr.CreatedAt = time.Now().UTC()
	}
	if vr.UpdatedAt.IsZero() {
		vr.UpdatedAt = vr.CreatedAt
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, revision, version, status, "values",
			digest, parent_revision_id, secret_refs, created_by,
			approved_by, approved_at, rejected_by, rejected_at, rejection_reason,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, vr.Version, string(vr.Status), vr.Values,
		vr.Digest, vr.ParentRevisionID, vr.SecretRefs, vr.CreatedBy,
		vr.ApprovedBy, formatOptionalTime(vr.ApprovedAt), vr.RejectedBy, formatOptionalTime(vr.RejectedAt), vr.RejectionReason,
		vr.CreatedAt.UTC().Format(time.RFC3339Nano), vr.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert values revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create values revision: %w", err)
	}
	return nil
}

func (s *valuesStore) Get(ctx context.Context, id string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, valuesSelect+` WHERE id = ?`, id)
	return scanValues(row)
}

func (s *valuesStore) GetByDigest(ctx context.Context, definitionID, digest string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND digest = ?
	`, definitionID, digest)
	return scanValues(row)
}

func (s *valuesStore) GetLatestApproved(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND status = 'approved'
		ORDER BY revision DESC
		LIMIT 1
	`, definitionID)
	return scanValues(row)
}

func (s *valuesStore) List(ctx context.Context, definitionID string) ([]*store.ValuesRevision, error) {
	rows, err := s.db.QueryContext(ctx, valuesSelect+`
		WHERE release_definition_id = ?
		ORDER BY revision DESC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list values revisions: %w", err)
	}
	defer rows.Close()

	var revisions []*store.ValuesRevision
	for rows.Next() {
		vr, err := scanValues(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, vr)
	}
	return revisions, rows.Err()
}

// GetNextRevisionNumber returns max(revision)+1 for the given definition, or 1 if none exist.
func (s *valuesStore) GetNextRevisionNumber(ctx context.Context, definitionID string) (int, error) {
	var maxRevision sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT MAX(revision) FROM values_revisions
		WHERE release_definition_id = ?
	`, definitionID).Scan(&maxRevision); err != nil {
		return 0, fmt.Errorf("get next revision number: %w", err)
	}
	if maxRevision.Valid {
		return int(maxRevision.Int64) + 1, nil
	}
	return 1, nil
}

||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
func (s *valuesStore) Approve(ctx context.Context, id string, expectedVersion int, approvedBy string) (approvedRevision, supersededRevision *store.ValuesRevision, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin approve values revision: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	current, err := getValues(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if current.Version != expectedVersion {
		return nil, nil, store.ErrOptimisticLock
	}

	now := time.Now().UTC()
	var superseded *store.ValuesRevision
	row := tx.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND status = 'approved' AND id != ?
		ORDER BY revision DESC
		LIMIT 1
	`, current.ReleaseDefinitionID, id)
	superseded, err = scanValues(row)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, err
	}
	if errors.Is(err, store.ErrNotFound) {
		superseded = nil
	}
	if superseded != nil {
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE values_revisions
			SET status = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND status = ? AND version = ?
		`, string(store.ValuesStatusSuperseded), now.Format(time.RFC3339Nano),
			superseded.ID, string(store.ValuesStatusApproved), superseded.Version)
		if updateErr != nil {
			return nil, nil, fmt.Errorf("supersede values revision: %w", updateErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, nil, fmt.Errorf("supersede values revision rows: %w", rowsErr)
		}
		if rows == 0 {
			return nil, nil, store.ErrOptimisticLock
		}
		superseded.Status = store.ValuesStatusSuperseded
		superseded.Version++
		superseded.UpdatedAt = now
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = ?, version = version + 1, approved_by = ?, approved_at = ?,
			rejected_by = '', rejection_reason = '', updated_at = ?
		WHERE id = ? AND status = ? AND version = ?
	`, string(store.ValuesStatusApproved), approvedBy, now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), id, string(store.ValuesStatusDraft), expectedVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("approve values revision: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, nil, fmt.Errorf("approve values revision rows: %w", err)
	}
	if rows == 0 {
		return nil, nil, store.ErrOptimisticLock
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit approve values revision: %w", err)
	}

	current.Status = store.ValuesStatusApproved
	current.Version++
	current.ApprovedBy = approvedBy
	current.ApprovedAt = &now
	current.RejectedBy = ""
	current.RejectionReason = ""
	current.UpdatedAt = now
	return current, superseded, nil
}

func (s *valuesStore) Reject(ctx context.Context, id string, expectedVersion int, rejectedBy, reason string) (*store.ValuesRevision, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = ?, version = version + 1, rejected_by = ?, rejection_reason = ?,
			approved_by = '', approved_at = NULL, updated_at = ?
		WHERE id = ? AND status = ? AND version = ?
	`, string(store.ValuesStatusRejected), rejectedBy, reason, now.Format(time.RFC3339Nano),
		id, string(store.ValuesStatusDraft), expectedVersion)
	if err != nil {
		return nil, fmt.Errorf("reject values revision: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reject values revision rows: %w", err)
	}
	if rows == 0 {
		return nil, store.ErrOptimisticLock
	}
	return s.Get(ctx, id)
}

// Update persists status changes with optimistic locking on parent_revision_id.
func (s *valuesStore) Update(ctx context.Context, vr *store.ValuesRevision, expectedParentRev string) error {
	vr.UpdatedAt = time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = ?,
		    digest = ?,
		    secret_refs = ?,
		    updated_at = ?
		WHERE id = ? AND parent_revision_id = ?
	`, string(vr.Status), vr.Digest, vr.SecretRefs,
		vr.UpdatedAt.UTC().Format(time.RFC3339),
		vr.ID, expectedParentRev)
	if err != nil {
		return fmt.Errorf("update values revision: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update values revision rows: %w", err)
	}
	if n == 0 {
		return store.ErrOptimisticLock
	}
	return nil
}

const valuesSelect = `
	SELECT id, release_definition_id, revision, state_version, status, "values",
		digest, parent_revision_id, secret_refs, created_by_user_id,
		submitted_at, decided_at, created_at, updated_at
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
	SELECT id, release_definition_id, revision, version, status, "values",
		digest, parent_revision_id, secret_refs, created_by,
		approved_by, approved_at, rejected_by, rejection_reason,
		created_at, updated_at
	FROM values_revisions`

func getValues(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*store.ValuesRevision, error) {
	return scanValues(q.QueryRowContext(ctx, valuesSelect+` WHERE id = ?`, id))
}

func scanValues(row interface{ Scan(...interface{}) error }) (*store.ValuesRevision, error) {
	var (
		vr                     store.ValuesRevision
		status                 string
		submittedAt, decidedAt sql.NullString
		createdAt, updatedAt   string
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
		vr                   store.ValuesRevision
		status               string
		approvedAt           sql.NullString
		createdAt, updatedAt string
	)

	err := row.Scan(
		&vr.ID, &vr.ReleaseDefinitionID, &vr.Revision, &vr.StateVersion, &status, &vr.Values,
		&vr.Digest, &vr.ParentRevisionID, &vr.SecretRefs, &vr.CreatedByUserID,
		&submittedAt, &decidedAt, &createdAt, &updatedAt,
	)
	if err != nil {
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))

	err := row.Scan(
		&vr.ID, &vr.ReleaseDefinitionID, &vr.Revision, &vr.Version, &status, &vr.Values,
		&vr.Digest, &vr.ParentRevisionID, &vr.SecretRefs, &vr.CreatedBy,
		&vr.ApprovedBy, &approvedAt, &vr.RejectedBy, &vr.RejectionReason,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan values: %w", err)
	}

	vr.Status = store.ValuesStatus(status)
	var parseErr error
	if submittedAt.Valid {
		vr.SubmittedAt, parseErr = parseOptionalTime(submittedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse submitted_at: %w", parseErr)
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
	if approvedAt.Valid {
		parsedApprovedAt, parseErr := time.Parse(time.RFC3339Nano, approvedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse approved_at: %w", parseErr)
		}
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
		vr.ApprovedAt = &parsedApprovedAt
	}
	if decidedAt.Valid {
		vr.DecidedAt, parseErr = parseOptionalTime(decidedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse decided_at: %w", parseErr)
		}
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
	var errParse error
	vr.CreatedAt, errParse = time.Parse(time.RFC3339Nano, createdAt)
	if errParse != nil {
		return nil, fmt.Errorf("parse created_at: %w", errParse)
	}
	vr.CreatedAt, parseErr = parseSQLiteTime(createdAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse created_at: %w", parseErr)
	}
	vr.UpdatedAt, parseErr = parseSQLiteTime(updatedAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse updated_at: %w", parseErr)
||||||| parent of 92a0d2b (feat: ValuesRevision edit/approve frontend and backend (TASK-055))
	vr.UpdatedAt, errParse = time.Parse(time.RFC3339Nano, updatedAt)
	if errParse != nil {
		return nil, fmt.Errorf("parse updated_at: %w", errParse)
	}
	return &vr, nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	parsed, err := parseSQLiteTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseSQLiteTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time value %q", value)
}

func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
