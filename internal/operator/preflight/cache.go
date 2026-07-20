package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Cache provides idempotent dry-run results keyed by (render_digest,
// capability_version). When the capability version changes, cached entries
// are automatically invalidated.
type Cache struct {
	mu       sync.RWMutex
	entries  map[cacheKey]*cacheEntry
	latestCV string
}

type cacheKey struct {
	RenderDigest      string
	CapabilityVersion string
}

type cacheEntry struct {
	result    *BatchResult
	createdAt time.Time
}

// NewCache creates an initially empty cache.
func NewCache() *Cache {
	return &Cache{
		entries: make(map[cacheKey]*cacheEntry),
	}
}

// Get returns a cached batch result if one exists and the capability version
// matches the latest known version, or nil and false.
func (c *Cache) Get(renderDigest, capabilityVersion string) (*BatchResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.latestCV != "" && capabilityVersion != c.latestCV {
		return nil, false
	}

	key := cacheKey{RenderDigest: renderDigest, CapabilityVersion: capabilityVersion}
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return entry.result, true
}

// Put stores a batch result. If the capability version differs from the
// one currently tracked, the cache is cleared before inserting.
func (c *Cache) Put(result *BatchResult, capabilityVersion string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if capabilityVersion != c.latestCV {
		c.entries = make(map[cacheKey]*cacheEntry)
		c.latestCV = capabilityVersion
	}

	key := cacheKey{
		RenderDigest:      result.RenderDigest,
		CapabilityVersion: capabilityVersion,
	}
	c.entries[key] = &cacheEntry{
		result:    result,
		createdAt: time.Now(),
	}
}

// Invalidate removes all cached entries.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[cacheKey]*cacheEntry)
	c.latestCV = ""
}

// ClientFactory creates a new dynamic.Interface from a rest.Config.
type ClientFactory func(cfg *rest.Config) (dynamic.Interface, error)

// DefaultClientFactory creates a dynamic client from a rest config.
func DefaultClientFactory(cfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(cfg)
}

// Digest computes a SHA-256 hex digest of the given input's manifest stream
// for use as a render digest cache key.
func Digest(manifestStream []byte) string {
	if len(manifestStream) == 0 {
		return ""
	}
	h := sha256.Sum256(manifestStream)
	return hex.EncodeToString(h[:])
}

// ResultJSON encodes a BatchResult as JSON, suitable for storage in
// localstore CommandEntry.ResultJSON. The encoding strips zero-value fields.
func ResultJSON(result *BatchResult) (string, error) {
	if result == nil {
		return "", nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Gate determines whether an operation may proceed to Helm execution.
// It returns true only when the preflight batch passed (every resource accepted).
func Gate(result *BatchResult) bool {
	if result == nil {
		return false
	}
	return result.Passed
}

// Orchestrator ties together the full preflight lifecycle:
//  1. Decode manifest stream
//  2. Map GVKs to GVRs
//  3. Execute dry-run batch
//  4. Cache results
//  5. Return preflight outcome
type Orchestrator struct {
	mapper   *GKVMapper
	executor *DryRunExecutor
	cache    *Cache
	clientFn ClientFactory
	restCfg  *rest.Config
}

// NewOrchestrator creates a preflight orchestrator bound to a
// Kubernetes API server.
func NewOrchestrator(cfg *rest.Config) (*Orchestrator, error) {
	clientF := DefaultClientFactory
	dynClient, err := clientF(cfg)
	if err != nil {
		return nil, err
	}

	mapper, err := NewGKVMapper(cfg, dynClient)
	if err != nil {
		return nil, err
	}

	return &Orchestrator{
		mapper:   mapper,
		executor: NewDryRunExecutor(mapper),
		cache:    NewCache(),
		clientFn: clientF,
		restCfg:  cfg,
	}, nil
}

// NewOrchestratorWithCache is used by tests to inject a pre-built
// mapper and cache.
func NewOrchestratorWithCache(
	mapper *GKVMapper,
	cache *Cache,
) *Orchestrator {
	return &Orchestrator{
		mapper:   mapper,
		executor: NewDryRunExecutor(mapper),
		cache:    cache,
	}
}

// Run executes the full preflight pipeline: decode → dry-run → cache.
// If a cached result exists for the same (render_digest, capability_version),
// it is returned without accessing the API server.
func (o *Orchestrator) Run(
	ctx context.Context,
	input Input,
) (*BatchResult, error) {
	o.executor.SetTimeout(DefaultResourceTimeout)

	// Check cache.
	if cached, ok := o.cache.Get(input.RenderDigest, input.CapabilityVersion); ok {
		return cached, nil
	}

	// Decode.
	resources, err := DecodeManifestStream(input.ManifestStream)
	if err != nil {
		return nil, err
	}

	// Execute.
	result, err := o.executor.DryRunAll(ctx, resources, input)
	if err != nil {
		return result, err
	}

	// Cache successful results (cache misses are safe: just re-execute).
	o.cache.Put(result, input.CapabilityVersion)

	return result, nil
}

// Cache returns the orchestrator's cache for external inspection/tests.
func (o *Orchestrator) Cache() *Cache {
	return o.cache
}
