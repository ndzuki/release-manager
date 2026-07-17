package preflight

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ndzuki/release-manager/internal/store"
)

const maxRegistryErrorBody = 4 << 10

// OCIResolver reads manifest digests from OCI Distribution-compatible registries.
type OCIResolver struct {
	client *http.Client
}

// NewOCIResolver creates a resolver backed by the supplied HTTP client.
func NewOCIResolver(client *http.Client) *OCIResolver {
	if client == nil {
		client = http.DefaultClient
	}
	return &OCIResolver{client: client}
}

// ResolveDigest returns the digest advertised by the routed registry manifest.
func (r *OCIResolver) ResolveDigest(
	ctx context.Context,
	_ store.ArtifactType,
	targetURI string,
) (string, error) {
	manifestURL, err := manifestRequestURL(targetURI)
	if err != nil {
		return "", fmt.Errorf("resolve manifest url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("create manifest request: %w", err)
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: query manifest: %v", ErrDependencyUnavailable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
		if digest == "" {
			return "", fmt.Errorf("%w: registry omitted Docker-Content-Digest", ErrDependencyUnavailable)
		}
		return digest, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrRegistryUnauthorized
	case http.StatusNotFound:
		return "", ErrArtifactNotFound
	default:
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRegistryErrorBody))
		if readErr != nil {
			return "", fmt.Errorf("%w: registry returned %s", ErrDependencyUnavailable, resp.Status)
		}
		return "", fmt.Errorf("%w: registry returned %s: %s", ErrDependencyUnavailable, resp.Status, strings.TrimSpace(string(body)))
	}
}

func manifestRequestURL(targetURI string) (string, error) {
	parsed, err := url.Parse(targetURI)
	if err != nil {
		return "", fmt.Errorf("parse target uri: %w", err)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("target uri must not contain userinfo")
	}

	switch parsed.Scheme {
	case "oci":
		parsed.Scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported target uri scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("target uri host is required")
	}

	repository, reference, err := splitReference(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil {
		return "", err
	}
	parsed.Path = "/v2/" + repository + "/manifests/" + url.PathEscape(reference)
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func splitReference(ref string) (repository, reference string, err error) {
	if digestAt := strings.LastIndex(ref, "@"); digestAt >= 0 {
		repository = ref[:digestAt]
		reference = ref[digestAt+1:]
	} else {
		lastSlash := strings.LastIndex(ref, "/")
		tagAt := strings.LastIndex(ref, ":")
		if tagAt <= lastSlash {
			return "", "", fmt.Errorf("target uri must include a tag or digest")
		}
		repository = ref[:tagAt]
		reference = ref[tagAt+1:]
	}
	if repository == "" || reference == "" {
		return "", "", fmt.Errorf("target uri must include repository and reference")
	}
	return repository, reference, nil
}
