package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testJWTPrivateKey is the Ed25519 signing key shared by the internal/auth
// tests (REQ-065 AC-065-01). Access tokens are EdDSA now, so a placeholder byte
// string can no longer stand in for key material: a manager built with one
// refuses to sign. Every manager in this package shares the key so a token
// minted in one place still verifies in another.
var testJWTPrivateKey = func() ed25519.PrivateKey {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("generate test JWT key: " + err.Error())
	}
	return privateKey
}()

func TestJWTManager_OrganizationClaim(t *testing.T) {
	manager := NewJWTManager(testJWTPrivateKey, time.Hour, time.Hour)
	token, _, err := manager.GenerateAccessToken(
		"user-1",
		"org-1",
		[]string{"viewer"},
	)
	require.NoError(t, err)

	claims, err := manager.ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
	assert.Equal(t, "org-1", claims.OrgID)
	assert.Equal(t, []string{"viewer"}, claims.Roles)
}
