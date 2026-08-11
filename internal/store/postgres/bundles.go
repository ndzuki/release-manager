package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

var defaultBundleCursorSecret = []byte("release-manager-bundle-pagination-v1")

type bundleStore struct{ gorm *DB }

type bundleCursor struct {
	QueryHash string `json:"query_hash"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Signature string `json:"signature"`
}

func (s *bundleStore) Create(ctx context.Context, bundle *store.ReleaseBundle) error {
	return s.gorm.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.CreateTx(tx, bundle)
	})
}

func (s *bundleStore) CreateTx(tx *gorm.DB, bundle *store.ReleaseBundle) error {
	if tx == nil {
		return fmt.Errorf("create release bundle: nil transaction")
	}
	if bundle.CreatedAt.IsZero() {
		bundle.CreatedAt = time.Now().UTC()
	}
	if bundle.Status == "" {
		bundle.Status = store.BundleReceived
	}
	if bundle.DigestAlg == "" {
		bundle.DigestAlg = "sha256"
	}

	imagesJSON, err := json.Marshal(bundle.Images)
	if err != nil {
		return fmt.Errorf("marshal bundle images: %w", err)
	}
	var archivedFrom string
	if bundle.ArchivedFromStatus != nil {
		archivedFrom = string(*bundle.ArchivedFromStatus)
	}
	if err := tx.Exec(`
		INSERT INTO release_bundles (
			id, name, digest_alg, digest_value, status,
			chart_ref, chart_version, chart_digest, images,
			git_commit, pipeline_id,
			signature_ref, signature_digest, sbom_ref, sbom_digest,
			provenance_ref, provenance_digest,
			archived_at, archived_from_status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, bundle.ID, bundle.Name, bundle.DigestAlg, bundle.DigestValue, string(bundle.Status),
		bundle.ChartRef, bundle.ChartVersion, bundle.ChartDigest, string(imagesJSON),
		bundle.GitCommit, bundle.PipelineID,
		bundle.SignatureRef, bundle.SignatureDigest, bundle.SBOMRef, bundle.SBOMDigest,
		bundle.ProvenanceRef, bundle.ProvenanceDigest,
		bundle.ArchivedAt, archivedFrom, bundle.CreatedAt.UTC()).Error; err != nil {
		return fmt.Errorf("insert release bundle: %w", err)
	}

	for position, image := range bundle.Images {
		if err := tx.Exec(`
			INSERT INTO release_bundle_image_bindings
				(bundle_id, ref, digest, values_path, value_kind, position)
			VALUES (?, ?, ?, ?, ?, ?)
		`, bundle.ID, image.Ref, image.Digest, image.ValuesPath, string(image.ValueKind), position).Error; err != nil {
			return fmt.Errorf("insert release bundle image binding: %w", err)
		}
	}
	return nil
}

func (s *bundleStore) Get(ctx context.Context, id string) (*store.ReleaseBundle, error) {
	return getBundle(ctx, s.gorm.gorm, `WHERE b.id = ?`, id)
}

func (s *bundleStore) GetByDigest(ctx context.Context, algorithm, value string) (*store.ReleaseBundle, error) {
	return getBundle(ctx, s.gorm.gorm, `WHERE b.digest_alg = ? AND b.digest_value = ?`, algorithm, value)
}

func (s *bundleStore) GetByAlias(ctx context.Context, alias string) (*store.ReleaseBundle, error) {
	return getBundle(ctx, s.gorm.gorm, `
		JOIN bundle_aliases AS a ON a.canonical_bundle_id = b.id
		WHERE a.alias = ?
	`, alias)
}

func getBundle(ctx context.Context, db *gorm.DB, suffix string, args ...any) (*store.ReleaseBundle, error) {
	row := db.WithContext(ctx).Raw(`
		SELECT b.id, b.name, b.digest_alg, b.digest_value, b.status,
			b.chart_ref, b.chart_version, b.chart_digest,
			b.git_commit, b.pipeline_id,
			b.signature_ref, b.signature_digest,
			b.sbom_ref, b.sbom_digest,
			b.provenance_ref, b.provenance_digest,
			b.archived_at, b.archived_from_status, b.created_at
		FROM release_bundles AS b `+suffix, args...).Row()
	bundle, err := scanBundle(row)
	if err != nil {
		return nil, err
	}
	images, err := loadBundleImages(ctx, db, []string{bundle.ID})
	if err != nil {
		return nil, err
	}
	bundle.Images = images[bundle.ID]
	return bundle, nil
}

func scanBundle(row interface{ Scan(...any) error }) (*store.ReleaseBundle, error) {
	var bundle store.ReleaseBundle
	var status string
	var archivedAt sql.NullTime
	var archivedFrom sql.NullString
	if err := row.Scan(
		&bundle.ID, &bundle.Name, &bundle.DigestAlg, &bundle.DigestValue, &status,
		&bundle.ChartRef, &bundle.ChartVersion, &bundle.ChartDigest,
		&bundle.GitCommit, &bundle.PipelineID,
		&bundle.SignatureRef, &bundle.SignatureDigest,
		&bundle.SBOMRef, &bundle.SBOMDigest,
		&bundle.ProvenanceRef, &bundle.ProvenanceDigest,
		&archivedAt, &archivedFrom, &bundle.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan release bundle: %w", err)
	}
	bundle.Status = store.BundleStatus(status)
	if archivedAt.Valid {
		bundle.ArchivedAt = &archivedAt.Time
	}
	if archivedFrom.Valid && archivedFrom.String != "" {
		value := store.BundleStatus(archivedFrom.String)
		bundle.ArchivedFromStatus = &value
	}
	return &bundle, nil
}

func loadBundleImages(ctx context.Context, db *gorm.DB, bundleIDs []string) (map[string][]store.BundleImage, error) {
	result := make(map[string][]store.BundleImage, len(bundleIDs))
	if len(bundleIDs) == 0 {
		return result, nil
	}
	rows, err := db.WithContext(ctx).Raw(`
		SELECT bundle_id, ref, digest, values_path, value_kind
		FROM release_bundle_image_bindings
		WHERE bundle_id IN ?
		ORDER BY bundle_id, position
	`, bundleIDs).Rows()
	if err != nil {
		return nil, fmt.Errorf("query release bundle image bindings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bundleID string
		var image store.BundleImage
		var valueKind string
		if err := rows.Scan(&bundleID, &image.Ref, &image.Digest, &image.ValuesPath, &valueKind); err != nil {
			return nil, fmt.Errorf("scan release bundle image binding: %w", err)
		}
		image.ValueKind = store.ImageValueKind(valueKind)
		result[bundleID] = append(result[bundleID], image)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release bundle image bindings: %w", err)
	}
	return result, nil
}

func (s *bundleStore) List(ctx context.Context, filter store.BundleListFilter) (*store.BundlePage, error) {
	pageSize := int(contracts.NormalizePageSize(int32(filter.PageSize))) //nolint:gosec // page_size 由输入契约 clamp 到 1-100，int32 转换无溢出。
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []store.BundleStatus{store.BundleReceived, store.BundleValidated}
	}

	where := []string{"b.status IN ?"}
	args := []any{statuses}
	if filter.ReleaseDefinitionID != "" {
		where = append(where, `EXISTS (
			SELECT 1 FROM release_definitions AS d
			WHERE d.id = ? AND (d.chart_name = '' OR b.chart_ref LIKE '%' || d.chart_name || '%')
		)`)
		args = append(args, filter.ReleaseDefinitionID)
	}
	if filter.ChartName != "" {
		where = append(where, `b.chart_ref LIKE '%' || ? || '%'`)
		args = append(args, filter.ChartName)
	}
	queryHash := bundleQueryHash(filter, statuses)
	if filter.PageToken != "" {
		cursor, err := decodeBundleCursor(filter.PageToken, queryHash)
		if err != nil {
			return nil, err
		}
		where = append(where, `(b.created_at, b.id) < (?, ?)`)
		args = append(args, cursor.CreatedAt, cursor.ID)
	}

	var total int64
	countArgs := append([]any(nil), args...)
	if filter.PageToken != "" {
		countArgs = countArgs[:len(countArgs)-2]
	}
	countWhere := where
	if filter.PageToken != "" {
		countWhere = where[:len(where)-1]
	}
	if err := s.gorm.gorm.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM release_bundles AS b WHERE `+strings.Join(countWhere, " AND "), countArgs...).Scan(&total).Error; err != nil {
		return nil, fmt.Errorf("count release bundles: %w", err)
	}

	rows, err := s.gorm.gorm.WithContext(ctx).Raw(`
		SELECT b.id, b.name, b.digest_alg, b.digest_value, b.status,
			b.chart_ref, b.chart_version, b.chart_digest,
			b.git_commit, b.pipeline_id,
			b.signature_ref, b.signature_digest,
			b.sbom_ref, b.sbom_digest,
			b.provenance_ref, b.provenance_digest,
			b.archived_at, b.archived_from_status, b.created_at
		FROM release_bundles AS b
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY b.created_at DESC, b.id DESC
		LIMIT ?
	`, append(args, pageSize+1)...).Rows()
	if err != nil {
		return nil, fmt.Errorf("query release bundles: %w", err)
	}
	defer rows.Close()

	bundles := make([]*store.ReleaseBundle, 0, pageSize+1)
	for rows.Next() {
		bundle, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, bundle)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release bundles: %w", err)
	}

	hasMore := len(bundles) > pageSize
	if hasMore {
		bundles = bundles[:pageSize]
	}
	ids := make([]string, len(bundles))
	for index, bundle := range bundles {
		ids[index] = bundle.ID
	}
	images, err := loadBundleImages(ctx, s.gorm.gorm, ids)
	if err != nil {
		return nil, err
	}
	for _, bundle := range bundles {
		bundle.Images = images[bundle.ID]
	}

	page := &store.BundlePage{Bundles: bundles, TotalSize: boundedInt32(total)}
	if hasMore {
		last := bundles[len(bundles)-1]
		page.NextPageToken, err = encodeBundleCursor(bundleCursor{
			QueryHash: queryHash,
			CreatedAt: last.CreatedAt.UTC().Format(time.RFC3339Nano),
			ID:        last.ID,
		})
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func bundleQueryHash(filter store.BundleListFilter, statuses []store.BundleStatus) string {
	values := make([]string, len(statuses))
	for index, status := range statuses {
		values[index] = string(status)
	}
	payload := strings.Join([]string{filter.ReleaseDefinitionID, strings.Join(values, ","), filter.ChartName}, "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func encodeBundleCursor(cursor bundleCursor) (string, error) {
	cursor.Signature = bundleCursorSignature(cursor.QueryHash, cursor.CreatedAt, cursor.ID)
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode bundle cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeBundleCursor(encoded, expectedQueryHash string) (*bundleCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, store.ErrInvalidCursor
	}
	var cursor bundleCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, store.ErrInvalidCursor
	}
	if cursor.QueryHash != expectedQueryHash || !hmac.Equal(
		[]byte(cursor.Signature),
		[]byte(bundleCursorSignature(cursor.QueryHash, cursor.CreatedAt, cursor.ID)),
	) {
		return nil, store.ErrInvalidCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil {
		return nil, store.ErrInvalidCursor
	}
	return &cursor, nil
}

func bundleCursorSignature(queryHash, createdAt, id string) string {
	mac := hmac.New(sha256.New, defaultBundleCursorSecret)
	_, _ = mac.Write([]byte(queryHash))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(createdAt))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(id))
	return hex.EncodeToString(mac.Sum(nil))
}

func boundedInt32(value int64) int32 {
	if value <= 0 {
		return 0
	}
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	return int32(value)
}

func (s *bundleStore) UpdateStatusTx(tx *gorm.DB, id string, from, to store.BundleStatus, validationErr string) error {
	if tx == nil {
		return fmt.Errorf("update release bundle status: nil transaction")
	}
	result := tx.Exec(`
		UPDATE release_bundles
		SET status = ?
		WHERE id = ? AND status = ?
	`, string(to), id, string(from))
	if result.Error != nil {
		return fmt.Errorf("update release bundle status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrOptimisticLock
	}
	_ = validationErr
	return nil
}

// ListForArchive returns bundle IDs eligible for archival.
func (s *bundleStore) ListForArchive(ctx context.Context, retentionDays int, terminalStates []store.OperationStatus) ([]string, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	terminal := make([]string, len(terminalStates))
	for index, status := range terminalStates {
		terminal[index] = string(status)
	}
	rows, err := s.gorm.gorm.WithContext(ctx).Raw(`
		SELECT b.id FROM release_bundles AS b
		WHERE b.status IN ('received', 'validated')
		  AND b.created_at < ?
		  AND NOT EXISTS (
			SELECT 1 FROM release_definitions AS d
			WHERE d.current_bundle_id = b.id AND d.status = 'active'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM operations AS o
			WHERE o.bundle_id = b.id AND o.status NOT IN ?
		  )
		ORDER BY b.created_at, b.id
	`, cutoff, terminal).Rows()
	if err != nil {
		return nil, fmt.Errorf("list bundles for archive: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan bundle id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Archive marks the given bundles as archived while preserving their prior state.
func (s *bundleStore) Archive(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := s.gorm.gorm.WithContext(ctx).Exec(`
		UPDATE release_bundles
		SET archived_from_status = status, status = 'archived', archived_at = ?
		WHERE id IN ? AND status IN ('received', 'validated', 'rejected')
	`, time.Now().UTC(), ids)
	if result.Error != nil {
		return 0, fmt.Errorf("archive bundles: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// DeleteBefore deletes bundles whose archive grace period has elapsed.
func (s *bundleStore) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.gorm.gorm.WithContext(ctx).Exec(`
		DELETE FROM release_bundles
		WHERE status = 'archived' AND archived_at < ?
	`, cutoff.UTC())
	if result.Error != nil {
		return 0, fmt.Errorf("delete bundles: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// Unarchive restores the exact state recorded at archive time.
func (s *bundleStore) Unarchive(ctx context.Context, id string) error {
	result := s.gorm.gorm.WithContext(ctx).Exec(`
		UPDATE release_bundles
		SET status = archived_from_status, archived_at = NULL, archived_from_status = ''
		WHERE id = ? AND status = 'archived'
		  AND archived_from_status IN ('received', 'validated', 'rejected')
	`, id)
	if result.Error != nil {
		return fmt.Errorf("unarchive bundle: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("unarchive bundle %s: %w", id, store.ErrNotFound)
	}
	return nil
}
