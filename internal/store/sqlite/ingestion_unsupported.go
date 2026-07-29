package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

var errPostgresIngestionRequired = errors.New("bundle ingestion requires PostgreSQL")

type unsupportedArtifactEventStore struct{}

func (unsupportedArtifactEventStore) CreateTx(*gorm.DB, *store.ArtifactEvent) error {
	return errPostgresIngestionRequired
}

func (unsupportedArtifactEventStore) GetBySourceAndEvent(context.Context, string, string) (*store.ArtifactEvent, error) {
	return nil, errPostgresIngestionRequired
}

type unsupportedValidationOutboxStore struct{}

func (unsupportedValidationOutboxStore) CreateTx(*gorm.DB, *store.ValidationOutboxEntry) error {
	return errPostgresIngestionRequired
}

func (unsupportedValidationOutboxStore) ClaimPending(context.Context, time.Time, int) ([]store.ValidationOutboxEntry, error) {
	return nil, errPostgresIngestionRequired
}

func (unsupportedValidationOutboxStore) UpdateTx(*gorm.DB, *store.ValidationOutboxEntry) error {
	return errPostgresIngestionRequired
}

type unsupportedBundleSubmissionStore struct{}

func (unsupportedBundleSubmissionStore) Submit(context.Context, store.BundleSubmission) (*store.ReleaseBundle, bool, error) {
	return nil, false, errPostgresIngestionRequired
}

type unsupportedArtifactEventSubmissionStore struct{}

func (unsupportedArtifactEventSubmissionStore) Record(context.Context, store.ArtifactEventSubmission) (*store.ArtifactEventSubmissionResult, error) {
	return nil, errPostgresIngestionRequired
}
