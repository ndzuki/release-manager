package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"k8s.io/client-go/discovery"
)

// CapabilityVersioner derives the cluster capability version the dry-run cache
// invalidates against (ADR-026 V1).
type CapabilityVersioner interface {
	CapabilityVersion(ctx context.Context) (string, error)
}

// DiscoveryCapabilityVersioner derives the version from live discovery: the
// server version plus the served group/version/resource set.
//
// A CRD being added or removed changes the result, which is exactly what must
// invalidate a cached dry-run. The 2026-09 audit found the opposite behaviour:
// with no version produced anywhere, the cache replayed a "passed" after the
// cluster's capabilities had changed.
type DiscoveryCapabilityVersioner struct {
	client discovery.DiscoveryInterface
}

// NewDiscoveryCapabilityVersioner builds the probe over an existing discovery
// client.
func NewDiscoveryCapabilityVersioner(client discovery.DiscoveryInterface) *DiscoveryCapabilityVersioner {
	return &DiscoveryCapabilityVersioner{client: client}
}

// CapabilityVersion returns a stable hash of the server version and the served
// resources.
func (v *DiscoveryCapabilityVersioner) CapabilityVersion(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if v == nil || v.client == nil {
		return "", fmt.Errorf("capability version: discovery client is required")
	}
	version, err := v.client.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("capability version: server version: %w", err)
	}
	// ServerGroupsAndResources reports partial failure when one aggregated API
	// is unhealthy; the lists that did resolve still describe the cluster, so a
	// non-nil result is used rather than failing the stage.
	// This client-go version returns (groups, resourceLists, err) -- groups
	// first, unlike the upstream doc's ordering. Verified with `go doc`.
	_, resourceLists, listErr := v.client.ServerGroupsAndResources()
	if listErr != nil && resourceLists == nil {
		return "", fmt.Errorf("capability version: server resources: %w", listErr)
	}
	resources := make([]string, 0, 256)
	for _, list := range resourceLists {
		if list == nil {
			continue
		}
		for i := range list.APIResources {
			resource := &list.APIResources[i]
			resources = append(resources, list.GroupVersion+"/"+resource.Name)
		}
	}
	sort.Strings(resources)
	sum := sha256.Sum256([]byte(version.GitVersion + "|" + strings.Join(resources, ",")))
	return hex.EncodeToString(sum[:]), nil
}
