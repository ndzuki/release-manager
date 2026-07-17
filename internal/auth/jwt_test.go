package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJWTManager_OrganizationClaim(t *testing.T) {
	manager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
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
