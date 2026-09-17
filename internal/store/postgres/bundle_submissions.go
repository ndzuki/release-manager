package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type bundleSubmissionStore struct {
	gorm       *gorm.DB
	bundles    *bundleStore
	candidates *candidateArtifactStore
	validation *validationOutboxStore
}

func (s *bundleSubmissionStore) Submit(
	ctx context.Context,
	submission store.BundleSubmission,
) (*store.ReleaseBundle, bool, error) {
	if submission.Bundle == nil {
		return nil, false, fmt.Errorf("submit bundle: nil bundle")
	}
	if hasBundleIdempotencyKey(submission) {
		replay, err := s.lookupIdempotentReplay(ctx, submission.Idempotency)
		if err != nil {
			return nil, false, err
		}
		if replay != nil {
			return replay, false, nil
		}
	}

	err := s.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.submitBundleTx(tx, submission)
	})
	if err == nil {
		return submission.Bundle, true, nil
	}
	if !isUniqueConstraint(err) && !errors.Is(err, store.ErrDuplicateKey) {
		return nil, false, err
	}

	if hasBundleIdempotencyKey(submission) {
		replay, replayErr := s.lookupIdempotentReplay(ctx, submission.Idempotency)
		if replayErr != nil {
			return nil, false, replayErr
		}
		if replay != nil {
			return replay, false, nil
		}
	}
	bundle, lookupErr := s.bundles.GetByDigest(ctx, submission.Bundle.DigestAlg, submission.Bundle.DigestValue)
	if lookupErr != nil {
		return nil, false, fmt.Errorf("read concurrent bundle submission: %w", lookupErr)
	}
	return bundle, false, nil
}

// hasBundleIdempotencyKey reports whether the submission carries a usable
// idempotency key.
func hasBundleIdempotencyKey(submission store.BundleSubmission) bool {
	return submission.Idempotency != nil && submission.Idempotency.Key != ""
}

// lookupIdempotentReplay returns the bundle stored under the submission's
// idempotency key. A nil bundle means no usable record exists, so the caller
// proceeds with the insert.
func (s *bundleSubmissionStore) lookupIdempotentReplay(
	ctx context.Context, record *store.IdempotencyRecord,
) (*store.ReleaseBundle, error) {
	replay, err := s.loadIdempotentBundle(ctx, record)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return replay, nil
}

// submitBundleTx writes the bundle, its candidate artifacts, the validation
// outbox entry, the audit outbox entry and the idempotency record in one
// transaction.
func (s *bundleSubmissionStore) submitBundleTx(tx *gorm.DB, submission store.BundleSubmission) error {
	if err := s.bundles.CreateTx(tx, submission.Bundle); err != nil {
		return err
	}
	digests := make([]store.ArtifactDigest, 0, len(submission.Candidates))
	for _, candidate := range submission.Candidates {
		if err := s.candidates.UpsertTx(tx, candidate); err != nil {
			return err
		}
		if err := s.candidates.UpsertLocationTx(tx, candidate.ID, candidate.Ref, "bundle", submission.Bundle.CreatedAt); err != nil {
			return err
		}
		digests = append(digests, store.ArtifactDigest{Digest: candidate.Digest, ArtifactType: candidate.ArtifactType})
	}
	if err := s.candidates.LinkToBundleTx(tx, submission.Bundle.ID, digests); err != nil {
		return err
	}
	if submission.ValidationEntry != nil {
		if err := s.validation.CreateTx(tx, submission.ValidationEntry); err != nil {
			return err
		}
	}
	if submission.Audit != nil {
		if err := insertApprovalOutboxTx(tx, submission.Audit); err != nil {
			return err
		}
	}
	if !hasBundleIdempotencyKey(submission) {
		return nil
	}
	return insertBundleIdempotencyTx(tx, submission.Bundle.ID, submission.Idempotency)
}

// insertBundleIdempotencyTx persists the bundle idempotency record, mapping a
// unique-constraint violation onto the store's duplicate-key sentinel.
func insertBundleIdempotencyTx(tx *gorm.DB, bundleID string, record *store.IdempotencyRecord) error {
	responseRef, err := json.Marshal(map[string]string{"bundle_id": bundleID})
	if err != nil {
		return fmt.Errorf("encode bundle idempotency response: %w", err)
	}
	record.ResponseRef = responseRef
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	if err := tx.Exec(`
		INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, record.Scope, record.Key,
		record.RequestHash, record.ResponseRef,
		record.ExpiresAt.UTC()).Error; err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert bundle idempotency record: %w", err)
	}
	return nil
}

func (s *bundleSubmissionStore) loadIdempotentBundle(
	ctx context.Context,
	record *store.IdempotencyRecord,
) (*store.ReleaseBundle, error) {
	var requestHash string
	var responseRef []byte
	var expiresAt time.Time
	row := s.gorm.WithContext(ctx).Raw(`
		SELECT request_hash, response_ref, expires_at
		FROM idempotency_records
		WHERE scope = ? AND text_key = ?
	`, record.Scope, record.Key).Row()
	if err := row.Scan(&requestHash, &responseRef, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get bundle idempotency record: %w", err)
	}
	if expiresAt.Before(time.Now().UTC()) {
		return nil, store.ErrNotFound
	}
	if requestHash != record.RequestHash {
		return nil, store.ErrIdempotencyConflict
	}
	var response struct {
		BundleID string `json:"bundle_id"`
	}
	if err := json.Unmarshal(responseRef, &response); err != nil {
		return nil, fmt.Errorf("decode bundle idempotency response: %w", err)
	}
	return s.bundles.Get(ctx, response.BundleID)
}

func insertApprovalOutboxTx(tx *gorm.DB, entry *store.ApprovalOutboxEntry) error {
	result := tx.Exec(`
		INSERT INTO audit_outbox (id, event_type, payload_json, created_at, delivered, delivered_at)
		VALUES (?, ?, ?::jsonb, ?, FALSE, NULL)
	`, entry.ID, entry.EventType, entry.PayloadJSON, entry.CreatedAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("insert bundle audit outbox: %w", result.Error)
	}
	return nil
}
