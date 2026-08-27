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
	"regexp"
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
	// AC-065-28 knobs keep their defaults (3 retries) but the real 1s/2s/4s
	// backoff and 3s operator poll would slow every test; shorten both for
	// the duration of this test.
	prevDelays := seedRetryDelays
	prevPoll := operatorOnlinePollPeriod
	seedRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	operatorOnlinePollPeriod = time.Millisecond
	t.Cleanup(func() {
		seedRetryDelays = prevDelays
		operatorOnlinePollPeriod = prevPoll
	})
	cfg := Config{
		Mode:              ModeLocal,
		OrchestratorURL:   "http://orchestrator.test",
		WebhookURL:        "http://webhook.test",
		AuthURL:           "http://auth.test",
		DataDir:           t.TempDir(),
		ChartDir:          filepath.Join(t.TempDir(), "missing-chart"),
		AdminUser:         "dev-admin",
		DeployerUser:      "dev-deployer",
		InstallTimeout:    5 * time.Second,
		InstallPollPeriod: time.Millisecond,
		// Short operator-online deadline: offline-session cases poll a few
		// times instead of waiting the real 180s (AC-065-28 default).
		OperatorOnlineTimeout: 100 * time.Millisecond,
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:                   func() time.Time { return testNow },
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

	// Trust RPCs must carry the admin bearer token: the real TrustService
	// sits behind the shared auth interceptor (real smoke 2026-08-24:
	// missing header → unauthenticated on GetTrustPolicy).
	for i, hdr := range fakes.trust.lastAuthHdrs {
		require.Truef(t, strings.HasPrefix(hdr, "Bearer "),
			"trust call %d missing bearer token: %q", i, hdr)
	}

	// CreateOperation must act as e2e-runner (release_admin): the deployer
	// role cannot create Operations (REQ-027 matrix / D-101; real smoke
	// 2026-08-27: install phase permission_denied on the deployer token).
	require.NotEmpty(t, fakes.orch.createAuthHdrs)
	for i, hdr := range fakes.orch.createAuthHdrs {
		require.Equalf(t, "Bearer token-e2e-runner", hdr,
			"CreateOperation %d must carry the e2e-runner bearer, got %q", i, hdr)
	}

	// Enrollment token files: one per cluster.
	for _, seed := range clusterSeeds {
		info, err := os.Stat(r.enrollmentTokenPath(seed.id))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	// Enrollment token operator_name must equal the cluster id: the agent's
	// CSR CommonName (from its operator.dev.yaml operator_name) is the
	// cluster id, and the store rejects a token↔operator name mismatch with
	// ErrNotAuthorized ("approval command not authorized", real smoke
	// 2026-08-27 — the token previously carried operator-<cluster>).
	require.Len(t, fakes.orch.enrollTokens, 4)
	for _, tok := range fakes.orch.enrollTokens {
		require.Equalf(t, tok.GetClusterId(), tok.GetOperatorName(),
			"enrollment token for %s must carry operator_name = cluster id", tok.GetClusterId())
	}

	// Manifest persisted on disk.
	raw, err := os.ReadFile(r.manifestPath())
	require.NoError(t, err)
	var onDisk Manifest
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	require.Equal(t, manifest.Bundle.ID, onDisk.Bundle.ID)

	// AC-065-34: four development accounts with correct roles — e2e-runner
	// carries release_admin (REQ-066 E2E write identity, 批次4 D1/D2).
	require.Equal(t, []string{"release_admin"}, fakes.auth.users[e2eRunnerUser].GetRoles())
	require.Equal(t, []string{"platform_admin"}, fakes.auth.users["dev-admin"].GetRoles())
	require.Equal(t, []string{"deployer"}, fakes.auth.users["dev-deployer"].GetRoles())
	require.Equal(t, []string{"viewer"}, fakes.auth.users[readerUser].GetRoles())
	// The credentials file carries all four keys (AC-065-34).
	credsRaw, err := os.ReadFile(filepath.Join(r.cfg.DataDir, credentialsFileName))
	require.NoError(t, err)
	for _, key := range []string{"DEV_ADMIN_PASSWORD", "DEV_DEPLOYER_PASSWORD", "DEV_READER_PASSWORD", "E2E_RUNNER_PASSWORD"} {
		require.Truef(t, strings.Contains(string(credsRaw), key+"="), "credentials file missing %s", key)
	}
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
	// A persistent bundle outage: with AC-065-28 retries (1 initial + 3
	// retries) the phase fails for good and the run turns partial.
	fakes.webhook.failEvery = errors.New("simulated bundle outage")
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "bundle phase")
	require.Contains(t, err.Error(), "after 3 retries")
	p := loadProgressFile(t, r.cfg.DataDir)
	require.True(t, p.Partial)
	// Phases before the failure are committed and preserved.
	require.Equal(t, "committed", p.Phases["identity"].Status)
	require.Equal(t, "committed", p.Phases["trust"].Status)
	_, ok := p.Phases["bundle"]
	require.False(t, ok)

	// Resume: earlier phases must not re-create entities. The simulated
	// outage is over.
	fakes.webhook.failEvery = nil
	identityCreates := fakes.orch.count("CreateCustomer") + fakes.orch.count("CreateCluster")

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Equal(t, identityCreates, fakes.orch.count("CreateCustomer")+fakes.orch.count("CreateCluster"))
	// First run burned 1 initial + 3 retries on the failing bundle phase;
	// the resumed run submits once.
	require.Equal(t, 5, fakes.webhook.calls)

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
		c.E2ERunnerPassword = strings.Repeat("d", 32)
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

// TestRun_SeedRetryRecoversFromTransientWriteFailure covers AC-065-28: a
// transient phase-write failure is retried (default 3 retries with 1s/2s/4s
// backoff; shortened in tests) and the run completes without partial state.
func TestRun_SeedRetryRecoversFromTransientWriteFailure(t *testing.T) {
	fakes := newFakeServices()
	fakes.webhook.failOnce = errors.New("simulated transient bundle outage")
	r := testRunner(t, fakes)

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest)
	// Initial attempt + one retry.
	require.Equal(t, 2, fakes.webhook.calls)
	p := loadProgressFile(t, r.cfg.DataDir)
	require.False(t, p.Partial)
	for _, phase := range phaseOrder {
		require.Equal(t, "committed", p.Phases[phase].Status)
	}
}

// TestRun_FixtureVersionChangeArchivesProgressAndPrunes covers AC-065-30:
// the old progress generation is archived as
// archive-<ISO8601>-<fixture_version>.json and data/archive/ keeps only the
// most recent 3 generations.
func TestRun_FixtureVersionChangeArchivesProgressAndPrunes(t *testing.T) {
	fakes := newFakeServices()
	dataDir := t.TempDir()
	r := testRunner(t, fakes, func(c *Config) {
		c.DataDir = dataDir
		c.FixtureVersion = "v1"
	})
	_, err := r.run(context.Background())
	require.NoError(t, err)

	// Three legacy archive generations from earlier contract moves (older
	// timestamps than testNow so the v1 archive is newest).
	archiveDir := filepath.Join(dataDir, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0o700))
	for _, name := range []string{
		"archive-2026-01-01T00-00-00Z-v0.json",
		"archive-2026-02-01T00-00-00Z-v0.json",
		"archive-2026-03-01T00-00-00Z-v0.json",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(archiveDir, name), []byte("{}\n"), 0o600))
	}

	// The version authority moved v1 -> v2 (maintainer bump): the v1
	// progress is archived and the archive pruned to 3 generations — the
	// oldest (2026-01) generation is dropped.
	r2 := testRunner(t, fakes, func(c *Config) {
		c.DataDir = dataDir
		c.FixtureVersion = "v2"
	})
	manifest, err := r2.run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v2", manifest.FixtureVersion)

	entries, err := os.ReadDir(archiveDir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.Len(t, names, 3, "archive must keep the 3 most recent generations, got %v", names)
	require.Contains(t, names, "archive-2026-08-14T10-00-00Z-v1.json")
	require.Contains(t, names, "archive-2026-03-01T00-00-00Z-v0.json")
	require.Contains(t, names, "archive-2026-02-01T00-00-00Z-v0.json")
	require.NotContains(t, names, "archive-2026-01-01T00-00-00Z-v0.json")

	// The persisted progress carries the new authoritative version.
	p := loadProgressFile(t, dataDir)
	require.Equal(t, "v2", p.FixtureVersion)
}

// TestLoadCredentialsFileEnforcesPasswordCharset covers D2: values must be
// exactly 32 characters of [A-Za-z0-9]; any other character (or length)
// makes the file invalid so it is regenerated.
func TestLoadCredentialsFileEnforcesPasswordCharset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-credentials.env")
	valid := "DEV_ADMIN_PASSWORD=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"DEV_DEPLOYER_PASSWORD=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB\n" +
		"DEV_READER_PASSWORD=01234567890123456789012345678901\n" +
		"E2E_RUNNER_PASSWORD=CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC\n"
	require.NoError(t, os.WriteFile(path, []byte(valid), 0o600))
	creds, ok := loadCredentialsFile(path)
	require.True(t, ok)
	require.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", creds.admin)
	require.Equal(t, "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", creds.runner)

	// Same length, but a '-' sneaks into the alphanumeric contract.
	invalid := "DEV_ADMIN_PASSWORD=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-\n" +
		"DEV_DEPLOYER_PASSWORD=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB\n" +
		"DEV_READER_PASSWORD=01234567890123456789012345678901\n" +
		"E2E_RUNNER_PASSWORD=CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC\n"
	require.NoError(t, os.WriteFile(path, []byte(invalid), 0o600))
	_, ok = loadCredentialsFile(path)
	require.False(t, ok)

	// Three keys (the pre-批次4 format) is stale: the file must carry all
	// four accounts and is regenerated.
	stale := "DEV_ADMIN_PASSWORD=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"DEV_DEPLOYER_PASSWORD=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB\n" +
		"DEV_READER_PASSWORD=01234567890123456789012345678901\n"
	require.NoError(t, os.WriteFile(path, []byte(stale), 0o600))
	_, ok = loadCredentialsFile(path)
	require.False(t, ok)
}

// TestRun_GetBundleReadbackCarriesAdminBearer covers the D-016 residual fix
// (AC-065-33 后半链): GetBundle on the real orchestrator sits behind the
// shared auth interceptor, so both readback paths — the committed-phase
// consistency check (checkCommittedBundle) and verify (verifyBundle) — must
// attach the admin bearer (real smoke 2026-08-25: unauthenticated).
func TestRun_GetBundleReadbackCarriesAdminBearer(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)
	// The first run reads the bundle twice: checkCommittedBundle does not
	// run for uncommitted phases, so this is verifyBundle only.
	require.NotEmpty(t, fakes.bundle.lastAuthHdrs)
	for i, hdr := range fakes.bundle.lastAuthHdrs {
		require.Truef(t, strings.HasPrefix(hdr, "Bearer "),
			"GetBundle call %d missing bearer token: %q", i, hdr)
	}

	// The second run exercises checkCommittedBundle for the committed
	// bundle phase; it must carry the bearer too.
	before := len(fakes.bundle.lastAuthHdrs)
	_, err = r.run(context.Background())
	require.NoError(t, err)
	require.Greater(t, len(fakes.bundle.lastAuthHdrs), before)
	for _, hdr := range fakes.bundle.lastAuthHdrs {
		require.True(t, strings.HasPrefix(hdr, "Bearer "), "GetBundle call missing bearer token: %q", hdr)
	}
}

// TestRun_AllWritesCarryStableIdempotencyKeys covers 批次5 D9 / AC-065-41:
// every create/submit-class write carries a stable Idempotency-Key in the
// devseed-<phase>-<logical-key> format, and the key never embeds a
// server-generated id (the old devseed-create-values-<definitionID> key
// would not be replayable on a phase-internal retry because the definition
// id does not exist on the retry path).
func TestRun_AllWritesCarryStableIdempotencyKeys(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)

	keys := []string{}
	keys = append(keys, fakes.orch.idemKeys...)
	keys = append(keys, fakes.auth.idemKeys...)
	keys = append(keys, fakes.binding.idemKeys...)
	keys = append(keys, fakes.trust.idemKeys...)
	keys = append(keys, fakes.webhook.idemKeys...)
	require.NotEmpty(t, keys)

	keyPattern := regexp.MustCompile(`^devseed-[a-z]+-[a-z0-9-]+$`)
	for _, key := range keys {
		require.Truef(t, keyPattern.MatchString(key),
			"write idempotency key %q does not match devseed-<phase>-<logical-key>", key)
		// The old contract embedded server-generated ids (definition id):
		// any key carrying one breaks retry idempotency.
		require.NotContains(t, key, "definition-", "idempotency key embeds a server definition id")
		require.NotContains(t, key, "values-1", "idempotency key embeds a server values id")
		require.NotContains(t, key, "devseed-create-values-", "legacy key format must be gone")
	}

	want := []string{
		"devseed-identity-dev-customer-a",
		"devseed-identity-dev-customer-b",
		"devseed-identity-binding-dev-customer-a",
		"devseed-identity-dev-customer-a-direct",
		"devseed-identity-dev-customer-b-mixed",
		"devseed-routing-dev-route-ca-direct-image",
		"devseed-accounts-dev-deployer",
		"devseed-accounts-dev-reader",
		"devseed-accounts-e2e-runner",
		"devseed-trust-dev-trust-root",
		"devseed-bundle-submit",
		"devseed-values-e2e-release-target-definition",
		"devseed-values-e2e-release-target",
		"devseed-values-e2e-release-target-submit",
		"devseed-values-e2e-release-target-approve",
		"devseed-enrollment-dev-customer-a-direct",
		"devseed-install-e2e-release",
	}
	for _, key := range want {
		require.Contains(t, keys, key, "expected write to carry idempotency key %s", key)
	}
}

// TestRun_WriteRetryReusesSameIdempotencyKey covers AC-065-41's retry leg:// a phase-internal retry (DEV_TIMEOUT_SEED_RETRIES) replays the same key so
// the server-side scoped idempotency dedupes instead of creating duplicates.
func TestRun_WriteRetryReusesSameIdempotencyKey(t *testing.T) {
	fakes := newFakeServices()
	fakes.webhook.failOnce = errors.New("simulated transient bundle outage")
	r := testRunner(t, fakes)

	_, err := r.run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, fakes.webhook.calls)
	require.Len(t, fakes.webhook.idemKeys, 2)
	require.Equal(t, "devseed-bundle-submit", fakes.webhook.idemKeys[0])
	require.Equal(t, fakes.webhook.idemKeys[0], fakes.webhook.idemKeys[1],
		"retry must replay the same idempotency key")
}

// TestRun_StopAfterPhaseCommitsUpToAndStops covers the split-seed contract:
// --stop-after enrollment commits identity..enrollment, leaves install and
// verify uncommitted, exits cleanly (no partial marker), and a subsequent
// plain Run resumes at the install phase.
func TestRun_StopAfterPhaseCommitsUpToAndStops(t *testing.T) {
	fakes := newFakeServices()
	r := testRunner(t, fakes, func(c *Config) { c.StopAfterPhase = "enrollment" })

	manifest, err := r.run(context.Background())
	require.NoError(t, err)
	require.Nil(t, manifest) // verify never ran: no readback manifest yet
	p := loadProgressFile(t, r.cfg.DataDir)
	require.False(t, p.Partial, "stop-after is a clean exit, not a failure")
	for _, phase := range []string{"identity", "routing", "accounts", "trust", "bundle", "values", "enrollment"} {
		require.Equal(t, "committed", p.Phases[phase].Status, "phase %s", phase)
	}
	_, ok := p.Phases["install"]
	require.False(t, ok, "install must not run under --stop-after enrollment")

	// Resume: a plain run completes install + verify.
	r2 := testRunner(t, fakes, func(c *Config) {
		c.DataDir = r.cfg.DataDir
	})
	manifest2, err := r2.run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, manifest2)
	p2 := loadProgressFile(t, r.cfg.DataDir)
	require.Equal(t, "committed", p2.Phases["install"].Status)
	require.Equal(t, "committed", p2.Phases["verify"].Status)
	// No duplicate writes for the phases committed before the split: the
	// deployer (authenticate), reader and e2e-runner (accounts phase) are
	// each created exactly once across both runs.
	require.Equal(t, 2, fakes.orch.count("CreateCustomer"))
	require.Equal(t, 3, fakes.auth.createCalls)
}

// TestRun_InstallWaitsForBundleValidation covers the bundle_not_ready leg
// (real smoke 2026-08-27): the orchestrator validates bundles
// asynchronously (30s worker poll), so the install phase must wait for the
// VALIDATED status instead of burning its retry budget against a received
// bundle. A bundle that never validates fails closed with the observed
// status within the (test-shortened) window.
func TestRun_InstallWaitsForBundleValidation(t *testing.T) {
	fakes := newFakeServices()
	fakes.webhook.validateSync = false
	r := testRunner(t, fakes)

	prevPoll := bundleValidationPoll
	prevTimeout := bundleValidationTimeout
	bundleValidationPoll = time.Millisecond
	bundleValidationTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		bundleValidationPoll = prevPoll
		bundleValidationTimeout = prevTimeout
	})

	_, err := r.run(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not validated")
	require.Contains(t, err.Error(), "BUNDLE_STATUS_RECEIVED")
	// The install phase never created an operation (the wait gates it).
	require.Equal(t, 0, fakes.orch.count("CreateOperation"))
	p := loadProgressFile(t, r.cfg.DataDir)
	require.True(t, p.Partial)
}
