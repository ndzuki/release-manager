package devfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

var testNow = time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

// testRunner builds a runner wired to fake services with a temp data dir.
func testRunner(t *testing.T, fakes *fakeServices, mutate ...func(*Config)) *runner {
	t.Helper()
	cfg := Config{
		Mode:              ModeLocal,
		OrchestratorURL:   "http://orchestrator.test",
		WebhookURL:        "http://webhook.test",
		AuthURL:           "http://auth.test",
		DataDir:           t.TempDir(),
		ChartDir:          filepath.Join(t.TempDir(), "missing-chart"),
		AdminUser:         "admin",
		DeployerUser:      "deployer",
		InstallTimeout:    5 * time.Second,
		InstallPollPeriod: time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:               func() time.Time { return testNow },
	}
	for _, fn := range mutate {
		fn(&cfg)
	}
	cfg = cfg.withDefaults()
	r := newRunner(cfg)
	if fakes != nil {
		r.clients = &connectClients{
			auth: fakes.auth, binding: fakes.binding, orch: fakes.orch,
			trust: fakes.trust, webhook: fakes.webhook, bundle: fakes.bundle,
		}
	}
	r.chartSvc = stubChartPackager{digest: "sha256:" + strings.Repeat("ab", 32)}
	return r
}

// stubChartPackager avoids chart packaging/registry side effects in tests.
type stubChartPackager struct{ digest string }

func (s stubChartPackager) Package(context.Context) (string, error) { return s.digest, nil }
func (s stubChartPackager) Digest() (string, error)                 { return s.digest, nil }

// loggingSink captures log output for up-to-date assertions.
type loggingSink struct{ buf bytes.Buffer }

func (s *loggingSink) Handle(_ context.Context, record slog.Record) error {
	var message strings.Builder
	message.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		message.WriteString(" ")
		message.WriteString(attr.Key)
		message.WriteString("=")
		message.WriteString(attr.Value.String())
		return true
	})
	s.buf.WriteString(message.String() + "\n")
	return nil
}

func (s *loggingSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *loggingSink) WithGroup(string) slog.Handler      { return s }
func (s *loggingSink) Enabled(context.Context, slog.Level) bool {
	return true
}

func loadProgressFile(t *testing.T, dataDir string) progress {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, progressFileName))
	require.NoError(t, err)
	var p progress
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

func TestRun_SeedsAllNinePhases(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)

	p := loadProgressFile(t, r.cfg.DataDir)
	require.Equal(t, "v2", p.FixtureVersion)
	for _, phase := range phaseOrder {
		state, ok := p.Phases[phase]
		require.Truef(t, ok, "phase %s committed", phase)
		require.Equal(t, "committed", state.Status)
	}
	require.False(t, p.Partial)
	require.Len(t, p.Phases["install"].Operations, 4)

	// Manifest shape (REQ-065): 2 customers, 4 clusters, 4 definitions, 1 bundle.
	require.Len(t, manifest.Customers, 2)
	require.Len(t, manifest.Clusters, 4)
	require.Len(t, manifest.Definitions, 4)
	for _, seed := range definitionSeeds {
		ref := manifest.Definitions[seed.logicalKey]
		require.NotEmpty(t, ref.ID)
		require.NotEmpty(t, ref.ValuesRevisionID)
		require.Equal(t, manifest.Bundle.ID, ref.BundleID)
	}
	require.NotEmpty(t, manifest.Bundle.ID)
	require.True(t, strings.HasPrefix(manifest.Bundle.Digest, "sha256:"))

	// Entity write counts match the canonical fixture.
	orch := fakes.orch
	require.Equal(t, 2, orch.count("CreateCustomer"))
	require.Equal(t, 4, orch.count("CreateCluster"))
	require.Equal(t, 8, orch.count("ConfigureClusterRoute"))
	require.Equal(t, 4, orch.count("CreateReleaseDefinition"))
	require.Equal(t, 4, orch.count("CreateValuesRevision"))
	require.Equal(t, 4, orch.count("SubmitValuesRevision"))
	require.Equal(t, 4, orch.count("ApproveValuesRevision"))
	require.Equal(t, 4, orch.count("CreateEnrollmentToken"))
	require.Equal(t, 4, orch.count("CreateOperation"))
	require.Equal(t, 1, fakes.webhook.calls)
	require.Equal(t, 1, fakes.trust.calls["CreateTrustRoot"])

	// Enrollment token files: one per cluster.
	for _, seed := range clusterSeeds {
		info, err := os.Stat(r.enrollmentTokenPath(seed.id))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	// Manifest persisted on disk.
	raw, err := os.ReadFile(r.manifestPath())
	require.NoError(t, err)
	var onDisk Manifest
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	require.Equal(t, manifest.Bundle.ID, onDisk.Bundle.ID)
}

func TestRun_RepeatedRunIsIdempotentAndUpToDate(t *testing.T) {
	fakes := newFakeServices()
	sink := &loggingSink{}
	r := testRunner(t, fakes, func(c *Config) {
		c.Logger = slog.New(sink)
	})

	_, err := r.run(context.Background())
	require.NoError(t, err)

	writeMethods := []string{
		"CreateCustomer", "CreateCluster", "ConfigureClusterRoute",
		"CreateReleaseDefinition", "CreateValuesRevision",
		"SubmitValuesRevision", "ApproveValuesRevision",
		"CreateEnrollmentToken", "CreateOperation",
	}
	writesBefore := map[string]int{}
	for _, method := range writeMethods {
		writesBefore[method] = fakes.orch.count(method)
	}
	bundleCalls := fakes.webhook.calls
	trustCreates := fakes.trust.calls["CreateTrustRoot"]

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)

	// No duplicate write RPCs on the second run (AC-065-03).
	for method, before := range writesBefore {
		require.Equalf(t, before, fakes.orch.count(method), "write RPC %s repeated on up-to-date run", method)
	}
	require.Equal(t, bundleCalls, fakes.webhook.calls)
	require.Equal(t, trustCreates, fakes.trust.calls["CreateTrustRoot"])
	require.Contains(t, sink.buf.String(), "fixture up-to-date")

	p := loadProgressFile(t, r.cfg.DataDir)
	require.False(t, p.Partial)
}

func TestRun_CanonicalDriftYieldsFixtureConflict(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)

	// Drift: delete a seeded customer server-side.
	delete(fakes.orch.customers, "11111111-1111-4111-8111-111111111111")

	_, err = r.run(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFixtureConflict), "expected fixture_conflict, got %v", err)
}

func TestRun_InterruptedResumeContinuesFromFailedPhase(t *testing.T) {
	fakes := newFakeServices()
	fakes.webhook.failOnce = errors.New("simulated bundle outage")
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "bundle phase")
	p := loadProgressFile(t, r.cfg.DataDir)
	require.True(t, p.Partial)
	// Phases before the failure are committed and preserved.
	require.Equal(t, "committed", p.Phases["identity"].Status)
	require.Equal(t, "committed", p.Phases["trust"].Status)
	_, ok := p.Phases["bundle"]
	require.False(t, ok)

	// Resume: earlier phases must not re-create entities.
	identityCreates := fakes.orch.count("CreateCustomer") + fakes.orch.count("CreateCluster")

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Equal(t, identityCreates, fakes.orch.count("CreateCustomer")+fakes.orch.count("CreateCluster"))
	require.Equal(t, 2, fakes.webhook.calls)

	p = loadProgressFile(t, r.cfg.DataDir)
	require.False(t, p.Partial)
	for _, phase := range phaseOrder {
		require.Equal(t, "committed", p.Phases[phase].Status)
	}
}

func TestRun_AllCommittedManifestMissingRebuilds(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.Remove(r.manifestPath()))

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Len(t, manifest.Definitions, 4)
	// Regenerated manifest persisted.
	_, err = os.Stat(r.manifestPath())
	require.NoError(t, err)
	// No entity re-created.
	require.Equal(t, 2, fakes.orch.count("CreateCustomer"))
	require.Equal(t, 4, fakes.orch.count("CreateOperation"))
}

func TestRun_FailedInstallOperationRetriesWithFreshKey(t *testing.T) {
	fakes := newFakeServices()
	// The deterministic key for the first E2E target replays a failed op.
	fakes.orch.opKeyTerminal["devseed-install-e2e-release"] = orchestratorv1.OperationStatus_OPERATION_STATUS_FAILED
	r := testRunner(t, fakes)

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	// 4 base keys + 1 retry key for the failed target.
	require.Equal(t, 5, fakes.orch.count("CreateOperation"))
	p := loadProgressFile(t, r.cfg.DataDir)
	require.Len(t, p.Phases["install"].Operations, 4)
}

func TestRun_InstallTimeoutReportsError(t *testing.T) {
	fakes := newFakeServices()
	// An op stuck in a non-terminal state → wait timeout.
	fakes.orch.opKeyTerminal["devseed-install-e2e-release"] = orchestratorv1.OperationStatus_OPERATION_STATUS_QUEUED
	r := testRunner(t, fakes, func(c *Config) {
		c.InstallTimeout = 30 * time.Millisecond
	})

	_, err := r.run(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "terminal state")
	p := loadProgressFile(t, r.cfg.DataDir)
	require.True(t, p.Partial)
}

func TestRun_ValuesRevisionWorkflowDrivesSubmitApprove(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)

	// Every definition has exactly one approved revision with the seed digest.
	require.Len(t, fakes.orch.values, 4)
	for _, revision := range fakes.orch.values {
		require.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_APPROVED, revision.GetStatus())
		require.Equal(t, valuesDigest([]byte(valuesDocument)), revision.GetDigest())
	}
}

func TestRun_CIProfileRequiresSecrets(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes, func(c *Config) {
		c.Mode = ModeCI
	})
	_, err := r.run(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "DEV_ADMIN_PASSWORD")
}

func TestRun_CIProfileUsesEnvSecretsAndWritesNoFiles(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes, func(c *Config) {
		c.Mode = ModeCI
		c.AdminPassword = strings.Repeat("a", 32)
		c.DeployerPassword = strings.Repeat("b", 32)
		c.ReaderPassword = strings.Repeat("c", 32)
		c.TrustRootPrivateKey = testPrivateKeyPEM
	})

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	// No credentials file and no trust root key file in CI mode.
	_, err = os.Stat(filepath.Join(r.cfg.DataDir, credentialsFileName))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(r.trustRootKeyPath())
	require.True(t, os.IsNotExist(err))
}

func TestRun_TrustRootCreatedOnce(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, fakes.trust.calls["CreateTrustRoot"])

	_, err = r.run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, fakes.trust.calls["CreateTrustRoot"])
}

func TestWithAuth_SetsBearerHeader(t *testing.T) {
	req := connect.NewRequest(&authv1.GetInitStatusRequest{})
	withAuth(req, "token-abc")
	require.Equal(t, "Bearer token-abc", req.Header().Get("Authorization"))
}

func TestValuesDigest_MatchesServerSemantics(t *testing.T) {
	digest := valuesDigest([]byte(`{"replicaCount":1}`))
	require.Len(t, digest, 64)
	require.NotEqual(t, valuesDigest([]byte(`{"replicaCount":2}`)), digest)
}

func TestRetrySnapshotWarmup_RetriesOnceOnStale(t *testing.T) {
	previous := snapshotWarmupBackoff
	snapshotWarmupBackoff = time.Millisecond
	defer func() { snapshotWarmupBackoff = previous }()

	calls := 0
	response, err := retrySnapshotWarmup(context.Background(), func() (*connect.Response[orchestratorv1.GetOperationResponse], error) {
		calls++
		if calls == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("authorization snapshot stale"))
		}
		return connect.NewResponse(&orchestratorv1.GetOperationResponse{}), nil
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, 2, calls)
}

func TestRetrySnapshotWarmup_DoesNotRetryOtherErrors(t *testing.T) {
	previous := snapshotWarmupBackoff
	snapshotWarmupBackoff = time.Millisecond
	defer func() { snapshotWarmupBackoff = previous }()

	calls := 0
	_, err := retrySnapshotWarmup(context.Background(), func() (*connect.Response[orchestratorv1.GetOperationResponse], error) {
		calls++
		return nil, errors.New("boom")
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

// TestVerifyEnrollment_RequiresOnlineOperatorSessions covers AC-065-18: the
// verify phase fails with `operator_not_online` naming the faulting cluster
// when an operator session has not reached ONLINE, and passes when every
// cluster has an online session.
func TestVerifyEnrollment_RequiresOnlineOperatorSessions(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)
	_, err := r.run(context.Background())
	require.NoError(t, err) // default fake sessions are ONLINE

	// Take one cluster's session offline: verify must now fail naming it.
	fakes.orch.mu.Lock()
	fakes.orch.operators[clusterSeeds[0].id] = orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_OFFLINE
	fakes.orch.mu.Unlock()

	err = r.phaseVerify(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator_not_online")
	require.Contains(t, err.Error(), clusterSeeds[0].id)
	require.Contains(t, err.Error(), "OFFLINE")

	// Restore ONLINE: verify passes again.
	fakes.orch.mu.Lock()
	fakes.orch.operators[clusterSeeds[0].id] = orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_ONLINE
	fakes.orch.mu.Unlock()
	err = r.phaseVerify(context.Background())
	require.NoError(t, err)
}
