// Package metrics provides the Prometheus instrumentation surface for
// filegate: an HTTP RED middleware shared by the REST + S3 adapters,
// a set of counters the background loops feed, a scrape-time domain
// collector, and the /metrics HTTP handler.
//
// Design notes:
//
//   - Instance-based, not package-global. A Registry owns its own
//     *prometheus.Registry and every metric. Tests get full isolation
//     by constructing their own Registry; there is no shared global
//     state to reset.
//
//   - Cardinality discipline. Labels are bounded sets only —
//     adapter (rest|s3), op (a bounded S3-op set or an HTTP method),
//     status_class (2xx/3xx/4xx/5xx), mount name, reason, phase. No
//     path/key/access-key labels (those are unbounded and would blow
//     up the time-series database).
//
//   - The Registry is always cheap to construct. The caller (cli)
//     always builds one so the background-loop counter handles are
//     valid from boot; only the /metrics endpoint and the per-request
//     middleware wrapping are gated on metrics.enabled. An unscraped
//     registry costs a few KB and is otherwise inert.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// byteBuckets spans request/response body sizes from 1 KiB to 1 GiB.
// File-gateway payloads cluster in the KiB–MiB range with a long tail
// up to multipart-sized objects; these buckets give useful p50/p95
// without an explosion of bucket series.
var byteBuckets = []float64{
	1 << 10,  // 1 KiB
	4 << 10,  // 4 KiB
	64 << 10, // 64 KiB
	1 << 20,  // 1 MiB
	8 << 20,  // 8 MiB
	64 << 20, // 64 MiB
	256 << 20,
	1 << 30, // 1 GiB
}

// phaseBuckets spans the multipart-Complete sub-phase durations. The
// hash + concat phases can run into seconds on large objects; the
// lock_wait + pebble_batch phases are usually sub-millisecond. A wide
// span with fine low-end granularity covers both.
var phaseBuckets = []float64{
	0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Registry bundles the filegate metric set with its backing Prometheus
// registry. Construct one with New; pass it (or its handles) to the
// adapters and background loops.
type Registry struct {
	reg *prometheus.Registry

	// HTTP RED.
	httpRequests   *prometheus.CounterVec   // {adapter,op,status_class}
	httpDuration   *prometheus.HistogramVec // {adapter,op}
	httpInFlight   *prometheus.GaugeVec     // {adapter}
	httpReqBytes   *prometheus.HistogramVec // {adapter,op}
	httpRespBytes  *prometheus.HistogramVec // {adapter,op}

	// Background-loop + rate-limit counters.
	cleanupRetired *prometheus.CounterVec // {reason}
	cleanupErrors  prometheus.Counter
	pruneDeleted   prometheus.Counter
	pruneKept      prometheus.Counter
	pruneErrors    prometheus.Counter
	detectorEvents *prometheus.CounterVec // {type}
	ratelimitRejected prometheus.Counter

	// Hot-path phase histogram.
	completePhase *prometheus.HistogramVec // {phase}

	// Build info gauge (value always 1, info carried in labels).
	buildInfo *prometheus.GaugeVec // {version,commit}
}

// BuildInfo identifies the running binary. Set from ldflags-injected
// vars in main; defaults to "dev"/"none" so unset builds still emit a
// well-formed series.
type BuildInfo struct {
	Version string
	Commit  string
}

// New constructs a Registry with every metric registered, plus the
// Go-runtime and process collectors (goroutines, GC, heap, open file
// descriptors, CPU, RSS — all free and high-value for a file gateway).
// statsProvider may be nil; when set, a scrape-time domain collector
// is registered that turns the provider's snapshot into gauges.
func New(build BuildInfo, statsProvider StatsProvider) *Registry {
	reg := prometheus.NewRegistry()
	r := &Registry{reg: reg}

	r.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "filegate_http_requests_total",
		Help: "Total HTTP requests, by adapter, operation, and status class.",
	}, []string{"adapter", "op", "status_class"})
	r.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "filegate_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds, by adapter and operation.",
		Buckets: prometheus.DefBuckets,
	}, []string{"adapter", "op"})
	r.httpInFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "filegate_http_requests_in_flight",
		Help: "In-flight HTTP requests, by adapter.",
	}, []string{"adapter"})
	r.httpReqBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "filegate_http_request_size_bytes",
		Help:    "HTTP request body size in bytes, by adapter and operation.",
		Buckets: byteBuckets,
	}, []string{"adapter", "op"})
	r.httpRespBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "filegate_http_response_size_bytes",
		Help:    "HTTP response body size in bytes, by adapter and operation.",
		Buckets: byteBuckets,
	}, []string{"adapter", "op"})

	r.cleanupRetired = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "filegate_multipart_cleanup_retired_total",
		Help: "Multipart staging dirs retired by the cleanup loop, by reason.",
	}, []string{"reason"})
	r.cleanupErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filegate_multipart_cleanup_errors_total",
		Help: "Errors encountered by the multipart cleanup loop.",
	})
	r.pruneDeleted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filegate_version_prune_deleted_total",
		Help: "Versions deleted by the version pruner.",
	})
	r.pruneKept = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filegate_version_prune_kept_total",
		Help: "Versions kept by the version pruner.",
	})
	r.pruneErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filegate_version_prune_errors_total",
		Help: "Errors encountered by the version pruner.",
	})
	r.detectorEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "filegate_detector_events_total",
		Help: "Filesystem detector events observed, by type.",
	}, []string{"type"})
	r.ratelimitRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filegate_s3_ratelimit_rejected_total",
		Help: "S3 requests rejected with 503 SlowDown by the per-key rate limiter.",
	})

	r.completePhase = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "filegate_multipart_complete_phase_seconds",
		Help:    "Multipart CompleteMultipartUpload sub-phase duration in seconds.",
		Buckets: phaseBuckets,
	}, []string{"phase"})

	r.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "filegate_build_info",
		Help: "Build information; value is always 1, details in labels.",
	}, []string{"version", "commit"})
	version, commit := build.Version, build.Commit
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "none"
	}
	r.buildInfo.WithLabelValues(version, commit).Set(1)

	reg.MustRegister(
		r.httpRequests, r.httpDuration, r.httpInFlight, r.httpReqBytes, r.httpRespBytes,
		r.cleanupRetired, r.cleanupErrors, r.pruneDeleted, r.pruneKept, r.pruneErrors,
		r.detectorEvents, r.ratelimitRejected, r.completePhase, r.buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	if statsProvider != nil {
		reg.MustRegister(newDomainCollector(statsProvider))
	}
	return r
}

// Handler returns the /metrics HTTP handler scraping this registry.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

// --- Background-loop + rate-limit counter accessors -----------------
// All accessors are nil-receiver safe: callers (background loops, the
// S3 router, the Complete handler) can hold a possibly-nil *Registry
// and call unconditionally without a guard. In production the registry
// is always non-nil; the nil path exists so tests (and any future
// metrics-off wiring) need no special-casing.

// CleanupRetired records a retired multipart staging dir by reason
// ("done", "aborted", "stuck"). n may be >1 for a batch.
func (r *Registry) CleanupRetired(reason string, n int) {
	if r == nil || n <= 0 {
		return
	}
	r.cleanupRetired.WithLabelValues(reason).Add(float64(n))
}

// CleanupErrors records cleanup-loop errors.
func (r *Registry) CleanupErrors(n int) {
	if r == nil || n <= 0 {
		return
	}
	r.cleanupErrors.Add(float64(n))
}

// PruneStats records a version-pruner pass.
func (r *Registry) PruneStats(deleted, kept, errs int) {
	if r == nil {
		return
	}
	if deleted > 0 {
		r.pruneDeleted.Add(float64(deleted))
	}
	if kept > 0 {
		r.pruneKept.Add(float64(kept))
	}
	if errs > 0 {
		r.pruneErrors.Add(float64(errs))
	}
}

// DetectorEvents records n filesystem-detector events of a type
// ("created", "changed", "deleted", "unknown").
func (r *Registry) DetectorEvents(eventType string, n int) {
	if r == nil || n <= 0 {
		return
	}
	r.detectorEvents.WithLabelValues(eventType).Add(float64(n))
}

// RatelimitRejected records one 503-SlowDown rejection.
func (r *Registry) RatelimitRejected() {
	if r == nil {
		return
	}
	r.ratelimitRejected.Inc()
}

// ObserveCompletePhase records a multipart-Complete sub-phase duration
// in seconds. phase is one of "concat", "lock_wait", "hash",
// "pebble_batch".
func (r *Registry) ObserveCompletePhase(phase string, seconds float64) {
	if r == nil {
		return
	}
	r.completePhase.WithLabelValues(phase).Observe(seconds)
}
