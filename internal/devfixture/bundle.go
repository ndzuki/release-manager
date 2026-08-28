package devfixture

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/registry"
)

// bundle image constants (REQ-065: the bundle carries the fixture chart plus
// one image artifact; the chart's values reference localhost:5001/release-fixture).
const (
	bundleImageRef        = "localhost:5001/release-fixture:dev"
	bundleImageValuesPath = "image.repository"
)

// chartPackager produces the deterministic chart digest for the bundle.
// Package performs side effects (packaging + registry push); Digest is the
// side-effect-free form used by canonical consistency checks.
type chartPackager interface {
	Package(ctx context.Context) (digest string, err error)
	Digest() (string, error)
}

// defaultChartPackager packages deploy/fixtures/chart with the Helm SDK when
// the chart is present and falls back to a deterministic content digest
// otherwise (TASK-065: the fixture chart may not exist yet in older trees).
type defaultChartPackager struct {
	cfg Config
}

func (p defaultChartPackager) Package(ctx context.Context) (string, error) {
	if _, err := os.Stat(filepath.Join(p.cfg.ChartDir, "Chart.yaml")); err != nil {
		p.cfg.log().Info("fixture chart not present, using deterministic fallback digest", "chart_dir", p.cfg.ChartDir)
		return fallbackChartDigest(), nil
	}
	tgz, err := packageChartArchive(p.cfg.ChartDir)
	if err != nil {
		return "", err
	}
	defer os.Remove(tgz) //nolint:errcheck // temp archive cleanup is best-effort
	digest, err := archiveDigest(tgz)
	if err != nil {
		return "", err
	}
	if err := pushChartArchive(ctx, tgz); err != nil {
		return "", err
	}
	p.cfg.log().Info("fixture chart packaged and pushed", "digest", digest)
	return digest, nil
}

func (p defaultChartPackager) Digest() (string, error) {
	if _, err := os.Stat(filepath.Join(p.cfg.ChartDir, "Chart.yaml")); err != nil {
		return fallbackChartDigest(), nil
	}
	tgz, err := packageChartArchive(p.cfg.ChartDir)
	if err != nil {
		return "", err
	}
	defer os.Remove(tgz) //nolint:errcheck // temp archive cleanup is best-effort
	return archiveDigest(tgz)
}

// packageChartArchive loads the chart directory and saves it as a tgz,
// returning the archive path (helm SDK only — no helm CLI). The archive is
// timestamp-normalized before returning so the tgz bytes are content
// addressed: helm's chartutil.Save stamps every tar entry with time.Now()
// (save.go writeToTar), which would make each packaging produce a different
// digest — the resume's bundle drift check would then never match the
// submitted digest (real smoke 2026-08-27: "bundle identity mismatch").
func packageChartArchive(chartDir string) (string, error) {
	chart, err := loader.LoadDir(chartDir)
	if err != nil {
		return "", fmt.Errorf("load fixture chart: %w", err)
	}
	dir, err := os.MkdirTemp("", "devfixture-chart-*")
	if err != nil {
		return "", fmt.Errorf("create chart temp dir: %w", err)
	}
	path, err := chartutil.Save(chart, dir)
	if err != nil {
		return "", fmt.Errorf("package fixture chart: %w", err)
	}
	if err := normalizeChartTgz(path); err != nil {
		return "", fmt.Errorf("normalize fixture chart archive: %w", err)
	}
	return path, nil
}

// normalizeChartTgz rewrites a helm chart tgz with deterministic tar and
// gzip headers: every file entry gets a fixed modification time and the gzip
// header carries the fixed Helm magic (matching chartutil.Save's output
// shape). Two packagings of the same chart then produce identical bytes.
func normalizeChartTgz(tgzPath string) error {
	raw, err := os.ReadFile(tgzPath)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)

	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	// Header.Extra/Header.Comment — the embedded Header field; staticcheck
	// QF1008 suggests dropping the selector, but Header is the standard
	// gzip.Header struct field and the explicit form reads clearly (kept
	// intentional: matches helm's save.go output byte-for-byte).
	zw.Header.Extra = []byte("+aHR0cHM6Ly95b3V0dS5iZS96OVV6MWljandyTQo=") //nolint:staticcheck // explicit Header field reads clearly
	zw.Header.Comment = "Helm"                                                //nolint:staticcheck // explicit Header field reads clearly
	tw := tar.NewWriter(zw)
	const fixedModTime = int64(0) // epoch: deterministic regardless of when packaged
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		hdr.ModTime = time.Unix(fixedModTime, 0).UTC()
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// The archive is a trusted dev fixture chart; copy with a sane bound
		// (64 MiB) to satisfy the decompression-bomb check.
		if _, err := io.Copy(tw, io.LimitReader(tr, 64<<20)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(tgzPath, out.Bytes(), 0o600)
}

// archiveDigest returns the content sha256 digest of a chart archive.
func archiveDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read chart archive: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// pushChartArchive pushes the packaged chart to the local k3d registry as an
// OCI artifact via the Helm registry SDK (plain HTTP for the localhost dev
// registry). The push uses the HOST-side reference (bundleChartHostRef): the
// registry is published at localhost:5001 on the dev host, while the bundle's
// canonical chart ref (bundleChartRef) is the cluster-reachable
// registry.dev.release-manager.local alias the operator pulls from.
func pushChartArchive(_ context.Context, tgzPath string) error {
	raw, err := os.ReadFile(tgzPath)
	if err != nil {
		return fmt.Errorf("read chart archive for push: %w", err)
	}
	client, err := registry.NewClient(registry.ClientOptPlainHTTP())
	if err != nil {
		return fmt.Errorf("create helm registry client: %w", err)
	}
	ref := bundleChartHostRef + ":" + bundleChartVer
	if _, err := client.Push(raw, ref); err != nil {
		return fmt.Errorf("push fixture chart to %s: %w", ref, err)
	}
	return nil
}

// fallbackChartDigest deterministically digests the canonical chart
// descriptor so bundle submissions stay content-addressed and idempotent
// even before the fixture chart directory exists.
func fallbackChartDigest() string {
	payload := `{"name":"release-fixture","version":"0.1.0","values":{"replicaCount":1}}`
	sum := sha256.Sum256([]byte(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fixtureImageDigest resolves the REAL registry digest of the dev fixture
// image (release-fixture:dev, re-tagged by dev.sh from the content-sha256
// push). The bundle's image digest must match what the operator pulls and
// what preflight resolves (real smoke 2026-08-27: the previous content
// digest of the ref STRING was fake — image pulls and preflight digest
// parity could never match the actual manifest).
func fixtureImageDigest() (string, error) {
	client, err := registry.NewClient(registry.ClientOptPlainHTTP())
	if err != nil {
		return "", fmt.Errorf("create helm registry client: %w", err)
	}
	desc, err := client.Resolve(bundleImageRef)
	if err != nil {
		return "", fmt.Errorf("resolve fixture image %s: %w", bundleImageRef, err)
	}
	if desc.Digest == "" {
		return "", fmt.Errorf("resolve fixture image %s: empty digest", bundleImageRef)
	}
	return desc.Digest.String(), nil
}

// phaseBundle packages and submits the fixture release bundle through the
// webhook service. The bundle carries chart + image digests computed from
// deterministic content so re-submissions dedupe server-side (the bundle
// store is idempotent on the canonical digest). The Dev Trust Root private
// key signs the canonical bundle digest; the signature is attached to the
// install operations (CreateOperation.SignatureRef), not to the submission
// itself — the server's canonical bundle digest includes signature evidence,
// which would make the signed payload circular.
func (r *runner) phaseBundle(ctx context.Context) error {
	chartDigest, err := r.chartSvc.Package(ctx)
	if err != nil {
		return err
	}
	imageDigest, err := r.imageDigest()
	if err != nil {
		return err
	}
	r.cfg.log().Info("fixture image digest resolved", "ref", bundleImageRef, "digest", imageDigest)

	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         bundleName,
		ChartRef:     bundleChartRef,
		ChartVersion: bundleChartVer,
		ChartDigest:  chartDigest,
		Images: []*commonv1.BundleImage{{
			Ref:        bundleImageRef,
			Digest:     imageDigest,
			ValuesPath: bundleImageValuesPath,
			ValueKind:  commonv1.ImageValueKind_IMAGE_VALUE_KIND_FULL_REFERENCE,
		}},
		GitCommit:  bundleGitCommit,
		PipelineId: bundlePipeline,
	})
	req.Header().Set("Idempotency-Key", idempotencyKey("bundle", "submit"))
	response, err := r.clients.webhook.SubmitReleaseBundle(ctx, req)
	if err != nil {
		return fmt.Errorf("submit release bundle: %w", err)
	}
	bundle := response.Msg.GetBundle()
	if bundle == nil || bundle.GetId() == "" {
		return fmt.Errorf("submit release bundle: empty bundle response")
	}
	r.state.bundle = bundleRecord{id: bundle.GetId(), digest: bundleDigestString(bundle.GetDigest())}
	r.cfg.log().Info("release bundle submitted",
		"bundle_id", bundle.GetId(),
		"digest", r.state.bundle.digest,
		"created", response.Msg.GetCreated(),
	)
	return nil
}

// bundleDigestString renders a ReleaseDigest as "algorithm:value".
func bundleDigestString(digest *commonv1.ReleaseDigest) string {
	if digest == nil {
		return ""
	}
	if digest.GetAlgorithm() != "" {
		return digest.GetAlgorithm() + ":" + digest.GetValue()
	}
	return digest.GetValue()
}

// checkCommittedBundle verifies the recorded bundle still exists with the
// same chart content digest (logical-key check via the persisted bundle id).
func (r *runner) checkCommittedBundle(ctx context.Context) error {
	state := r.progress.Phases["bundle"]
	if state.BundleID == "" {
		return fmt.Errorf("bundle id not recorded in progress")
	}
	chartDigest, err := r.chartSvc.Digest()
	if err != nil {
		return fmt.Errorf("compute chart digest: %w", err)
	}
	// D-016 残余 (AC-065-33 后半链): GetBundle runs behind the shared auth
	// interceptor on the real orchestrator — the readback must carry the
	// admin bearer like every other phase (real smoke 2026-08-25:
	// unauthenticated) and is definition-scoped (real smoke 2026-08-27:
	// not_authorized: release_definition_id is required).
	definitionID, err := r.resolveBundleDefinitionID(ctx)
	if err != nil {
		return fmt.Errorf("get bundle %s: %w", state.BundleID, err)
	}
	req := connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId:            state.BundleID,
		ReleaseDefinitionId: definitionID,
	})
	withAuth(req, r.state.adminToken)
	response, err := r.clients.bundle.GetBundle(ctx, req)
	if err != nil {
		return fmt.Errorf("get bundle %s: %w", state.BundleID, err)
	}
	summary := response.Msg.GetBundle().GetSummary()
	if summary == nil {
		return fmt.Errorf("bundle %s has no summary", state.BundleID)
	}
	if summary.GetName() != bundleName || summary.GetChartDigest() != chartDigest {
		return fmt.Errorf("bundle %s identity mismatch (name %q, chart digest %q)", state.BundleID, summary.GetName(), summary.GetChartDigest())
	}
	return nil
}
