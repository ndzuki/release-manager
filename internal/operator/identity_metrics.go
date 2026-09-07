package operator

import (
	"github.com/prometheus/client_golang/prometheus"
)

// IdentityMetrics owns the REQ-088 workload identity convergence instruments
// (D7=A, ADR-016 low-cardinality counters). The counters make the buffered →
// bound → dropped → conflict → purged lifecycle observable for the AC-088-04
// contract tests and the AC-066-17 final combination smoke. Registering with
// the same registry as authorization.NewMetrics lets the shared /metrics
// endpoint expose both instrument families through one gatherer.
type IdentityMetrics struct {
	// ReportBuffered counts identity reports buffered into
	// pending_workload_identity because their inventory row did not exist yet.
	ReportBuffered prometheus.Counter
	// BoundAfterInventory counts buffered identities bound to an inventory row
	// after that row appeared (event-driven replay or periodic sweep).
	BoundAfterInventory prometheus.Counter
	// Conflict counts D4=C conflicts where a reported identity disagreed with
	// the existing row (kind/name/namespace mismatch) and the existing
	// identity was kept (fail closed).
	Conflict prometheus.Counter
	// ReportDropped counts report items dropped because no selectable
	// four-tuple could be determined (ambiguous, incomplete, or no matching
	// definition) — fail closed, never bound.
	ReportDropped prometheus.Counter
	// PendingPurged counts orphaned pending rows removed by the TTL sweep
	// (no inventory row materialized within the TTL).
	PendingPurged prometheus.Counter
}

// NewIdentityMetrics registers the REQ-088 instruments. A nil registry creates
// an isolated one so callers that do not expose /metrics keep working with a
// no-op cost.
func NewIdentityMetrics(registry *prometheus.Registry) *IdentityMetrics {
	if registry == nil {
		registry = prometheus.NewRegistry()
	}
	metrics := &IdentityMetrics{
		ReportBuffered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "identity_report_buffered_total",
			Help: "Identity reports buffered because their release_inventory row did not exist yet.",
		}),
		BoundAfterInventory: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "identity_bound_after_inventory_total",
			Help: "Buffered workload identities bound to an inventory row after the row appeared.",
		}),
		Conflict: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "identity_conflict_total",
			Help: "Identity reports conflicting with the existing row identity (existing identity kept).",
		}),
		ReportDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "identity_report_dropped_total",
			Help: "Identity report items dropped because no selectable four-tuple could be determined.",
		}),
		PendingPurged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "identity_pending_purged_total",
			Help: "Orphaned pending workload identities purged by the TTL sweep.",
		}),
	}
	registry.MustRegister(
		metrics.ReportBuffered,
		metrics.BoundAfterInventory,
		metrics.Conflict,
		metrics.ReportDropped,
		metrics.PendingPurged,
	)
	return metrics
}
