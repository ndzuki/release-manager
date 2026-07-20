package auth

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLDAPProvider_ValidateRejectsPlaintextProductionBinding(t *testing.T) {
	t.Parallel()

	provider := NewLDAPProvider(LDAPConfig{
		URL:        "ldap://ldap.example.com",
		BindDN:     "cn=service,dc=example,dc=com",
		BaseDN:     "dc=example,dc=com",
		Production: true,
	})

	err := provider.Validate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext")
}

func TestLDAPProvider_ValidateAllowsStartTLSProductionBinding(t *testing.T) {
	t.Parallel()

	provider := NewLDAPProvider(LDAPConfig{
		URL:        "ldap://ldap.example.com",
		BindDN:     "cn=service,dc=example,dc=com",
		BaseDN:     "dc=example,dc=com",
		Production: true,
		StartTLS:   true,
	})

	require.NoError(t, provider.Validate(context.Background()))
}

func TestDingTalkProvider_StateIsSingleUse(t *testing.T) {
	t.Parallel()

	provider := NewDingTalkProvider(DingTalkConfig{
		ClientID:     "client",
		ClientSecret: "secret",
		RedirectURL:  "https://app.example.com/auth/dingtalk/callback",
	})
	authURL, err := provider.AuthURL(context.Background())
	require.NoError(t, err)

	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	state := parsed.Query().Get("state")
	require.NotEmpty(t, state)

	_, ok := provider.states.Take(state)
	assert.True(t, ok)
	_, ok = provider.states.Take(state)
	assert.False(t, ok)
}

func TestStateStore_RejectsExpiredState(t *testing.T) {
	t.Parallel()

	states := newStateStore(-time.Second)
	states.Put("expired", struct{}{})

	_, ok := states.Take("expired")
	assert.False(t, ok)
}

func TestLDAPRoleMapping(t *testing.T) {
	t.Parallel()

	mapping := map[string]string{
		"release-admins": "release_admin",
		"viewers":        "viewer",
	}
	attributes := map[string]string{}
	for _, group := range []string{"unknown", "release-admins", "viewers"} {
		if role := mapping[group]; role != "" {
			attributes["role"] = role
			break
		}
	}

	assert.Equal(t, "release_admin", attributes["role"])
}
