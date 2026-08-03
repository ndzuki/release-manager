// Package authorization implements the cross-service Authorization Snapshot consumer.
package authorization

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the stable REQ-027 authorization instruments.
type Metrics struct {
	Decisions           *prometheus.CounterVec
	SnapshotStale       prometheus.Counter
	SourceVersion       prometheus.Gauge
	CheckpointVersion   prometheus.Gauge
	PolicyHealth        prometheus.Gauge
	EnforceDuration     prometheus.Histogram
	SnapshotRPCDuration prometheus.Histogram
	gatherer            prometheus.Gatherer
}

// NewMetrics registers authorization metrics with an isolated registry.
func NewMetrics(registry *prometheus.Registry) *Metrics {
	if registry == nil {
		registry = prometheus.NewRegistry()
	}
	metrics := &Metrics{
		Decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "auth_decisions_total",
			Help: "Authorization decisions by bounded result and actor type.",
		}, []string{"result", "actor_type"}),
		SnapshotStale: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "auth_snapshot_stale_total",
			Help: "Authorization requests rejected because the local snapshot was stale.",
		}),
		SourceVersion: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "auth_source_version",
			Help: "Latest authorization source version observed by this process.",
		}),
		CheckpointVersion: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "auth_checkpoint_version",
			Help: "Latest authorization checkpoint version persisted by this process.",
		}),
		PolicyHealth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "auth_policy_health",
			Help: "Whether the current authorization policy is healthy (1) or unavailable (0).",
		}),
		EnforceDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "auth_enforce_duration_seconds",
			Help:    "Local authorization enforcement latency.",
			Buckets: prometheus.ExponentialBuckets(0.00005, 2, 12),
		}),
		SnapshotRPCDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "auth_snapshot_rpc_duration_seconds",
			Help:    "Authorization Snapshot RPC latency.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12),
		}),
		gatherer: registry,
	}
	registry.MustRegister(
		metrics.Decisions,
		metrics.SnapshotStale,
		metrics.SourceVersion,
		metrics.CheckpointVersion,
		metrics.PolicyHealth,
		metrics.EnforceDuration,
		metrics.SnapshotRPCDuration,
	)
	return metrics
}

// Handler exposes the registry through the service's existing HTTP mux.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}
