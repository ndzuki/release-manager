package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type artifactEventSubmissionStore struct {
	gorm       *gorm.DB
	events     *artifactEventStore
	candidates *candidateArtifactStore
}

func (s *artifactEventSubmissionStore) Record(
	ctx context.Context,
	submission store.ArtifactEventSubmission,
) (*store.ArtifactEventSubmissionResult, error) {
	if submission.Event == nil {
		return nil, fmt.Errorf("record artifact event: nil event")
	}

	result := &store.ArtifactEventSubmissionResult{}
	err := s.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.events.CreateTx(tx, submission.Event); err != nil {
			return err
		}
		result.Created = true
		result.Event = submission.Event
		now := submission.Event.ReceivedAt
		if now.IsZero() {
			now = time.Now().UTC()
		}
		for _, candidate := range submission.Candidates {
			var existed int64
			if err := tx.Raw(`
				SELECT COUNT(*) FROM candidate_artifacts WHERE digest = ? AND artifact_type = ?
			`, candidate.Digest, string(candidate.ArtifactType)).Scan(&existed).Error; err != nil {
				return fmt.Errorf("check candidate artifact existence: %w", err)
			}
			if err := s.candidates.UpsertTx(tx, candidate); err != nil {
				return err
			}
			if err := s.candidates.UpsertLocationTx(tx, candidate.ID, candidate.Ref, submission.Event.SourceID, now); err != nil {
				return err
			}
			if existed == 0 {
				result.NewCandidates++
			} else {
				result.UpdatedLocations++
			}
		}
		if submission.Audit != nil {
			if err := insertApprovalOutboxTx(tx, submission.Audit); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, store.ErrDuplicateKey) && !isUniqueConstraint(err) {
		return nil, err
	}
	existing, lookupErr := s.events.GetBySourceAndEvent(ctx, submission.Event.SourceID, submission.Event.EventID)
	if lookupErr != nil {
		return nil, fmt.Errorf("read duplicate artifact event: %w", lookupErr)
	}
	if existing.PayloadSHA256 != submission.Event.PayloadSHA256 {
		return nil, store.ErrIdempotencyConflict
	}
	return &store.ArtifactEventSubmissionResult{Event: existing, Created: false}, nil
}
