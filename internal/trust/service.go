// Package trust implements artifact trust verification and trust root lifecycle management.
package trust

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// TrustService implements the TrustServiceHandler Connect interface.
//
//nolint:revive // Name matches Connect convention TrustServiceHandler.
type TrustService struct {
	store  store.TrustRootStore
	audit  audit.Sink
	logger *slog.Logger
}

// NewTrustService creates a new trust management Connect handler.
func NewTrustService(st store.TrustRootStore, emitter audit.Sink, logger *slog.Logger) *TrustService {
	if logger == nil {
		logger = slog.Default()
	}
	return &TrustService{store: st, audit: emitter, logger: logger}
}

// CreateTrustRoot creates a new trust root, activates it, and bumps the policy version.
func (s *TrustService) CreateTrustRoot(
	ctx context.Context,
	req *connect.Request[trustv1.CreateTrustRootRequest],
) (*connect.Response[trustv1.CreateTrustRootResponse], error) {
	msg := req.Msg
	now := time.Now().UTC()

	root := toDomainRoot(msg, now)
	if err := root.Validate(now); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	root.State = RootActive
	root.ID = uuid.New().String()

	// Create root and bump policy in sequence.
	if err := s.store.Create(ctx, toStoreRoot(root)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create trust root: %w", err))
	}

	ver, epoch, err := s.store.BumpPolicy(ctx, root.Environment)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bump policy: %w", err))
	}

	policy, err := s.buildPolicy(ctx, root.Environment, ver, epoch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build policy: %w", err))
	}

	s.emitTrustAudit(ctx, msg.GetOperator(), "create_root", root.ID, root.Issuer, root.Environment)

	return connect.NewResponse(&trustv1.CreateTrustRootResponse{
		Policy: policy,
		Root:   toProtoRoot(root),
	}), nil
}

// RotateTrustRoot moves an existing root to grace, creates a new active root, bumps the policy.
func (s *TrustService) RotateTrustRoot(
	ctx context.Context,
	req *connect.Request[trustv1.RotateTrustRootRequest],
) (*connect.Response[trustv1.RotateTrustRootResponse], error) {
	msg := req.Msg
	now := time.Now().UTC()

	// Validate old root exists and is active.
	old, err := s.store.Get(ctx, msg.GetOldRootId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("old root not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get old root: %w", err))
	}
	if old.State != store.TrustRootActive {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("old root %q is %s, must be active to rotate", old.ID, old.State))
	}

	newRoot := toDomainRootFromRotate(msg, now)
	if err := newRoot.Validate(now); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if msg.GetGraceUntil() != nil {
		newRoot.State = RootGrace
		t := msg.GetGraceUntil().AsTime()
		newRoot.GraceUntil = &t
	} else {
		newRoot.State = RootActive
	}

	// Check overlap: ensure new root's issuer doesn't conflict with any existing active/grace roots for the same env.
	if err := s.checkOverlap(ctx, newRoot.Environment, newRoot.Issuer, newRoot.KeyID, newRoot.ID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	newRoot.ID = uuid.New().String()

	// Transition old root to grace.
	old.State = store.TrustRootGrace
	gc := now
	old.GraceUntil = &gc
	if msg.GetGraceUntil() != nil {
		t := msg.GetGraceUntil().AsTime()
		old.GraceUntil = &t
	}
	old.UpdatedAt = now
	if err := s.store.Update(ctx, old); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("transition old root: %w", err))
	}

	if err := s.store.Create(ctx, toStoreRoot(newRoot)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create new root: %w", err))
	}

	ver, epoch, err := s.store.BumpPolicy(ctx, newRoot.Environment)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bump policy: %w", err))
	}

	policy, err := s.buildPolicy(ctx, newRoot.Environment, ver, epoch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build policy: %w", err))
	}

	s.emitTrustAudit(ctx, msg.GetOperator(), "rotate_root", newRoot.ID, newRoot.Issuer, newRoot.Environment)

	return connect.NewResponse(&trustv1.RotateTrustRootResponse{
		Policy:  policy,
		OldRoot: toProtoRoot(fromStoreRoot(old)),
		NewRoot: toProtoRoot(newRoot),
	}), nil
}

// EndGrace transitions a root from grace to retired.
func (s *TrustService) EndGrace(
	ctx context.Context,
	req *connect.Request[trustv1.EndGraceRequest],
) (*connect.Response[trustv1.EndGraceResponse], error) {
	msg := req.Msg
	now := time.Now().UTC()

	root, err := s.store.Get(ctx, msg.GetRootId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("root not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get root: %w", err))
	}
	if root.State != store.TrustRootGrace {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("root %q is %s, must be grace to end grace", root.ID, root.State))
	}

	// Check last active guard.
	if err := s.ensureNotLastActive(ctx, root.Environment, root.ID); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	root.State = store.TrustRootRetired
	root.UpdatedAt = now
	if err := s.store.Update(ctx, root); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update root: %w", err))
	}

	ver, epoch, err := s.store.BumpPolicy(ctx, root.Environment)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bump policy: %w", err))
	}

	policy, err := s.buildPolicy(ctx, root.Environment, ver, epoch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build policy: %w", err))
	}

	s.emitTrustAudit(ctx, msg.GetOperator(), "end_grace", root.ID, root.Issuer, root.Environment)

	return connect.NewResponse(&trustv1.EndGraceResponse{
		Policy: policy,
		Root:   toProtoRoot(fromStoreRoot(root)),
	}), nil
}

// RetireTrustRoot retires an active or grace root.
func (s *TrustService) RetireTrustRoot(
	ctx context.Context,
	req *connect.Request[trustv1.RetireTrustRootRequest],
) (*connect.Response[trustv1.RetireTrustRootResponse], error) {
	msg := req.Msg
	now := time.Now().UTC()

	root, err := s.store.Get(ctx, msg.GetRootId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("root not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get root: %w", err))
	}
	if root.State != store.TrustRootActive && root.State != store.TrustRootGrace {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("root %q is %s, must be active or grace to retire", root.ID, root.State))
	}

	if err := s.ensureNotLastActive(ctx, root.Environment, root.ID); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	root.State = store.TrustRootRetired
	root.UpdatedAt = now
	if err := s.store.Update(ctx, root); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update root: %w", err))
	}

	ver, epoch, err := s.store.BumpPolicy(ctx, root.Environment)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bump policy: %w", err))
	}

	policy, err := s.buildPolicy(ctx, root.Environment, ver, epoch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build policy: %w", err))
	}

	s.emitTrustAudit(ctx, msg.GetOperator(), "retire_root", root.ID, root.Issuer, root.Environment)

	return connect.NewResponse(&trustv1.RetireTrustRootResponse{
		Policy: policy,
		Root:   toProtoRoot(fromStoreRoot(root)),
	}), nil
}

// RevokeTrustRoot immediately revokes a root and bumps the revocation epoch.
func (s *TrustService) RevokeTrustRoot(
	ctx context.Context,
	req *connect.Request[trustv1.RevokeTrustRootRequest],
) (*connect.Response[trustv1.RevokeTrustRootResponse], error) {
	msg := req.Msg
	now := time.Now().UTC()

	root, err := s.store.Get(ctx, msg.GetRootId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("root not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get root: %w", err))
	}
	if root.State == store.TrustRootRevoked {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("root %q is already revoked", root.ID))
	}

	if err := s.ensureNotLastActive(ctx, root.Environment, root.ID); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	root.State = store.TrustRootRevoked
	root.RevokedAt = &now
	root.UpdatedAt = now
	if err := s.store.Update(ctx, root); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update root: %w", err))
	}

	_, err = s.store.BumpRevocationEpoch(ctx, root.Environment)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bump revocation epoch: %w", err))
	}

	policyMeta, err := s.store.GetPolicy(ctx, root.Environment)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get policy: %w", err))
	}
	policy, err := s.buildPolicy(ctx, root.Environment, policyMeta.Version, policyMeta.RevocationEpoch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build policy: %w", err))
	}

	s.emitTrustAudit(ctx, msg.GetOperator(), "revoke_root", root.ID, root.Issuer, root.Environment)

	return connect.NewResponse(&trustv1.RevokeTrustRootResponse{
		Policy: policy,
		Root:   toProtoRoot(fromStoreRoot(root)),
	}), nil
}

// GetTrustPolicy returns the current trust policy with all roots for an environment.
func (s *TrustService) GetTrustPolicy(
	ctx context.Context,
	req *connect.Request[trustv1.GetTrustPolicyRequest],
) (*connect.Response[trustv1.GetTrustPolicyResponse], error) {
	env := req.Msg.GetEnvironment()
	meta, err := s.store.GetPolicy(ctx, env)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get policy: %w", err))
	}
	policy, err := s.buildPolicy(ctx, env, meta.Version, meta.RevocationEpoch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build policy: %w", err))
	}
	return connect.NewResponse(&trustv1.GetTrustPolicyResponse{
		Policy: policy,
	}), nil
}

func (s *TrustService) buildPolicy(ctx context.Context, env string, version, epoch int64) (*trustv1.TrustPolicy, error) {
	roots, err := s.store.ListByEnvironment(ctx, env)
	if err != nil {
		return nil, err
	}
	protoRoots := make([]*trustv1.TrustRoot, 0, len(roots))
	for _, r := range roots {
		protoRoots = append(protoRoots, toProtoRoot(fromStoreRoot(r)))
	}
	return &trustv1.TrustPolicy{
		Environment:     env,
		Version:         version,
		RevocationEpoch: epoch,
		Roots:           protoRoots,
	}, nil
}

// ensureNotLastActive rejects the operation if the root being removed is the last active/grace root.
func (s *TrustService) ensureNotLastActive(ctx context.Context, env, excludeID string) error {
	active, err := s.store.GetActiveByEnvironment(ctx, env, time.Now())
	if err != nil {
		return fmt.Errorf("get active roots: %w", err)
	}
	remaining := 0
	for _, r := range active {
		if r.ID != excludeID {
			remaining++
		}
	}
	if remaining == 0 {
		return ErrLastRootRemovalForbidden
	}
	return nil
}

// checkOverlap rejects a new root if an existing active/grace root for the same
// environment already uses the same issuer or key_id (but not the same root ID).
func (s *TrustService) checkOverlap(ctx context.Context, env, issuer, keyID, selfID string) error {
	roots, err := s.store.ListByEnvironment(ctx, env)
	if err != nil {
		return fmt.Errorf("list roots: %w", err)
	}
	for _, r := range roots {
		if r.ID == selfID {
			continue
		}
		if r.State != store.TrustRootActive && r.State != store.TrustRootGrace {
			continue
		}
		if r.KeyID == keyID {
			return fmt.Errorf("%w: key_id %q already in use by root %q", ErrOverlapConflict, keyID, r.ID)
		}
		if r.Issuer == issuer {
			return fmt.Errorf("%w: issuer %q already in use by root %q", ErrOverlapConflict, issuer, r.ID)
		}
	}
	return nil
}

// --- Conversion helpers ---

func toDomainRoot(msg *trustv1.CreateTrustRootRequest, now time.Time) *Root {
	r := &Root{
		Environment:    msg.GetEnvironment(),
		KeyID:          msg.GetKeyId(),
		PublicKeyPEM:   msg.GetPublicKeyPem(),
		Issuer:         msg.GetIssuer(),
		SubjectPattern: msg.GetSubjectPattern(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if msg.GetValidFrom() != nil {
		r.ValidFrom = msg.GetValidFrom().AsTime()
	} else {
		r.ValidFrom = now
	}
	return r
}

func toDomainRootFromRotate(msg *trustv1.RotateTrustRootRequest, now time.Time) *Root {
	r := &Root{
		Environment:    msg.GetEnvironment(),
		KeyID:          msg.GetKeyId(),
		PublicKeyPEM:   msg.GetPublicKeyPem(),
		Issuer:         msg.GetIssuer(),
		SubjectPattern: msg.GetSubjectPattern(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if msg.GetValidFrom() != nil {
		r.ValidFrom = msg.GetValidFrom().AsTime()
	} else {
		r.ValidFrom = now
	}
	return r
}

func toProtoRoot(r *Root) *trustv1.TrustRoot {
	tr := &trustv1.TrustRoot{
		Id:             r.ID,
		Environment:    r.Environment,
		KeyId:          r.KeyID,
		PublicKeyPem:   r.PublicKeyPEM,
		Issuer:         r.Issuer,
		SubjectPattern: r.SubjectPattern,
		State:          rootStateToProto(r.State),
		ValidFrom:      timestamppb.New(r.ValidFrom),
		CreatedAt:      timestamppb.New(r.CreatedAt),
		UpdatedAt:      timestamppb.New(r.UpdatedAt),
	}
	if r.GraceUntil != nil {
		tr.GraceUntil = timestamppb.New(*r.GraceUntil)
	}
	if r.RevokedAt != nil {
		tr.RevokedAt = timestamppb.New(*r.RevokedAt)
	}
	return tr
}

func toStoreRoot(r *Root) *store.TrustRoot {
	return &store.TrustRoot{
		ID:             r.ID,
		Environment:    r.Environment,
		KeyID:          r.KeyID,
		PublicKeyPEM:   r.PublicKeyPEM,
		Issuer:         r.Issuer,
		SubjectPattern: r.SubjectPattern,
		State:          store.TrustRootState(r.State),
		ValidFrom:      r.ValidFrom,
		GraceUntil:     r.GraceUntil,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		RevokedAt:      r.RevokedAt,
	}
}

func fromStoreRoot(sr *store.TrustRoot) *Root {
	return &Root{
		ID:             sr.ID,
		Environment:    sr.Environment,
		KeyID:          sr.KeyID,
		PublicKeyPEM:   sr.PublicKeyPEM,
		Issuer:         sr.Issuer,
		SubjectPattern: sr.SubjectPattern,
		State:          RootState(sr.State),
		ValidFrom:      sr.ValidFrom,
		GraceUntil:     sr.GraceUntil,
		CreatedAt:      sr.CreatedAt,
		UpdatedAt:      sr.UpdatedAt,
		RevokedAt:      sr.RevokedAt,
	}
}

func rootStateToProto(s RootState) trustv1.TrustRootState {
	switch s {
	case RootPending:
		return trustv1.TrustRootState_TRUST_ROOT_STATE_PENDING
	case RootActive:
		return trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE
	case RootGrace:
		return trustv1.TrustRootState_TRUST_ROOT_STATE_GRACE
	case RootRetired:
		return trustv1.TrustRootState_TRUST_ROOT_STATE_RETIRED
	case RootRevoked:
		return trustv1.TrustRootState_TRUST_ROOT_STATE_REVOKED
	default:
		return trustv1.TrustRootState_TRUST_ROOT_STATE_UNSPECIFIED
	}
}

func (s *TrustService) emitTrustAudit(ctx context.Context, fallbackOperator, action, rootID, issuer, env string) {
	if s.audit == nil {
		return
	}
	actorKind := store.AuditActorUser
	actorID := fallbackOperator
	organizationID := ""
	role := "platform_admin"
	if actor, ok := authctx.ActorFromContext(ctx); ok {
		actorID = actor.UserID
		organizationID = actor.OrganizationID
		if actor.Service != "" {
			actorKind = store.AuditActorService
			actorID = actor.Service
		}
		if len(actor.Roles) > 0 {
			role = actor.Roles[0]
		}
	}
	ev := audit.NewEvent(
		actorKind,
		actorID,
		organizationID,
		role,
		"trust_root",
		rootID,
		action,
		"succeeded",
		fmt.Sprintf("trust_root %s succeeded issuer=%s env=%s", action, issuer, env),
		map[string]string{"environment": env, "issuer": issuer},
	)
	if !s.audit.Emit(ev).Accepted {
		s.logger.Warn("trust audit event rejected", "action", action, "root_id", rootID)
	}
}

// Compile-time check: TrustService implements the Connect handler interface.
var _ trustv1connect.TrustServiceHandler = (*TrustService)(nil)
