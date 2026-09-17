package notifier

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
)

// EgressPolicy is the outbound allowlist for webhook delivery (REQ-031,
// TASK-096). A destination is reachable only when its scheme, host and
// effective port are all listed.
//
// The zero value (and an empty list) denies everything: outbound destinations
// must be opted in by configuration, so neither a mistaken recipient nor a
// compromised caller can turn the control plane into an HTTP client for
// arbitrary URLs — the classic SSRF surface (169.254.169.254, 127.0.0.1/::1 and
// RFC1918 ranges are denied by default because they are simply not listed).
type EgressPolicy struct {
	allowed map[string]struct{}
}

// NewEgressPolicy parses "scheme://host[:port]" entries into an allowlist. A
// malformed entry is a configuration error and fails startup rather than
// silently shrinking the list.
func NewEgressPolicy(entries []string) (*EgressPolicy, error) {
	policy := &EgressPolicy{allowed: make(map[string]struct{}, len(entries))}
	for _, entry := range entries {
		key, err := egressKey(entry)
		if err != nil {
			return nil, fmt.Errorf("egress allowlist entry %q: %w", entry, err)
		}
		policy.allowed[key] = struct{}{}
	}
	return policy, nil
}

// Allow reports whether target is reachable, with a stable denial reason
// (recorded on the notification job and in the log, never containing a secret).
func (p *EgressPolicy) Allow(target string) (allowed bool, reason string) {
	key, err := egressKey(target)
	if err != nil {
		return false, ReasonEgressInvalidTarget
	}
	if p == nil || len(p.allowed) == 0 {
		return false, ReasonEgressNotAllowlisted
	}
	if _, ok := p.allowed[key]; !ok {
		return false, ReasonEgressNotAllowlisted
	}
	return true, ""
}

// EgressRules returns the normalized allowlist (for logs and diagnostics); the
// values are destinations, never credentials.
func (p *EgressPolicy) EgressRules() []string {
	if p == nil {
		return nil
	}
	rules := make([]string, 0, len(p.allowed))
	for key := range p.allowed {
		rules = append(rules, key)
	}
	return rules
}

// egressKey normalizes a URL to "scheme://host:port" using the effective port
// (443 for https, 80 for http) so an allowlist entry and a request that spell
// the same destination differently still match.
func egressKey(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q (only http and https are deliverable)", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("missing host")
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// EgressMetrics counts outbound admission decisions. The notifier has no
// Prometheus scrape surface (pre-existing), so the counters are exposed as a
// snapshot, mirroring the audit emitter's metrics convention in this repository.
type EgressMetrics struct {
	allowed atomic.Uint64
	denied  atomic.Uint64
}

// EgressMetricsSnapshot is a point-in-time copy of the counters.
type EgressMetricsSnapshot struct {
	Allowed uint64 `json:"allowed"`
	Denied  uint64 `json:"denied"`
}

// Snapshot returns the current counters.
func (m *EgressMetrics) Snapshot() EgressMetricsSnapshot {
	if m == nil {
		return EgressMetricsSnapshot{}
	}
	return EgressMetricsSnapshot{Allowed: m.allowed.Load(), Denied: m.denied.Load()}
}

func (m *EgressMetrics) recordAllowed() {
	if m != nil {
		m.allowed.Add(1)
	}
}

func (m *EgressMetrics) recordDenied() {
	if m != nil {
		m.denied.Add(1)
	}
}

// Stable denial reasons (REQ-031/TASK-096). They are part of the delivery
// record the console reads, so they must not change casually.
const (
	// ReasonEgressNotAllowlisted is the default-deny outcome: the destination is
	// well formed but absent from the configured allowlist.
	ReasonEgressNotAllowlisted = "egress_not_allowlisted"
	// ReasonEgressInvalidTarget is a malformed URL or an undeliverable scheme.
	ReasonEgressInvalidTarget = "egress_invalid_target"
)
