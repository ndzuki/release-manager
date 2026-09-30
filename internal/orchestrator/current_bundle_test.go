package orchestrator

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"

	"github.com/ndzuki/release-manager/internal/store"
)

// writeCurrentBundleDirect writes the definition pointer that the operation-creation UoW owns:
// DefinitionStore.SetCurrentBundle was removed with the TASK-226 dead-surface batch (it had no
// shipping caller), so fixtures that need the pointer write it themselves.
func writeCurrentBundleDirect(ctx context.Context, st store.Store, definitionID, bundleID string) error {
	if sqliteStore, ok := st.(interface{ DB() *sql.DB }); ok {
		_, err := sqliteStore.DB().ExecContext(ctx,
			`UPDATE release_definitions SET current_bundle_id = ? WHERE id = ?`, bundleID, definitionID)
		return err
	}
	if pgStore, ok := st.(interface{ GORM() *gorm.DB }); ok {
		return pgStore.GORM().Exec(
			`UPDATE release_definitions SET current_bundle_id = $1 WHERE id = $2`, bundleID, definitionID).Error
	}
	return fmt.Errorf("store has no DB accessor")
}
