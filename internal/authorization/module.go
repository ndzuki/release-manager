package authorization

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

const snapshotDeadline = 200 * time.Millisecond

// Snapshot is the consumer-side authorization projection for one actor and scope.
type Snapshot struct {
	OrganizationID           string
	CustomerID               string
	ActorID                  string
	Role                     string
	CanExecuteEmergency      bool
	CanResolveEmergency      bool
	CanCreateValuesRevision  bool
	CanApproveValuesRevision bool
	SourceVersion            uint64
	PolicyVersion            uint64
	Checkpoint               uint64
	Fresh                    bool
}

// Authorizer is the governance-write authorization seam consumed by Orchestrator.
type Authorizer interface {
	AuthorizeWrite(ctx context.Context, actor authctx.Actor, customerID string, action store.AuthorizationAction) error
	Snapshot(ctx context.Context, organizationID, customerID string) (*Snapshot, error)
}

type cacheKey struct {
	actorID        string
	organizationID string
	customerID     string
}

type cacheEntry struct {
	actor authctx.Actor
	// credential is the verified bearer or browser-cookie token of the last
	// request seen for this actor+scope. It is what the background refresh
	// forwards; see ADR-028 clause 5.
	credential  string
	snapshot    Snapshot
	initialized bool
	backoff     time.Duration
	nextPull    time.Time
	pullMu      sync.Mutex
}

// Module maintains active actor snapshots and persisted scope checkpoints.
type Module struct {
	client      authv1connect.AuthorizationServiceClient
	checkpoints store.AuthorizationStore
	metrics     *Metrics
	logger      *slog.Logger
	interval    time.Duration
	backoffMax  time.Duration

	mu      sync.RWMutex
	entries map[cacheKey]*cacheEntry
	latest  map[string]Snapshot
}

// NewModule constructs the Authorization Snapshot consumer.
func NewModule(
	client authv1connect.AuthorizationServiceClient,
	checkpoints store.AuthorizationStore,
	metrics *Metrics,
	logger *slog.Logger,
	interval time.Duration,
	backoffMax time.Duration,
) *Module {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Second
	}
	if backoffMax <= 0 {
		backoffMax = 30 * time.Second
	}
	return &Module{
		client: client, checkpoints: checkpoints, metrics: metrics, logger: logger,
		interval: interval, backoffMax: backoffMax,
		entries: make(map[cacheKey]*cacheEntry), latest: make(map[string]Snapshot),
	}
}

// Run refreshes snapshots for active actors with bounded exponential backoff.
func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.pullDue(ctx, now)
		}
	}
}

func (m *Module) pullDue(ctx context.Context, now time.Time) {
	m.mu.RLock()
	keys := make([]cacheKey, 0, len(m.entries))
	for key, entry := range m.entries {
		if entry.nextPull.IsZero() || !now.Before(entry.nextPull) {
			keys = append(keys, key)
		}
	}
	m.mu.RUnlock()
	for _, key := range keys {
		if err := m.pull(ctx, key); err != nil {
			m.logger.Warn("authorization snapshot pull failed", "organization_id", key.organizationID,
				"customer_id", key.customerID, "reason", reasonCode(err))
		}
	}
}

// AuthorizeWrite fails closed on missing, stale, regressed, or unavailable snapshots.
func (m *Module) AuthorizeWrite(ctx context.Context, actor authctx.Actor, customerID string, action store.AuthorizationAction) error {
	started := time.Now()
	defer func() {
		if m.metrics != nil {
			m.metrics.EnforceDuration.Observe(time.Since(started).Seconds())
		}
	}()
	if !action.Valid() || uuid.Validate(actor.OrganizationID) != nil || uuid.Validate(customerID) != nil {
		return invalidActorError("actor organization, customer, and action are required")
	}
	actorID := authorizationSubject(actor)
	actorType := "human"
	if actor.Service != "" {
		actorType = "service"
	}
	if actorID == "" {
		return invalidActorError("actor identity is required")
	}
	key := cacheKey{actorID: actorID, organizationID: actor.OrganizationID, customerID: customerID}
	// ADR-028 clause 5: the background snapshot refresh needs a credential that
	// was verified for THIS request. The raw inbound Authorization header is
	// empty for a cookie-authenticated console, which left the cached entry
	// without any credential and made every browser write fail closed (B4).
	credential := authctx.VerifiedCredentialFromContext(ctx)
	if credential == "" {
		credential = bearerCredential(authctx.AuthorizationHeaderFromContext(ctx))
	}

	m.mu.Lock()
	entry, exists := m.entries[key]
	if !exists {
		entry = &cacheEntry{actor: actor, credential: credential}
		m.entries[key] = entry
	} else if credential != "" {
		entry.credential = credential
	}
	m.mu.Unlock()

	if err := m.pull(ctx, key); err != nil {
		cause, _ := staleCauseOf(err)
		m.observeStaleCause(cause)
		m.logger.Warn("authorization snapshot pull failed during authorize",
			"organization_id", actor.OrganizationID, "customer_id", customerID,
			"action", action, "reason", reasonCode(err), "stale_cause", string(cause), "error", err)
		m.recordDecision(actorID, actorType, actor.OrganizationID, customerID, action, "deny", reasonCode(err), Snapshot{})
		// Return a stable, non-leaking error. The raw pull failure can carry
		// credential text — the browser used to read
		// "invalid token: parse access token: token is expired" while its own
		// session was healthy. The cause stays in the WARN above and in the
		// decision record; only the bounded enum is exposed on the wire.
		stale := staleError(0, 0, 0)
		setStaleCauseMeta(stale, cause)
		return stale
	}

	m.mu.RLock()
	entry = m.entries[key]
	snapshot := entry.snapshot
	initialized := entry.initialized
	m.mu.RUnlock()
	if !initialized || !snapshot.Fresh || snapshot.Checkpoint < snapshot.SourceVersion {
		cause := StaleCauseNotFresh
		m.observeStaleCause(cause)
		m.logger.Warn("authorization snapshot not fresh during authorize",
			"organization_id", actor.OrganizationID, "customer_id", customerID, "action", action,
			"stale_cause", string(cause), "source_version", snapshot.SourceVersion,
			"checkpoint", snapshot.Checkpoint, "initialized", initialized, "fresh", snapshot.Fresh)
		err := staleError(snapshot.SourceVersion, snapshot.Checkpoint, snapshot.PolicyVersion)
		setStaleCauseMeta(err, cause)
		m.recordDecision(actorID, actorType, actor.OrganizationID, customerID, action, "deny", reasonCode(err), snapshot)
		return err
	}
	if !capabilityAllowed(snapshot, action) {
		err := permissionDeniedError(snapshot.SourceVersion, snapshot.Checkpoint, snapshot.PolicyVersion)
		m.recordDecision(actorID, actorType, actor.OrganizationID, customerID, action, "deny", reasonCode(err), snapshot)
		return err
	}
	recordFence(ctx, snapshot.SourceVersion)
	m.recordDecision(actorID, actorType, actor.OrganizationID, customerID, action, "allow", "ALLOW", snapshot)
	return nil
}

//nolint:gocyclo // Checkpoint gap/regression/timeout gates are explicit per REQ-027 freshness contract.
func (m *Module) pull(ctx context.Context, key cacheKey) error {
	m.mu.RLock()
	entry := m.entries[key]
	m.mu.RUnlock()
	if entry == nil {
		return withStaleCause(staleError(0, 0, 0), StaleCauseNotFresh)
	}
	entry.pullMu.Lock()
	defer entry.pullMu.Unlock()
	m.mu.RLock()
	credential := entry.credential
	m.mu.RUnlock()
	if m.client == nil {
		return withStaleCause(staleError(0, 0, 0), StaleCauseSnapshotRPCError)
	}

	request := connect.NewRequest(&authv1.GetAuthorizationSnapshotRequest{
		OrganizationId: key.organizationID,
		CustomerId:     key.customerID,
	})
	if credential != "" {
		request.Header().Set("Authorization", "Bearer "+credential)
	}
	callCtx, cancel := context.WithTimeout(ctx, snapshotDeadline)
	defer cancel()
	started := time.Now()
	response, err := m.client.GetAuthorizationSnapshot(callCtx, request)
	if m.metrics != nil {
		m.metrics.SnapshotRPCDuration.Observe(time.Since(started).Seconds())
	}
	if err != nil {
		m.markPullFailure(key)
		return withStaleCause(translateSnapshotError(err), snapshotRPCCause(err))
	}

	msg := response.Msg
	remote := Snapshot{
		OrganizationID:           msg.GetOrganizationId(),
		CustomerID:               msg.GetCustomerId(),
		ActorID:                  msg.GetActorId(),
		Role:                     msg.GetRole(),
		CanExecuteEmergency:      msg.GetCanExecuteEmergency(),
		CanResolveEmergency:      msg.GetCanResolveEmergency(),
		CanCreateValuesRevision:  msg.GetCanCreateValuesRevision(),
		CanApproveValuesRevision: msg.GetCanApproveValuesRevision(),
		SourceVersion:            msg.GetSourceVersion(),
		PolicyVersion:            msg.GetPolicyVersion(),
		Checkpoint:               msg.GetCheckpoint(),
		Fresh:                    msg.GetFresh(),
	}
	if remote.OrganizationID != key.organizationID || remote.CustomerID != key.customerID || remote.ActorID != key.actorID {
		return withStaleCause(staleError(remote.SourceVersion, 0, remote.PolicyVersion), StaleCauseScopeMismatch)
	}

	previous := uint64(0)
	previousPolicy := uint64(0)
	if m.checkpoints != nil {
		checkpoint, checkpointErr := m.checkpoints.GetCheckpoint(ctx, key.organizationID, key.customerID)
		if checkpointErr != nil && !errors.Is(checkpointErr, store.ErrNotFound) {
			return withStaleCause(staleError(remote.SourceVersion, 0, remote.PolicyVersion), StaleCauseCheckpointRead)
		}
		if checkpointErr == nil {
			previous = checkpoint.SourceVersion
			previousPolicy = checkpoint.PolicyVersion
			if remote.SourceVersion < previous || remote.Checkpoint < previous || remote.PolicyVersion < previousPolicy {
				return withStaleCause(staleError(remote.SourceVersion, previous, remote.PolicyVersion), StaleCauseRegression)
			}
		}
	}

	changed := previous != remote.Checkpoint || previousPolicy != remote.PolicyVersion
	gap := previous > 0 && remote.Checkpoint > previous+1
	checkpointFresh := remote.SourceVersion > 0 && remote.Fresh && remote.Checkpoint >= remote.SourceVersion && !gap
	if m.checkpoints != nil {
		if err := m.checkpoints.SaveCheckpoint(ctx, store.AuthorizationCheckpoint{
			OrganizationID: key.organizationID,
			CustomerID:     key.customerID,
			SourceVersion:  remote.Checkpoint,
			PolicyVersion:  remote.PolicyVersion,
			Fresh:          checkpointFresh,
		}); err != nil {
			return withStaleCause(staleError(remote.SourceVersion, previous, remote.PolicyVersion), StaleCauseCheckpointWrite)
		}
	}
	remote.Fresh = checkpointFresh

	m.mu.Lock()
	entry = m.entries[key]
	entry.snapshot = remote
	entry.initialized = true
	entry.backoff = 0
	entry.nextPull = time.Now().Add(m.interval)
	m.latest[scopeKey(key.organizationID, key.customerID)] = remote
	m.mu.Unlock()
	if m.metrics != nil {
		m.metrics.SourceVersion.Set(float64(remote.SourceVersion))
		m.metrics.CheckpointVersion.Set(float64(remote.Checkpoint))
		if remote.Fresh {
			m.metrics.PolicyHealth.Set(1)
		} else {
			m.metrics.PolicyHealth.Set(0)
		}
	}
	if changed || gap || !remote.Fresh {
		// gap is the most specific signal; a plain `changed` on a version the module has
		// never persisted is the healthy warm-up.
		cause := StaleCauseNotFresh
		if gap {
			cause = StaleCauseGap
		} else if changed {
			cause = StaleCauseWarmup
		}
		return withStaleCause(staleError(remote.SourceVersion, remote.Checkpoint, remote.PolicyVersion), cause)
	}
	return nil
}

func (m *Module) markPullFailure(key cacheKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[key]
	if entry == nil {
		return
	}
	if entry.backoff == 0 {
		entry.backoff = m.interval
	} else {
		entry.backoff *= 2
		if entry.backoff > m.backoffMax {
			entry.backoff = m.backoffMax
		}
	}
	entry.nextPull = time.Now().Add(entry.backoff)
}

// Snapshot returns the latest projection observed for a scope.
func (m *Module) Snapshot(_ context.Context, organizationID, customerID string) (*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot, ok := m.latest[scopeKey(organizationID, customerID)]
	if !ok {
		return nil, store.ErrNotFound
	}
	latest := snapshot
	return &latest, nil
}

func capabilityAllowed(snapshot Snapshot, action store.AuthorizationAction) bool {
	switch action {
	case store.AuthorizationCreateOperation:
		return snapshot.Role == string(store.RoleReleaseAdmin) || snapshot.Role == string(store.RolePlatformAdmin)
	case store.AuthorizationExecuteEmergency:
		return snapshot.CanExecuteEmergency
	case store.AuthorizationResolveEmergency:
		return snapshot.CanResolveEmergency
	case store.AuthorizationCreateValues:
		return snapshot.CanCreateValuesRevision
	case store.AuthorizationApproveValues:
		return snapshot.CanApproveValuesRevision
	default:
		return false
	}
}

func (m *Module) recordDecision(actorID, actorType, organizationID, customerID string, action store.AuthorizationAction, result, reason string, snapshot Snapshot) {
	if m.metrics != nil {
		m.metrics.Decisions.WithLabelValues(result, actorType).Inc()
		if reason == "AUTHORIZATION_SNAPSHOT_STALE" {
			m.metrics.SnapshotStale.Inc()
		}
	}
	m.logger.Info("authorization decision", "actor_id", actorID, "actor_type", actorType,
		"domain", organizationID+":"+customerID, "action", action, "result", result, "reason", reason,
		"source_version", snapshot.SourceVersion, "policy_version", snapshot.PolicyVersion, "checkpoint", snapshot.Checkpoint)
}

func scopeKey(organizationID, customerID string) string { return organizationID + ":" + customerID }

func authorizationSubject(actor authctx.Actor) string {
	if actor.Service != "" {
		return "service:" + actor.Service
	}
	return actor.UserID
}

// snapshotRPCCause classifies a failed snapshot RPC without ever looking at its message.
func snapshotRPCCause(err error) StaleCause {
	if errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return StaleCauseSnapshotRPCTimeout
	}
	return StaleCauseSnapshotRPCError
}

// observeStaleCause increments the bounded cause counter. The legacy
// auth_snapshot_stale_total keeps incrementing from recordDecision, so this only adds a
// breakdown (ADR-016 fixed metric stays untouched).
func (m *Module) observeStaleCause(cause StaleCause) {
	if m.metrics == nil {
		return
	}
	if !cause.Valid() {
		cause = StaleCauseUnknown
	}
	m.metrics.SnapshotStaleCause.WithLabelValues(string(cause)).Inc()
}

// setStaleCauseMeta exposes the bounded cause to callers that already read the other
// X-* metadata. It never replaces X-Reason-Code.
func setStaleCauseMeta(err error, cause StaleCause) {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return
	}
	if !cause.Valid() {
		cause = StaleCauseUnknown
	}
	connectErr.Meta().Set("X-Stale-Cause", string(cause))
}

func staleError(source, checkpoint, policy uint64) error {
	err := connect.NewError(connect.CodeUnavailable, errors.New("authorization snapshot stale"))
	err.Meta().Set("X-Reason-Code", "AUTHORIZATION_SNAPSHOT_STALE")
	err.Meta().Set("X-Source-Version", strconv.FormatUint(source, 10))
	err.Meta().Set("X-Checkpoint", strconv.FormatUint(checkpoint, 10))
	err.Meta().Set("X-Checkpoint-Fresh", "false")
	err.Meta().Set("X-Policy-Version", strconv.FormatUint(policy, 10))
	return err
}

func permissionDeniedError(source, checkpoint, policy uint64) error {
	err := connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	err.Meta().Set("X-Reason-Code", "PERMISSION_DENIED")
	err.Meta().Set("X-Source-Version", strconv.FormatUint(source, 10))
	err.Meta().Set("X-Checkpoint", strconv.FormatUint(checkpoint, 10))
	err.Meta().Set("X-Policy-Version", strconv.FormatUint(policy, 10))
	return err
}

func invalidActorError(message string) error {
	err := connect.NewError(connect.CodeUnauthenticated, errors.New(message))
	err.Meta().Set("X-Reason-Code", "INVALID_ACTOR_CONTEXT")
	return err
}

func translateSnapshotError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return staleError(0, 0, 0)
	}
	if connect.CodeOf(err) == connect.CodeUnavailable {
		return staleError(0, 0, 0)
	}
	return err
}

func reasonCode(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if reason := connectErr.Meta().Get("X-Reason-Code"); reason != "" {
			return reason
		}
	}
	return connect.CodeOf(err).String()
}

// bearerCredential extracts the token from a "Bearer <token>" header. It returns
// "" for an absent header — the shape a browser session sends — so callers can
// treat "no header" and "no credential" the same way.
func bearerCredential(authorizationHeader string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorizationHeader, prefix) {
		return ""
	}
	return strings.TrimPrefix(authorizationHeader, prefix)
}

var _ Authorizer = (*Module)(nil)
