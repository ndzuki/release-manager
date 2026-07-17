package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJWTManager_AccessTokenRoundTrip(t *testing.T) {
	manager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	token, expiresAt, err := manager.GenerateAccessToken("user-1", []string{"viewer"})
	require.NoError(t, err)

	claims, err := manager.ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
	assert.Equal(t, []string{"viewer"}, claims.Roles)
	assert.WithinDuration(t, expiresAt, claims.ExpiresAt.Time, time.Second)
}

func TestJWTManager_RejectsWrongSigningKey(t *testing.T) {
	token, _, err := NewJWTManager([]byte("key-a"), time.Hour, time.Hour).GenerateAccessToken("user-1", nil)
	require.NoError(t, err)

	_, err = NewJWTManager([]byte("key-b"), time.Hour, time.Hour).ValidateAccessToken(token)
	assert.Error(t, err)
}

func TestJWTManager_RefreshTokenIsRandomAndHashable(t *testing.T) {
	manager := NewJWTManager([]byte("key"), time.Hour, time.Hour)
	first, firstFamily, firstHash, err := manager.GenerateRefreshToken()
	require.NoError(t, err)
	second, secondFamily, secondHash, err := manager.GenerateRefreshToken()
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.NotEqual(t, firstFamily, secondFamily)
	assert.Equal(t, firstHash, manager.HashRefreshToken(first))
	assert.Equal(t, secondHash, manager.HashRefreshToken(second))
	assert.NotEqual(t, firstHash, secondHash)
}
