package observer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rolloutErrorCode extracts the stable ErrorCode from err, failing the test if
// err is not a *RolloutError.
func rolloutErrorCode(t *testing.T, err error) ErrorCode {
	t.Helper()
	var rolloutErr *RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	return rolloutErr.Code()
}

// assertRolloutLast checks that result matches err.(*RolloutError).Last.
func assertRolloutLast(t *testing.T, result WatchResult, err error) {
	t.Helper()
	var rolloutErr *RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	assert.Equal(t, result, rolloutErr.Last)
}
