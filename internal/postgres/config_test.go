package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDSNDoesNotExposePassword(t *testing.T) {
	err := ValidateDSN("postgres://user:secret@localhost:5432/release?sslmode=disable")
	require.NoError(t, err)
}

func TestValidateDSNRejectsInvalidInput(t *testing.T) {
	err := ValidateDSN("postgres://")
	require.ErrorContains(t, err, "dsn_invalid")
}
