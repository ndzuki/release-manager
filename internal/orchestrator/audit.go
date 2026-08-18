package orchestrator

import (
	"context"
	"fmt"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

func (s *CleanupService) emitGCAudit(ctx context.Context, key string, resp *orchestratorv1.RunCleanupResponse, errs []string) {
	if s.audit == nil {
		return
	}
	status := "succeeded"
	if len(errs) > 0 {
		status = "failed"
	}
	summary := fmt.Sprintf("deleted_bundles=%d deleted_candidates=%d deleted_preflights=%d skipped_bundles=%d errors=%d", resp.GetDeletedBundles(), resp.GetDeletedCandidates(), resp.GetDeletedPreflights(), resp.GetSkippedBundles(), len(errs))
	actorKind, actorID, organizationID, role := store.AuditActorSystem, "cleanup", "system", "system"
	if actor, ok := authctx.ActorFromContext(ctx); ok {
		actorKind, actorID, organizationID = store.AuditActorUser, actor.UserID, actor.OrganizationID
		if len(actor.Roles) > 0 {
			role = actor.Roles[0]
		}
	}
	result := s.audit.Emit(audit.NewEvent(actorKind, actorID, organizationID, role, "gc_cycle", key, "cleanup.gc", status, summary, nil))
	if !result.Accepted {
		s.logger.Warn("gc audit event rejected", "code", result.Code)
	}
}

func (s *CleanupService) emitBundleAudit(ctx context.Context, bundleID, status, summary string, metadata map[string]string) {
	if s.audit == nil {
		return
	}
	actorKind, actorID, organizationID, role := store.AuditActorSystem, "cleanup", "system", "system"
	if actor, ok := authctx.ActorFromContext(ctx); ok {
		actorKind, actorID, organizationID = store.AuditActorUser, actor.UserID, actor.OrganizationID
		if len(actor.Roles) > 0 {
			role = actor.Roles[0]
		}
	}
	result := s.audit.Emit(audit.NewEvent(actorKind, actorID, organizationID, role, "bundle", bundleID, "bundle.unarchived", status, summary, metadata))
	if !result.Accepted {
		s.logger.Warn("bundle audit event rejected", "action", "bundle.unarchived", "resource_id", bundleID, "code", result.Code)
	}
}
