package devfixture

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testPrivateKeyPEM is a fresh Ed25519 private key for CI-mode tests.
var testPrivateKeyPEM = mustEncodeTestKey()

func mustEncodeTestKey() string {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	encoded, err := encodePrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// fakeMigrations is the injectable Migrations seam for reset state-machine
// tests.
type fakeMigrations struct {
	downCalls int
	upCalls   int
	closed    bool
	downErr   error
	upErr     error
}

func (f *fakeMigrations) DownAll(context.Context) error {
	f.downCalls++
	return f.downErr
}

func (f *fakeMigrations) Up(context.Context) error {
	f.upCalls++
	return f.upErr
}

func (f *fakeMigrations) Close() error { f.closed = true; return nil }

func resetRunner(t *testing.T, fakes *fakeServices, mg *fakeMigrations) *runner {
	t.Helper()
	r := testRunner(t, fakes)
	r.cfg.MigrationsFactory = func(string) (Migrations, error) { return mg, nil }
	return r
}

func TestReset_SuccessRebuildsAndReseeds(t *testing.T) {
	fakes := newFakeServices()
	mg := &fakeMigrations{}
	r := resetRunner(t, fakes, mg)

	manifest, err := r.reset(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Equal(t, 1, mg.downCalls)
	require.Equal(t, 1, mg.upCalls)
	require.True(t, mg.closed)

	p := loadProgressFile(t, r.cfg.DataDir)
	require.False(t, p.Partial)
	for _, phase := range phaseOrder {
		require.Equal(t, "committed", p.Phases[phase].Status)
	}
	// Enrollment tokens regenerated after the DB wipe.
	for _, seed := range clusterSeeds {
		_, err := os.Stat(r.enrollmentTokenPath(seed.id))
		require.NoError(t, err)
	}
	require.Equal(t, 2, fakes.orch.count("CreateCustomer"))
}

func TestReset_SeedFailureMarksPartialAndPreservesPhases(t *testing.T) {
	fakes := newFakeServices()
	fakes.webhook.failEvery = errors.New("bundle endpoint down")
	mg := &fakeMigrations{}
	r := resetRunner(t, fakes, mg)

	_, err := r.reset(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "bundle phase")
	require.Equal(t, 1, mg.downCalls)
	require.Equal(t, 1, mg.upCalls)

	p := loadProgressFile(t, r.cfg.DataDir)
	require.True(t, p.Partial)
	// Phases completed before the failure stay committed.
	require.Equal(t, "committed", p.Phases["identity"].Status)
	require.Equal(t, "committed", p.Phases["trust"].Status)
	_, ok := p.Phases["bundle"]
	require.False(t, ok)
}

func TestReset_MigrationFailureMarksPartialAndSkipsSeed(t *testing.T) {
	fakes := newFakeServices()
	mg := &fakeMigrations{downErr: errors.New("down boom")}
	r := resetRunner(t, fakes, mg)

	_, err := r.reset(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "down-all")
	require.Equal(t, 1, mg.downCalls)
	require.Equal(t, 0, mg.upCalls)

	p := loadProgressFile(t, r.cfg.DataDir)
	require.True(t, p.Partial)
	require.Empty(t, p.Phases)

	// No seed writes happened.
	require.Equal(t, 0, fakes.orch.count("CreateCustomer"))
}

func TestReset_IgnoresFixtureVersion(t *testing.T) {
	fakes := newFakeServices()
	mg := &fakeMigrations{}
	r := resetRunner(t, fakes, mg)

	// Pre-existing progress with a stale version and a committed identity.
	progress := newProgress("v1")
	progress.Phases["identity"] = phaseState{Status: "committed", CommittedAt: "old"}
	raw, err := json.Marshal(progress)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(r.progressPath(), raw, 0o600))

	_, err = r.reset(context.Background())
	require.NoError(t, err)
	p := loadProgressFile(t, r.cfg.DataDir)
	require.Equal(t, "v2", p.FixtureVersion)
	require.False(t, p.Partial)
	for _, phase := range phaseOrder {
		require.Equal(t, "committed", p.Phases[phase].Status)
	}
}

func TestCredentials_GeneratedThenReused(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	creds, err := r.resolveCredentials(r.cfg)
	require.NoError(t, err)
	require.Len(t, creds.admin, 32)
	require.Len(t, creds.deployer, 32)
	require.Len(t, creds.reader, 32)

	path := filepath.Join(r.cfg.DataDir, credentialsFileName)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// A second runner reuses the file (no regeneration).
	r2 := testRunner(t, fakes, func(c *Config) { c.DataDir = r.cfg.DataDir })
	creds2, err := r2.resolveCredentials(r2.cfg)
	require.NoError(t, err)
	require.Equal(t, creds, creds2)

	// Malformed file → regenerated.
	require.NoError(t, os.WriteFile(path, []byte("DEV_ADMIN_PASSWORD=short\n"), 0o600))
	r3 := testRunner(t, fakes, func(c *Config) { c.DataDir = r.cfg.DataDir })
	creds3, err := r3.resolveCredentials(r3.cfg)
	require.NoError(t, err)
	require.Len(t, creds3.admin, 32)
	require.NotEqual(t, creds.admin, creds3.admin)
}

func TestCredentials_ExplicitOverridesWin(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes, func(c *Config) {
		c.AdminPassword = strings.Repeat("x", 32)
	})
	creds, err := r.resolveCredentials(r.cfg)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("x", 32), creds.admin)
	require.Len(t, creds.deployer, 32)
}

func TestCredentials_CIModeFailsWithoutAllSecrets(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes, func(c *Config) {
		c.Mode = ModeCI
		c.AdminPassword = strings.Repeat("a", 32)
		// deployer and reader missing
	})
	_, err := r.resolveCredentials(r.cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "DEV_DEPLOYER_PASSWORD")
	require.Contains(t, err.Error(), "DEV_READER_PASSWORD")
}

func TestTrustRootKey_ReusedAcrossRuns(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	key1, err := r.loadOrGenerateTrustRootKey(r.cfg)
	require.NoError(t, err)
	path := r.trustRootKeyPath()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	key1Pub, ok1 := key1.Public().(ed25519.PublicKey)
	key2, err := r.loadOrGenerateTrustRootKey(r.cfg)
	require.NoError(t, err)
	key2Pub, ok2 := key2.Public().(ed25519.PublicKey)
	require.True(t, ok1 && ok2)
	require.Equal(t, key1Pub, key2Pub)

	// CI mode loads the injected key and writes nothing.
	ci := testRunner(t, fakes, func(c *Config) {
		c.Mode = ModeCI
		c.TrustRootPrivateKey = string(mustPEM(key1))
	})
	key3, err := ci.loadOrGenerateTrustRootKey(ci.cfg)
	require.NoError(t, err)
	key3Pub, ok3 := key3.Public().(ed25519.PublicKey)
	require.True(t, ok3)
	require.Equal(t, key1Pub, key3Pub)
	_, err = os.Stat(ci.trustRootKeyPath())
	require.True(t, os.IsNotExist(err))
}

func mustPEM(key ed25519.PrivateKey) []byte {
	encoded, err := encodePrivateKey(key)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestChartArchivePackaging_RealFixtureChart(t *testing.T) {
	// The fixture chart ships with the repo (deploy/fixtures/chart); verify
	// the helm-SDK packaging produces a valid content digest without any
	// registry involvement.
	tgz, err := packageChartArchive(filepath.Join("..", "..", "deploy", "fixtures", "chart"))
	require.NoError(t, err)
	defer os.Remove(tgz) //nolint:errcheck // test cleanup

	digest, err := archiveDigest(tgz)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(digest, "sha256:"))
	require.Len(t, digest, len("sha256:")+64)

	// Deterministic: packaging the same chart twice yields the same digest.
	tgz2, err := packageChartArchive(filepath.Join("..", "..", "deploy", "fixtures", "chart"))
	require.NoError(t, err)
	defer os.Remove(tgz2) //nolint:errcheck // test cleanup
	digest2, err := archiveDigest(tgz2)
	require.NoError(t, err)
	require.Equal(t, digest, digest2)
}

func TestFallbackChartDigest_Deterministic(t *testing.T) {
	require.Equal(t, fallbackChartDigest(), fallbackChartDigest())
	require.True(t, strings.HasPrefix(fallbackChartDigest(), "sha256:"))
}
