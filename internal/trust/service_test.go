package trust

import (
	"strings"
	"sync"
	"testing"
	"time"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type recordingTrustSink struct{ events []*store.AuditEvent }

func (s *recordingTrustSink) Emit(event *store.AuditEvent) audit.Result {
	s.events = append(s.events, event)
	return audit.Result{EventID: event.ID, Accepted: true}
}

func newTrustServiceTest(t *testing.T, sink audit.Sink) *TrustService {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	return NewTrustService(st.TrustRoots(), sink, logger())
}

func createTrustRoot(t *testing.T, svc *TrustService, keyID, issuer string) *trustv1.TrustRoot {
	t.Helper()
	publicKeyPEM, _ := generateEd25519KeyPair(t)
	response, err := svc.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: keyID, PublicKeyPem: publicKeyPEM, Issuer: issuer, Operator: "operator-1",
	}))
	require.NoError(t, err)
	return response.Msg.GetRoot()
}

func TestTrustService_CreateUsesConnectRequestAndBumpsPolicy(t *testing.T) {
	sink := &recordingTrustSink{}
	svc := newTrustServiceTest(t, sink)
	publicKeyPEM, _ := generateEd25519KeyPair(t)

	response, err := svc.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "production", KeyId: "key-1", PublicKeyPem: publicKeyPEM, Issuer: "issuer-1", Operator: "operator-1",
	}))
	require.NoError(t, err)
	require.NotNil(t, response.Msg.GetPolicy())
	assert.Equal(t, int64(1), response.Msg.GetPolicy().GetVersion())
	assert.Zero(t, response.Msg.GetPolicy().GetRevocationEpoch())
	assert.Equal(t, trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE, response.Msg.GetRoot().GetState())
	assert.Len(t, sink.events, 1)
	assert.Equal(t, "create_root", sink.events[0].Action)
}

func TestTrustService_NilAuditSinkIsSafe(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	assert.NotEmpty(t, createTrustRoot(t, svc, "key-1", "issuer-1").GetId())
}

func TestTrustService_RotateOverlapConflictMapsToInvalidArgument(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	old := createTrustRoot(t, svc, "key-old", "issuer-old")
	createTrustRoot(t, svc, "key-other", "issuer-other")
	publicKeyPEM, _ := generateEd25519KeyPair(t)
	_, err := svc.RotateTrustRoot(t.Context(), connect.NewRequest(&trustv1.RotateTrustRootRequest{
		Environment: "staging", OldRootId: old.GetId(), KeyId: "key-new", PublicKeyPem: publicKeyPEM, Issuer: "issuer-other",
		GraceUntil: timestamppb.New(time.Now().UTC().Add(time.Hour)),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.ErrorContains(t, err, ErrOverlapConflict.Error())
}

func TestTrustService_ErrorMappings(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	publicKeyPEM, _ := generateEd25519KeyPair(t)
	_, err := svc.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-invalid", PublicKeyPem: "not-a-key", Issuer: "issuer-invalid",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.ErrorIs(t, err, ErrInvalidRoot)

	// invalid_root required fields (REQ-043 错误模型: environment/key_id/issuer 必填)。
	// time-window branch (grace_until must be after valid_from) is unreachable at the
	// service seam: CreateTrustRootRequest carries no grace_until, and Rotate assigns
	// grace_until directly to the old root without Validate.
	for _, tc := range []struct{ name, keyID, issuer string }{
		{name: "missing key id", keyID: "", issuer: "issuer-ok"},
		{name: "missing issuer", keyID: "key-ok", issuer: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
				Environment: "staging", KeyId: tc.keyID, PublicKeyPem: publicKeyPEM, Issuer: tc.issuer,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.ErrorIs(t, err, ErrInvalidRoot)
		})
	}

	calls := map[string]func() error{
		"end grace": func() error {
			_, e := svc.EndGrace(t.Context(), connect.NewRequest(&trustv1.EndGraceRequest{Environment: "staging", RootId: "missing"}))
			return e
		},
		"retire": func() error {
			_, e := svc.RetireTrustRoot(t.Context(), connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: "missing"}))
			return e
		},
		"revoke": func() error {
			_, e := svc.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{Environment: "staging", RootId: "missing"}))
			return e
		},
		"rotate": func() error {
			_, e := svc.RotateTrustRoot(t.Context(), connect.NewRequest(&trustv1.RotateTrustRootRequest{Environment: "staging", OldRootId: "missing", KeyId: "key-new", PublicKeyPem: publicKeyPEM, Issuer: "issuer-new"}))
			return e
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			assert.ErrorContains(t, err, "root not found")
		})
	}
}

func TestTrustService_RevokeAlreadyRevokedRootForbidden(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	first := createTrustRoot(t, svc, "key-1", "issuer-1")
	createTrustRoot(t, svc, "key-2", "issuer-2")

	_, err := svc.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{
		Environment: "staging", RootId: first.GetId(),
	}))
	require.NoError(t, err)

	_, err = svc.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{
		Environment: "staging", RootId: first.GetId(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorIs(t, err, ErrRevokedRoot)
}

func TestTrustService_AuditOperatorFallback(t *testing.T) {
	sink := &recordingTrustSink{}
	svc := newTrustServiceTest(t, sink)
	createTrustRoot(t, svc, "key-1", "issuer-1") // helper passes Operator: "operator-1"
	require.Len(t, sink.events, 1)
	// No actor in context → fallback to request Operator (REQ-043 审计: operator 回退)。
	assert.Equal(t, "operator-1", sink.events[0].ActorID)
	// REQ-043 输出契约: actor/organization/role 来自服务端身份上下文（无认证上下文时 role 默认 platform_admin）。
	assert.Equal(t, "platform_admin", sink.events[0].Role)
	assert.Empty(t, sink.events[0].OrganizationID)
}

func TestTrustService_LastActiveOrGraceRootForbidden(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*TrustService, string) error
	}{
		{name: "retire", call: func(svc *TrustService, id string) error {
			_, err := svc.RetireTrustRoot(t.Context(), connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: id}))
			return err
		}},
		{name: "revoke", call: func(svc *TrustService, id string) error {
			_, err := svc.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{Environment: "staging", RootId: id}))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTrustServiceTest(t, nil)
			root := createTrustRoot(t, svc, "key-1", "issuer-1")
			err := tc.call(svc, root.GetId())
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.ErrorContains(t, err, ErrLastRootRemovalForbidden.Error())
		})
	}
}

func TestTrustService_EndGraceLastGraceRootForbidden(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	old := createTrustRoot(t, svc, "key-old", "issuer-old")
	newKey, _ := generateEd25519KeyPair(t)
	rotated, err := svc.RotateTrustRoot(t.Context(), connect.NewRequest(&trustv1.RotateTrustRootRequest{
		Environment: "staging", OldRootId: old.GetId(), KeyId: "key-new", PublicKeyPem: newKey, Issuer: "issuer-new", GraceUntil: timestamppb.New(time.Now().UTC().Add(time.Hour)),
	}))
	require.NoError(t, err)
	_, err = svc.RetireTrustRoot(t.Context(), connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: rotated.Msg.GetNewRoot().GetId()}))
	require.NoError(t, err)
	_, err = svc.EndGrace(t.Context(), connect.NewRequest(&trustv1.EndGraceRequest{Environment: "staging", RootId: old.GetId()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, ErrLastRootRemovalForbidden.Error())
}

func TestTrustService_LastRootGuardAllowsSecondRoot(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	first := createTrustRoot(t, svc, "key-1", "issuer-1")
	second := createTrustRoot(t, svc, "key-2", "issuer-2")
	response, err := svc.RetireTrustRoot(t.Context(), connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: first.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, trustv1.TrustRootState_TRUST_ROOT_STATE_RETIRED, response.Msg.GetRoot().GetState())
	policy, err := svc.GetTrustPolicy(t.Context(), connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"}))
	require.NoError(t, err)
	secondProto := policyRoot(policy.Msg.GetPolicy(), second.GetId())
	require.NotNil(t, secondProto)
	assert.Equal(t, trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE, secondProto.GetState())
}

func TestTrustService_PolicyVersionAndEpochMonotonic(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	first := createTrustRoot(t, svc, "key-1", "issuer-1")
	newKey, _ := generateEd25519KeyPair(t)
	rotated, err := svc.RotateTrustRoot(t.Context(), connect.NewRequest(&trustv1.RotateTrustRootRequest{
		Environment: "staging", OldRootId: first.GetId(), KeyId: "key-2", PublicKeyPem: newKey, Issuer: "issuer-2", GraceUntil: timestamppb.New(time.Now().UTC().Add(time.Hour)),
	}))
	require.NoError(t, err)
	assert.Equal(t, int64(2), rotated.Msg.GetPolicy().GetVersion())
	assert.Zero(t, rotated.Msg.GetPolicy().GetRevocationEpoch())

	_, err = svc.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{Environment: "staging", RootId: first.GetId()}))
	require.NoError(t, err)
	policyResp, err := svc.GetTrustPolicy(t.Context(), connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"}))
	require.NoError(t, err)
	assert.Equal(t, int64(2), policyResp.Msg.GetPolicy().GetVersion())
	assert.Equal(t, int64(1), policyResp.Msg.GetPolicy().GetRevocationEpoch())
}

func TestTrustService_RetireAndEndGraceBumpPolicyVersion(t *testing.T) {
	svc := newTrustServiceTest(t, nil)
	first := createTrustRoot(t, svc, "key-1", "issuer-1")
	second := createTrustRoot(t, svc, "key-2", "issuer-2")
	third := createTrustRoot(t, svc, "key-3", "issuer-3")
	newPublicKey, _ := generateEd25519KeyPair(t)

	rotated, err := svc.RotateTrustRoot(t.Context(), connect.NewRequest(&trustv1.RotateTrustRootRequest{
		Environment: "staging", OldRootId: first.GetId(), KeyId: "key-4", PublicKeyPem: newPublicKey, Issuer: "issuer-4",
		GraceUntil: timestamppb.New(time.Now().UTC().Add(time.Hour)),
	}))
	require.NoError(t, err)
	assert.Equal(t, int64(4), rotated.Msg.GetPolicy().GetVersion())

	retired, err := svc.RetireTrustRoot(t.Context(), connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: second.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, int64(5), retired.Msg.GetPolicy().GetVersion())

	ended, err := svc.EndGrace(t.Context(), connect.NewRequest(&trustv1.EndGraceRequest{Environment: "staging", RootId: first.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, int64(6), ended.Msg.GetPolicy().GetVersion())

	policy, err := svc.GetTrustPolicy(t.Context(), connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"}))
	require.NoError(t, err)
	assert.Equal(t, trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE, policyRoot(policy.Msg.GetPolicy(), third.GetId()).GetState())
}

// policyRoot returns the trust root with the given id from a policy, or nil.
func policyRoot(policy *trustv1.TrustPolicy, id string) *trustv1.TrustRoot {
	for _, r := range policy.GetRoots() {
		if r.GetId() == id {
			return r
		}
	}
	return nil
}

func TestTrustService_AuditEventsExcludeKeyMaterial(t *testing.T) {
	sink := &recordingTrustSink{}
	svc := newTrustServiceTest(t, sink)
	publicKeyPEM, _ := generateEd25519KeyPair(t)
	privateMaterial := "PRIVATE KEY material must never appear"
	response, err := svc.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "production", KeyId: "key-sensitive", PublicKeyPem: publicKeyPEM, Issuer: "issuer-sensitive", Operator: privateMaterial,
	}))
	require.NoError(t, err)
	require.Len(t, sink.events, 1)
	event := sink.events[0]
	assert.Equal(t, response.Msg.GetRoot().GetId(), event.ResourceID)
	assert.Equal(t, "trust_root", event.ResourceType)
	assert.Equal(t, "create_root", event.Action)
	assert.NotContains(t, event.ChangeSummary, privateMaterial)
	assert.NotContains(t, event.ChangeSummary, publicKeyPEM)
	for key, value := range event.Metadata {
		assert.NotContains(t, strings.ToLower(key), "private")
		assert.NotContains(t, strings.ToLower(value), "private key")
		assert.NotContains(t, value, publicKeyPEM)
	}
}

// TestTrustService_ConcurrentRemovalLeavesAtLeastOneLiveRoot locks the AC-043-03
// invariant under concurrency: two racing removals must never leave the
// environment with zero live roots — exactly one wins, the other is rejected
// with last_root_removal_forbidden, and one live root remains.
func TestTrustService_ConcurrentRemovalLeavesAtLeastOneLiveRoot(t *testing.T) {
	// Use an on-disk store: modernc.org/sqlite's shared in-memory databases can
	// deadlock under simultaneous BEGIN IMMEDIATE, while the file store has
	// production locking semantics (WAL + busy_timeout).
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "trust-concurrent.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	svc := NewTrustService(st.TrustRoots(), nil, logger())
	first := createTrustRoot(t, svc, "key-1", "issuer-1")
	second := createTrustRoot(t, svc, "key-2", "issuer-2")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{first.GetId(), second.GetId()} {
		wg.Add(1)
		go func(rootID string) {
			defer wg.Done()
			<-start
			_, err := svc.RetireTrustRoot(t.Context(), connect.NewRequest(&trustv1.RetireTrustRootRequest{Environment: "staging", RootId: rootID}))
			errs <- err
		}(id)
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, forbidden int
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.ErrorContains(t, err, ErrLastRootRemovalForbidden.Error())
		forbidden++
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, forbidden)

	// The environment still has exactly one live root afterwards (public seam).
	policyResp, err := svc.GetTrustPolicy(t.Context(), connect.NewRequest(&trustv1.GetTrustPolicyRequest{Environment: "staging"}))
	require.NoError(t, err)
	live := 0
	for _, r := range policyResp.Msg.GetPolicy().GetRoots() {
		if r.GetState() == trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE ||
			r.GetState() == trustv1.TrustRootState_TRUST_ROOT_STATE_GRACE {
			live++
		}
	}
	assert.Equal(t, 1, live)
}
