package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type bundleStore struct{ db *sql.DB }

func (s *bundleStore) Create(ctx context.Context, b *store.ReleaseBundle) error {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}

	imagesJSON, err := json.Marshal(b.Images)
	if err != nil {
		return fmt.Errorf("marshal images: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO release_bundles (
			id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest,
			images,
			git_commit, pipeline_id,
			signature_ref, sbom_ref, provenance_ref,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		b.ID, b.Name, b.DigestAlg, b.DigestValue, string(b.Status),
		b.ChartRef, b.ChartVersion, b.ChartDigest,
		string(imagesJSON),
		b.GitCommit, b.PipelineID,
		b.SignatureRef, b.SBOMRef, b.ProvenanceRef,
		b.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert release_bundle: %w", err)
	}
	return nil
}

func (s *bundleStore) Get(ctx context.Context, id string) (*store.ReleaseBundle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest,
			images,
			git_commit, pipeline_id,
			signature_ref, sbom_ref, provenance_ref,
			created_at
		FROM release_bundles WHERE id = ?
	`, id)
	return scanBundle(row)
}

func (s *bundleStore) GetByDigest(ctx context.Context, alg, value string) (*store.ReleaseBundle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest,
			images,
			git_commit, pipeline_id,
			signature_ref, sbom_ref, provenance_ref,
			created_at
		FROM release_bundles WHERE digest_alg = ? AND digest_value = ?
	`, alg, value)
	return scanBundle(row)
}

func scanBundle(row interface{ Scan(...interface{}) error }) (*store.ReleaseBundle, error) {
	var (
		id, name, digestAlg, digestValue, status string
		chartRef, chartVersion, chartDigest      string
		imagesJSON                               string
		gitCommit, pipelineID                    string
		sigRef, sbomRef, provRef                 string
		createdAt                                string
	)

	if err := row.Scan(
		&id, &name, &digestAlg, &digestValue, &status,
		&chartRef, &chartVersion, &chartDigest,
		&imagesJSON,
		&gitCommit, &pipelineID,
		&sigRef, &sbomRef, &provRef,
		&createdAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan release_bundle: %w", err)
	}

	var images []store.BundleImage
	if imagesJSON != "" {
		if err := json.Unmarshal([]byte(imagesJSON), &images); err != nil {
			return nil, fmt.Errorf("unmarshal bundle images: %w", err)
		}
	}

	ts, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	return &store.ReleaseBundle{
		ID:            id,
		Name:          name,
		DigestAlg:     digestAlg,
		DigestValue:   digestValue,
		Status:        store.BundleStatus(status),
		ChartRef:      chartRef,
		ChartVersion:  chartVersion,
		ChartDigest:   chartDigest,
		Images:        images,
		GitCommit:     gitCommit,
		PipelineID:    pipelineID,
		SignatureRef:  sigRef,
		SBOMRef:       sbomRef,
		ProvenanceRef: provRef,
		CreatedAt:     ts,
	}, nil
}
