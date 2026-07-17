package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)

	assert.True(t, VerifyPassword(hash, "correct horse battery staple"))
	assert.False(t, VerifyPassword(hash, "wrong password"))
	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, cost, bcryptCost)
}

func TestHashPasswordRejectsBcryptOversizedInput(t *testing.T) {
	_, err := HashPassword(strings.Repeat("x", 73))
	assert.Error(t, err)
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	assert.False(t, VerifyPassword("not-a-bcrypt-hash", "password"))
}
