package devfixture

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// DevelopmentFixture is the canonical seed contract (REQ-065): stable
// logical identities, deterministic client-supplied ids where the contract
// allows them (CreateCustomer/CreateCluster), and the nine-phase execution
// order. Changing any identity here is a breaking fixture change (bump
// FixtureVersion).
const (
	orgName = "dev-platform"
	// readerUser is the third development account (viewer role).
	readerUser = "dev-reader"
	// e2eRunnerUser is the fourth development account: REQ-066's E2E write
	// identity with the release_admin role (CreateOperation / RollbackRelease
	// / emergency changes; 批次4 D1/D2, AC-065-34).
	e2eRunnerUser = "e2e-runner"

	bundleName      = "dev-release-bundle"
	// bundleChartRef is the OCI reference the OPERATOR pulls (installed into
	// the customer cluster). It must be reachable from the operator pod, so it
	// uses the registry.dev.release-manager.local hostAlias injected into the
	// agent deployment (real smoke 2026-08-27: localhost:5001 resolved to the
	// pod's own loopback → helm_install_failed connection refused).
	bundleChartRef  = "oci://registry.dev.release-manager.local:5000/release-fixture"
	// bundleChartHostRef is the HOST-side push reference (devseed pushes the
	// chart archive into the local registry from the dev host, where the
	// registry is published as localhost:5001). Same registry, different
	// reachable endpoint.
	bundleChartHostRef = "localhost:5001/release-fixture"
	bundleChartVer   = "0.1.0"
	bundleGitCommit  = "dev"
	bundlePipeline   = "dev-seed"

	// devTrustRootKeyID is the stable key id of the Dev Trust Root.
	devTrustRootKeyID = "dev-trust-root"
	// devTrustRootIssuer matches the trusted-issuer policy in every
	// environment (trust.DefaultPolicy); subject pattern prefixes the
	// signature subject.
	devTrustRootIssuer  = "release-manager-ci"
	devTrustRootSubject = "release-manager/dev-seed"
)

// snapshotWarmupBackoff is the retry delay after an authorization-snapshot
// staleness error; a package var so tests can shorten it.
var snapshotWarmupBackoff = 2 * time.Second

// valuesDocument is the minimal approved values document for every E2E
// target (REQ-065: replicaCount: 1).
const valuesDocument = `{"replicaCount":1}`

// phase names, fixed order (REQ-065 progress schema).
var phaseOrder = []string{"identity", "routing", "accounts", "trust", "bundle", "values", "enrollment", "install", "verify"}

// Customer seeds: logical identity, deterministic id, name, slug.
type customerSeed struct {
	logicalKey string
	id         string
	name       string
	slug       string
}

var customerSeeds = []customerSeed{
	{logicalKey: "dev-customer-a", id: "11111111-1111-4111-8111-111111111111", name: "Development Customer A", slug: "development-customer-a"},
	{logicalKey: "dev-customer-b", id: "22222222-2222-4222-8222-222222222222", name: "Development Customer B", slug: "development-customer-b"},
}

// Cluster seeds: logical key = cluster id, name, owning customer logical key.
type clusterSeed struct {
	id          string
	name        string
	customerKey string
	shortKey    string // route id short alias (ca-direct, ...)
}

var clusterSeeds = []clusterSeed{
	{id: "dev-customer-a-direct", name: "Customer A Direct", customerKey: "dev-customer-a", shortKey: "ca-direct"},
	{id: "dev-customer-a-cache", name: "Customer A Pull Through Cache", customerKey: "dev-customer-a", shortKey: "ca-cache"},
	{id: "dev-customer-b-replicated", name: "Customer B Replicated", customerKey: "dev-customer-b", shortKey: "cb-replicated"},
	{id: "dev-customer-b-mixed", name: "Customer B Mixed", customerKey: "dev-customer-b", shortKey: "cb-mixed"},
}

// routeSeed is one cluster route (REQ-065 table). Direct mode repeats the
// source prefix as target (the server rejects empty target prefixes).
type routeSeed struct {
	id           string
	clusterKey   string
	artifactType orchestratorv1.ArtifactType
	mode         orchestratorv1.ArtifactMode
	sourcePrefix string
	targetPrefix string
}

var routeSeeds = []routeSeed{
	{id: "dev-route-ca-direct-image", clusterKey: "dev-customer-a-direct", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "docker.io/library/", targetPrefix: "docker.io/library/"},
	{id: "dev-route-ca-direct-chart", clusterKey: "dev-customer-a-direct", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "https://charts.example.com/", targetPrefix: "https://charts.example.com/"},
	{id: "dev-route-ca-cache-image", clusterKey: "dev-customer-a-cache", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "docker.io/library/", targetPrefix: "registry.dev.release-manager.local:5000/proxy/"},
	{id: "dev-route-ca-cache-chart", clusterKey: "dev-customer-a-cache", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "https://charts.example.com/", targetPrefix: "registry.dev.release-manager.local:5000/charts/"},
	{id: "dev-route-cb-replicated-image", clusterKey: "dev-customer-b-replicated", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_PULL_THROUGH_CACHE, sourcePrefix: "docker.io/library/", targetPrefix: "registry.dev.release-manager.local:5000/cache/"},
	{id: "dev-route-cb-replicated-chart", clusterKey: "dev-customer-b-replicated", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "https://charts.example.com/", targetPrefix: "https://charts.example.com/"},
	{id: "dev-route-cb-mixed-image", clusterKey: "dev-customer-b-mixed", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "docker.io/library/", targetPrefix: "docker.io/library/"},
	{id: "dev-route-cb-mixed-chart", clusterKey: "dev-customer-b-mixed", artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "https://charts.example.com/", targetPrefix: "registry.dev.release-manager.local:5000/charts/"},
}

// Definition seeds (REQ-065): four E2E targets on dev-customer-a-direct,
// chart fixture-chart, minimal approved values replicaCount: 1.
type definitionSeed struct {
	logicalKey  string
	releaseName string
	namespace   string
}

var definitionSeeds = []definitionSeed{
	{logicalKey: "e2e-release-target", releaseName: "e2e-release", namespace: "e2e-release"},
	{logicalKey: "e2e-isolation-target", releaseName: "e2e-isolation", namespace: "e2e-isolation"},
	{logicalKey: "e2e-emergency-target", releaseName: "e2e-emergency", namespace: "e2e-emergency"},
	{logicalKey: "e2e-restart-target", releaseName: "e2e-restart", namespace: "e2e-restart"},
}

// phaseState is one committed phase entry in dev-seed-progress.json. The
// REQ-065 schema is a strict subset; bundle id/digest and operation ids are
// persisted extras so a resumed run can verify committed entities without a
// fixture manifest (ListBundles requires a definition id for clients).
type phaseState struct {
	Status       string   `json:"status"`
	CommittedAt  string   `json:"committed_at,omitempty"`
	Operations   []string `json:"operations,omitempty"`
	BundleID     string   `json:"bundle_id,omitempty"`
	BundleDigest string   `json:"bundle_digest,omitempty"`
}

// progress is the persisted seed progress document.
type progress struct {
	FixtureVersion string                `json:"fixture_version"`
	Partial        bool                  `json:"partial,omitempty"`
	Phases         map[string]phaseState `json:"phases"`
}

func newProgress(version string) progress {
	return progress{FixtureVersion: version, Phases: map[string]phaseState{}}
}

// runState carries tokens, resolved credentials, and accumulated server ids
// across phases.
type runState struct {
	adminToken     string
	deployerToken  string
	runnerToken    string
	adminOrgID     string
	deployerUserID string
	credentials    credentials
	trustRootKey   ed25519.PrivateKey

	customers map[string]string // logical key → server id
	clusters  map[string]string // cluster id → server id (id == logical key)
	// definitions maps logical key → definition record (ids filled in).
	definitions map[string]definitionRecord
	bundle      bundleRecord
	operations  []string
}

type definitionRecord struct {
	id               string
	valuesRevisionID string
}

type bundleRecord struct {
	id     string
	digest string
}

// runner executes the seed phases against the configured clients.
type runner struct {
	cfg      Config
	clients  *connectClients
	progress progress
	manifest *Manifest
	state    runState
	chartSvc chartPackager
	// imageDigest resolves the real registry digest of the fixture image for
	// the bundle. Defaults to fixtureImageDigest (hits the local registry);
	// tests inject a stub to avoid the network seam.
	imageDigest func() (string, error)
}

func newRunner(cfg Config) *runner {
	return &runner{cfg: cfg, clients: newConnectClients(cfg), chartSvc: defaultChartPackager{cfg: cfg}, imageDigest: fixtureImageDigest}
}

func (r *runner) progressPath() string { return filepath.Join(r.cfg.DataDir, progressFileName) }
func (r *runner) manifestPath() string { return filepath.Join(r.cfg.DataDir, manifestFileName) }

func (r *runner) loadProgress() error {
	raw, err := os.ReadFile(r.progressPath())
	if errors.Is(err, os.ErrNotExist) {
		r.progress = newProgress(r.cfg.FixtureVersion)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read seed progress: %w", err)
	}
	var p progress
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("parse seed progress: %w", err)
	}
	if p.Phases == nil {
		p.Phases = map[string]phaseState{}
	}
	r.progress = p
	return nil
}

func (r *runner) saveProgress() error {
	raw, err := json.MarshalIndent(r.progress, "", "  ")
	if err != nil {
		return fmt.Errorf("encode seed progress: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(r.progressPath(), raw); err != nil {
		return fmt.Errorf("write seed progress: %w", err)
	}
	return nil
}

func (r *runner) loadManifest() (*Manifest, error) {
	raw, err := os.ReadFile(r.manifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read fixture manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse fixture manifest: %w", err)
	}
	return &m, nil
}

func (r *runner) saveManifest(m *Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fixture manifest: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(r.manifestPath(), raw); err != nil {
		return fmt.Errorf("write fixture manifest: %w", err)
	}
	return nil
}

// markPartial flags the progress document as partial without disturbing the
// committed phase structure (dev-reset-data failure path contract).
func (r *runner) markPartial() {
	r.progress.Partial = true
	if err := r.saveProgress(); err != nil {
		r.cfg.log().Warn("mark progress partial failed", "error", err)
	}
}

// authenticate is the login preamble shared by every run: it resolves the
// credentials, initializes the system on first boot, logs in the admin and
// deployer actors, and resolves the admin organization.
func (r *runner) authenticate(ctx context.Context) error {
	creds, err := r.resolveCredentials(r.cfg)
	if err != nil {
		return err
	}
	r.state.credentials = creds

	authClient := r.clients.auth
	initStatus, err := authClient.GetInitStatus(ctx, connect.NewRequest(&authv1.GetInitStatusRequest{}))
	if err != nil {
		return fmt.Errorf("get init status: %w", err)
	}
	if !initStatus.Msg.GetInitialized() {
		if _, err := authClient.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
			Username:         r.cfg.AdminUser,
			Password:         creds.admin,
			OrganizationName: orgName,
		})); err != nil {
			return fmt.Errorf("initialize system: %w", err)
		}
		r.cfg.log().Info("system initialized", "admin_user", r.cfg.AdminUser, "organization", orgName)
	}

	adminLogin, err := authClient.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: r.cfg.AdminUser, Password: creds.admin}))
	if err != nil {
		return fmt.Errorf("login admin user %s: %w", r.cfg.AdminUser, err)
	}
	r.state.adminToken = adminLogin.Msg.GetAccessToken()

	userReq := connect.NewRequest(&authv1.GetLocalUserRequest{Username: r.cfg.AdminUser})
	withAuth(userReq, r.state.adminToken)
	userResp, err := authClient.GetLocalUser(ctx, userReq)
	if err != nil {
		return fmt.Errorf("resolve admin org: %w", err)
	}
	r.state.adminOrgID = userResp.Msg.GetUser().GetOrgId()
	if r.state.adminOrgID == "" {
		return fmt.Errorf("login admin user %s: no active organization", r.cfg.AdminUser)
	}

	deployerLogin, err := authClient.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: r.cfg.DeployerUser, Password: creds.deployer}))
	if err == nil {
		r.state.deployerToken = deployerLogin.Msg.GetAccessToken()
		r.state.deployerUserID = deployerLogin.Msg.GetUser().GetId()
		return nil
	}
	createReq := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: r.cfg.DeployerUser,
		Password: creds.deployer,
		Roles:    []string{"deployer"},
	})
	withAuth(createReq, r.state.adminToken)
	createReq.Header().Set("Idempotency-Key", idempotencyKey("accounts", r.cfg.DeployerUser))
	if _, createErr := authClient.CreateLocalUser(ctx, createReq); createErr != nil {
		return fmt.Errorf("login deployer user %s: %w; provision via CreateLocalUser: %v", r.cfg.DeployerUser, err, createErr)
	}
	r.cfg.log().Info("deployer user created", "user", r.cfg.DeployerUser)
	deployerLogin, err = authClient.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: r.cfg.DeployerUser, Password: creds.deployer}))
	if err != nil {
		return fmt.Errorf("login deployer user %s after provisioning: %w", r.cfg.DeployerUser, err)
	}
	r.state.deployerToken = deployerLogin.Msg.GetAccessToken()
	r.state.deployerUserID = deployerLogin.Msg.GetUser().GetId()
	return nil
}

// run drives the idempotent nine-phase seed.
func (r *runner) run(ctx context.Context) (*Manifest, error) {
	if err := r.loadProgress(); err != nil {
		return nil, err
	}
	manifest, err := r.loadManifest()
	if err != nil {
		return nil, err
	}
	r.manifest = manifest

	// A fixture version change means the seed contract moved on; previous
	// progress is stale and every phase re-runs (idempotently). The old
	// progress generation is archived first (AC-065-30), keeping the most
	// recent 3 generations in data/archive/.
	if r.progress.FixtureVersion != r.cfg.FixtureVersion {
		r.cfg.log().Info("fixture version changed, re-seeding all phases",
			"old", r.progress.FixtureVersion, "new", r.cfg.FixtureVersion)
		if err := r.archiveProgress(); err != nil {
			return nil, err
		}
		r.progress = newProgress(r.cfg.FixtureVersion)
		r.manifest = nil
	}

	if err := r.authenticate(ctx); err != nil {
		r.markPartial()
		return nil, err
	}

	// Canonical consistency of every already-committed phase before any
	// further write (drift → fixture_conflict).
	if err := r.checkCommittedPhases(ctx); err != nil {
		return nil, err
	}

	if r.allCommitted() {
		if r.manifest == nil {
			// Progress says committed but the manifest is missing: rebuild it
			// through the verify readback, hydrating server ids from the
			// persisted progress extras.
			r.state.definitions = map[string]definitionRecord{}
			r.state.customers = map[string]string{}
			r.state.clusters = map[string]string{}
			r.state.operations = r.progress.Phases["install"].Operations
			r.state.bundle = bundleRecord{
				id:     r.progress.Phases["bundle"].BundleID,
				digest: r.progress.Phases["bundle"].BundleDigest,
			}
			if err := r.phaseVerify(ctx); err != nil {
				return nil, err
			}
		}
		r.cfg.log().Info("fixture up-to-date", "fixture_version", r.cfg.FixtureVersion)
		return r.manifest, nil
	}

	// Resuming into the install phase (values already committed) needs the
	// in-memory definition/bundle records that the values/bundle phases
	// normally populate: hydrate them from the server readback and the
	// persisted progress extras (split-seed resume contract, AC-065-03).
	if r.progress.Phases["values"].Status == "committed" && r.progress.Phases["install"].Status != "committed" {
		r.state.definitions = map[string]definitionRecord{}
		if err := r.readbackDefinitions(ctx); err != nil {
			return nil, err
		}
		r.state.bundle = bundleRecord{
			id:     r.progress.Phases["bundle"].BundleID,
			digest: r.progress.Phases["bundle"].BundleDigest,
		}
	}

	if err := r.runPhases(ctx); err != nil {
		r.markPartial()
		return nil, err
	}
	r.progress.Partial = false
	if err := r.saveProgress(); err != nil {
		return nil, err
	}
	return r.manifest, nil
}

func (r *runner) allCommitted() bool {
	for _, name := range phaseOrder {
		state, ok := r.progress.Phases[name]
		if !ok || state.Status != "committed" {
			return false
		}
	}
	return true
}

// checkCommittedPhases verifies canonical consistency of every committed
// phase: entities exist and stable logical keys match, without comparing
// server-generated mutable fields (UUID, created_at, updated_at).
func (r *runner) checkCommittedPhases(ctx context.Context) error {
	for _, name := range phaseOrder {
		state, ok := r.progress.Phases[name]
		if !ok || state.Status != "committed" {
			continue
		}
		var err error
		switch name {
		case "identity":
			err = r.checkCommittedIdentity(ctx)
		case "routing":
			err = r.checkCommittedRouting(ctx)
		case "accounts":
			err = r.checkCommittedAccounts(ctx)
		case "trust":
			err = r.checkCommittedTrust(ctx)
		case "bundle":
			err = r.checkCommittedBundle(ctx)
		case "values":
			err = r.checkCommittedValues(ctx)
		case "enrollment":
			err = r.checkCommittedEnrollment(ctx)
		case "install":
			err = r.checkCommittedInstall(ctx, state)
		case "verify":
			continue // verify is the readback itself; nothing to re-check
		}
		if err != nil {
			return wrapConflict("phase %s drift: %v", name, err)
		}
	}
	return nil
}

// checkCommittedAccounts verifies the four accounts still authenticate with
// the effective passwords (reader and e2e-runner included).
func (r *runner) checkCommittedAccounts(ctx context.Context) error {
	authClient := r.clients.auth
	for _, user := range []struct {
		name     string
		password string
	}{
		{r.cfg.AdminUser, r.state.credentials.admin},
		{r.cfg.DeployerUser, r.state.credentials.deployer},
		{readerUser, r.state.credentials.reader},
		{e2eRunnerUser, r.state.credentials.runner},
	} {
		if _, err := authClient.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: user.name, Password: user.password})); err != nil {
			return fmt.Errorf("account %s no longer authenticates: %w", user.name, err)
		}
	}
	return nil
}

// retrySnapshotWarmup retries a governance write once after a short backoff
// when the authorization snapshot is still warming up (REQ-027); re-driving
// the same idempotent decision after the warmup window is safe and expected.
func retrySnapshotWarmup[T any](ctx context.Context, call func() (*connect.Response[T], error)) (*connect.Response[T], error) {
	response, err := call()
	if err == nil || !isAuthorizationSnapshotStale(err) {
		return response, err
	}
	select {
	case <-time.After(snapshotWarmupBackoff):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return call()
}

func isAuthorizationSnapshotStale(err error) bool {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) && connectErr.Meta().Get("X-Reason-Code") == "authorization_snapshot_stale" {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "authorization snapshot stale") || strings.Contains(message, "authorization snapshot is stale")
}
