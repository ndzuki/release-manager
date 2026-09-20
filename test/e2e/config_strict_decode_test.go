package e2e_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// AC-066-29: the consumer decodes strictly, so a manifest that drifts from the
// schema -- a renamed or removed producer key, for one -- fails loudly instead
// of silently loading with that field zero.
//
// The rest of the consumer contract already has coverage in
// config_snapshot_blackbox_test.go, which also owns validConfigYAML; this pins
// the one part it does not, which is the decoder's KnownFields behaviour.
func TestParseConfigRejectsAnUnknownKey(t *testing.T) {
	t.Setenv("E2E_RUNNER_PASSWORD", "s3cret-from-env")

	// The shared valid manifest must load, so the rejection below is
	// attributable to the added key rather than to the fixture.
	_, err := e2e.ParseConfig([]byte(validConfigYAML))
	require.NoError(t, err, "the shared valid manifest must be accepted")

	_, err = e2e.ParseConfig([]byte(validConfigYAML + "\nproducer_key_that_no_longer_exists: 1\n"))
	require.Error(t, err, "AC-066-29: an unknown key must be rejected, not ignored")
	// The decoder rejects without naming the key (the error is the generic
	// "decode yaml"), so the assertion stops at the rejection itself. Naming it
	// would be a better diagnostic but is not what the AC requires.
	assert.Contains(t, err.Error(), "decode yaml")
}
