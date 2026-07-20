package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type staticIdP struct {
	provider string
	identity *ExternalIdentity
	err      error
}

func (p staticIdP) Provider() string               { return p.provider }
func (p staticIdP) Validate(context.Context) error { return nil }
func (p staticIdP) Authenticate(context.Context, any) (*ExternalIdentity, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.identity, nil
}
func newExternalServiceTest(t *testing.T, provider ExternalIdP, cfg ExternalIdentityServiceConfig) (*ExternalIdentityService, *sqlitestore.Store) {
	t.Helper()

	st, err := sqlitestore.Open(t.TempDir() + "/auth.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	jwt := NewJWTManager([]byte("test-signing-key-long-enough"), time.Minute, time.Hour)
	service := NewExternalIdentityService(st, jwt, map[string]ExternalIdP{provider.Provider(): provider}, cfg, slog.Default())
	return service, st
}

func TestExternalIdentityService_AutoCreateIsIdempotent(t *testing.T) {
	provider := staticIdP{provider: ProviderLDAP, identity: &ExternalIdentity{
		Provider: ProviderLDAP,
		Subject:  "subject-1",
		Attributes: map[string]string{
			"username": "alice",
		},
	}}
	service, st := newExternalServiceTest(t, provider, ExternalIdentityServiceConfig{AutoCreate: true})
	ctx := context.Background()

	first, err := service.AuthenticateLDAP(ctx, connect.NewRequest(&authv1.AuthenticateLDAPRequest{Username: "alice", Password: "secret"}))
	require.NoError(t, err)
	require.NotEmpty(t, first.Msg.GetSession().GetAccessToken())

	second, err := service.AuthenticateLDAP(ctx, connect.NewRequest(&authv1.AuthenticateLDAPRequest{Username: "alice", Password: "secret"}))
	require.NoError(t, err)
	require.NotEmpty(t, second.Msg.GetSession().GetAccessToken())

	user, err := st.Users().GetByProviderSubject(ctx, ProviderLDAP, "subject-1")
	require.NoError(t, err)
	assert.Equal(t, "alice", user.Username)
	var count int
	require.NoError(t, st.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE provider = ? AND subject = ?", ProviderLDAP, "subject-1").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestExternalIdentityService_DisabledUserIsRejected(t *testing.T) {
	provider := staticIdP{provider: ProviderLDAP, identity: &ExternalIdentity{
		Provider: ProviderLDAP,
		Subject:  "disabled-subject",
		Attributes: map[string]string{
			"username": "disabled-user",
		},
	}}
	service, st := newExternalServiceTest(t, provider, ExternalIdentityServiceConfig{AutoCreate: true})
	ctx := context.Background()
	user := &store.User{ID: newID(), Username: "disabled-user", Provider: ProviderLDAP, Subject: "disabled-subject", Status: store.UserDisabled}
	require.NoError(t, st.Users().Create(ctx, user))

	_, err := service.AuthenticateLDAP(ctx, connect.NewRequest(&authv1.AuthenticateLDAPRequest{Username: "disabled-user", Password: "secret"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestExternalIdentityService_RequireApprovalCreatesPendingUser(t *testing.T) {
	provider := staticIdP{provider: ProviderLDAP, identity: &ExternalIdentity{
		Provider: ProviderLDAP,
		Subject:  "pending-subject",
		Attributes: map[string]string{
			"username": "pending-user",
		},
	}}
	service, st := newExternalServiceTest(t, provider, ExternalIdentityServiceConfig{AutoCreate: true, RequireApproval: true})
	ctx := context.Background()

	_, err := service.AuthenticateLDAP(ctx, connect.NewRequest(&authv1.AuthenticateLDAPRequest{Username: "pending-user", Password: "secret"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	user, getErr := st.Users().GetByProviderSubject(ctx, ProviderLDAP, "pending-subject")
	require.NoError(t, getErr)
	assert.Equal(t, store.UserPending, user.Status)
}

func TestExternalIdentityService_MapsExternalRole(t *testing.T) {
	provider := staticIdP{provider: ProviderLDAP, identity: &ExternalIdentity{
		Provider: ProviderLDAP,
		Subject:  "role-subject",
		Attributes: map[string]string{
			"username": "role-user",
			"role":     string(store.RoleReleaseAdmin),
		},
	}}
	service, st := newExternalServiceTest(t, provider, ExternalIdentityServiceConfig{AutoCreate: true, OrganizationID: "org-1"})
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Test Org"}))

	_, err := service.AuthenticateLDAP(ctx, connect.NewRequest(&authv1.AuthenticateLDAPRequest{Username: "role-user", Password: "secret"}))
	require.NoError(t, err)

	user, err := st.Users().GetByProviderSubject(ctx, ProviderLDAP, "role-subject")
	require.NoError(t, err)
	member, err := st.OrgMembers().Get(ctx, "org-1", user.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RoleReleaseAdmin, member.Role)
}

func TestExternalIdentityService_HidesProviderErrors(t *testing.T) {
	provider := staticIdP{provider: ProviderLDAP, err: errors.New("ldap server leaked detail")}
	service, _ := newExternalServiceTest(t, provider, ExternalIdentityServiceConfig{AutoCreate: true})

	_, err := service.AuthenticateLDAP(context.Background(), connect.NewRequest(&authv1.AuthenticateLDAPRequest{Username: "alice", Password: "bad"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.NotContains(t, err.Error(), "ldap server leaked detail")
}
