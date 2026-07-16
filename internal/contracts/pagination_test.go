package contracts

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeCursor(t *testing.T) {
	t.Run("round-trip", func(t *testing.T) {
		ts := time.Date(2026, 7, 16, 12, 0, 0, 123456789, time.UTC)
		id := "op-001"

		token := EncodeCursor(ts, id)
		assert.NotEmpty(t, token)

		decodedTS, decodedID, err := DecodeCursor(token)
		require.NoError(t, err)
		assert.Equal(t, ts.UnixNano(), decodedTS.UnixNano())
		assert.Equal(t, id, decodedID)
	})

	t.Run("empty token", func(t *testing.T) {
		_, _, err := DecodeCursor("")
		assert.ErrorContains(t, err, "empty cursor token")
	})

	t.Run("malformed base64", func(t *testing.T) {
		_, _, err := DecodeCursor("!!!not-base64!!!")
		assert.ErrorContains(t, err, "invalid cursor encoding")
	})

	t.Run("malformed payload", func(t *testing.T) {
		// "dG9rZW4" is valid base64 and decodes to "token" — no '|' separator.
		_, _, err := DecodeCursor("dG9rZW4")
		assert.ErrorContains(t, err, "malformed cursor")
	})
}

func TestNormalizePageSize(t *testing.T) {
	tests := []struct {
		name     string
		input    int32
		expected int32
	}{
		{"zero defaults to 20", 0, 20},
		{"negative defaults to 20", -5, 20},
		{"within range", 50, 50},
		{"min boundary", 1, 1},
		{"max boundary", 100, 100},
		{"above max clamped", 200, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizePageSize(tt.input))
		})
	}
}
