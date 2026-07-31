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
	if submission.Idempotency != nil && submission.Idempotency.Key != "" {
		replay, err := s.loadIdempotentBundle(ctx, submission.Idempotency)
		if err == nil {
			return replay, false, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, false, err
		}
	}

	err := s.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if submission.Idempotency != nil && submission.Idempotency.Key != "" {
			responseRef, err := json.Marshal(map[string]string{"bundle_id": submission.Bundle.ID})
			if err != nil {
				return fmt.Errorf("encode bundle idempotency response: %w", err)
			}
			submission.Idempotency.ResponseRef = responseRef
			if submission.Idempotency.ExpiresAt.IsZero() {
				submission.Idempotency.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
			}
			if err := tx.Exec(`
				INSERT INTO idempotency_records (scope, text_key, request_hash, response_ref, expires_at)
				VALUES (?, ?, ?, ?, ?)
			`, submission.Idempotency.Scope, submission.Idempotency.Key,
				submission.Idempotency.RequestHash, []byte(submission.Idempotency.ResponseRef),
				submission.Idempotency.ExpiresAt.UTC()).Error; err != nil {
				if isUniqueConstraint(err) {
					return store.ErrDuplicateKey
				}
				return fmt.Errorf("insert bundle idempotency record: %w", err)
			}
		}
		return nil
	})
	if err == nil {
		return submission.Bundle, true, nil
	}
	if !isUniqueConstraint(err) && !errors.Is(err, store.ErrDuplicateKey) {
		return nil, false, err
	}

	if submission.Idempotency != nil && submission.Idempotency.Key != "" {
		replay, replayErr := s.loadIdempotentBundle(ctx, submission.Idempotency)
		if replayErr == nil {
			return replay, false, nil
		}
		if !errors.Is(replayErr, store.ErrNotFound) {
			return nil, false, replayErr
		}
	}
	bundle, lookupErr := s.bundles.GetByDigest(ctx, submission.Bundle.DigestAlg, submission.Bundle.DigestValue)
	if lookupErr != nil {
		return nil, false, fmt.Errorf("read concurrent bundle submission: %w", lookupErr)
	}
	return bundle, false, nil
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
	`, entry.ID, entry.EventType, []byte(entry.PayloadJSON), entry.CreatedAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("insert bundle audit outbox: %w", result.Error)
	}
	return nil
}
