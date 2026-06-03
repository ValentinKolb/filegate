package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestStatusClass(t *testing.T) {
	cases := map[int]string{
		100: "1xx", 200: "2xx", 204: "2xx", 301: "3xx", 304: "3xx",
		400: "4xx", 404: "4xx", 412: "4xx", 500: "5xx", 503: "5xx",
		0: "other", 600: "other",
	}
	for code, want := range cases {
		if got := statusClass(code); got != want {
			t.Errorf("statusClass(%d)=%q, want %q", code, got, want)
		}
	}
}

func TestMiddlewareRecordsRequestAndStatusClass(t *testing.T) {
	r := New(BuildInfo{}, nil)
	h := r.Middleware("rest")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// requests_total{adapter=rest,op=GET,status_class=4xx} == 1
	got := testutil.ToFloat64(r.httpRequests.WithLabelValues("rest", "GET", "4xx"))
	if got != 1 {
		t.Errorf("requests_total{rest,GET,4xx}=%v, want 1", got)
	}
	// response bytes observed (4 bytes "nope") — histogram count == 1
	if c := testutil.CollectAndCount(r.httpRespBytes); c == 0 {
		t.Errorf("response-size histogram has no observations")
	}
}

func TestMiddlewareInFlightReturnsToZero(t *testing.T) {
	r := New(BuildInfo{}, nil)
	h := r.Middleware("s3")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Assert the gauge is 1 *during* the request.
		if v := testutil.ToFloat64(r.httpInFlight.WithLabelValues("s3")); v != 1 {
			t.Errorf("in_flight during request=%v, want 1", v)
		}
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if v := testutil.ToFloat64(r.httpInFlight.WithLabelValues("s3")); v != 0 {
		t.Errorf("in_flight after request=%v, want 0", v)
	}
}

func TestMiddlewareInFlightDecrementsOnPanic(t *testing.T) {
	r := New(BuildInfo{}, nil)
	h := r.Middleware("s3")(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))
	defer func() {
		_ = recover() // swallow the re-panic
		if v := testutil.ToFloat64(r.httpInFlight.WithLabelValues("s3")); v != 0 {
			t.Errorf("in_flight after panic=%v, want 0 (defer must dec)", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestMiddlewareOpFromContext(t *testing.T) {
	r := New(BuildInfo{}, nil)
	h := r.Middleware("s3")(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Simulate the S3 dispatcher labelling the op. Note: no
		// request replacement needed — SetOp mutates the holder the
		// middleware installed.
		SetOp(req.Context(), "PutObject")
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/b/k", nil))

	if got := testutil.ToFloat64(r.httpRequests.WithLabelValues("s3", "PutObject", "2xx")); got != 1 {
		t.Errorf("requests_total{s3,PutObject,2xx}=%v, want 1 (op-context not honoured)", got)
	}
}

// testChildKey is a typed context key for the derived-context test
// (avoids the SA1029 empty-anonymous-struct-as-key warning).
type testChildKey struct{}

func TestMiddlewareSetOpFromDerivedContext(t *testing.T) {
	// SetOp must work even when the handler derives a child context —
	// the pointer holder is reachable from descendants. This is the
	// real S3 case (dispatch wraps the request).
	r := New(BuildInfo{}, nil)
	h := r.Middleware("s3")(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		child := req.WithContext(context.WithValue(req.Context(), testChildKey{}, "x"))
		SetOp(child.Context(), "CompleteMultipartUpload")
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/b/k", nil))
	if got := testutil.ToFloat64(r.httpRequests.WithLabelValues("s3", "CompleteMultipartUpload", "2xx")); got != 1 {
		t.Errorf("SetOp from derived context not honoured")
	}
}

func TestMiddlewareOpDefaultsToMethod(t *testing.T) {
	r := New(BuildInfo{}, nil)
	h := r.Middleware("rest")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/", nil))
	if got := testutil.ToFloat64(r.httpRequests.WithLabelValues("rest", "DELETE", "2xx")); got != 1 {
		t.Errorf("op did not default to method DELETE")
	}
}

func TestBuildInfoEmitted(t *testing.T) {
	r := New(BuildInfo{Version: "1.2.3", Commit: "abc123"}, nil)
	want := `
# HELP filegate_build_info Build information; value is always 1, details in labels.
# TYPE filegate_build_info gauge
filegate_build_info{commit="abc123",version="1.2.3"} 1
`
	if err := testutil.CollectAndCompare(r.buildInfo, strings.NewReader(want)); err != nil {
		t.Errorf("build_info mismatch: %v", err)
	}
}

func TestBuildInfoDefaults(t *testing.T) {
	r := New(BuildInfo{}, nil)
	if got := testutil.ToFloat64(r.buildInfo.WithLabelValues("dev", "none")); got != 1 {
		t.Errorf("build_info default labels (dev/none) not set, got %v", got)
	}
}

func TestCounterAccessors(t *testing.T) {
	r := New(BuildInfo{}, nil)
	r.CleanupRetired("done", 3)
	r.CleanupRetired("stuck", 1)
	r.CleanupRetired("done", 0) // no-op
	r.CleanupErrors(2)
	r.PruneStats(5, 10, 1)
	r.DetectorEvents("created", 4)
	r.RatelimitRejected()
	r.RatelimitRejected()
	r.ObserveCompletePhase("hash", 0.123)

	if v := testutil.ToFloat64(r.cleanupRetired.WithLabelValues("done")); v != 3 {
		t.Errorf("cleanup_retired{done}=%v, want 3", v)
	}
	if v := testutil.ToFloat64(r.cleanupRetired.WithLabelValues("stuck")); v != 1 {
		t.Errorf("cleanup_retired{stuck}=%v, want 1", v)
	}
	if v := testutil.ToFloat64(r.cleanupErrors); v != 2 {
		t.Errorf("cleanup_errors=%v, want 2", v)
	}
	if v := testutil.ToFloat64(r.pruneDeleted); v != 5 {
		t.Errorf("prune_deleted=%v, want 5", v)
	}
	if v := testutil.ToFloat64(r.pruneKept); v != 10 {
		t.Errorf("prune_kept=%v, want 10", v)
	}
	if v := testutil.ToFloat64(r.detectorEvents.WithLabelValues("created")); v != 4 {
		t.Errorf("detector_events{created}=%v, want 4", v)
	}
	if v := testutil.ToFloat64(r.ratelimitRejected); v != 2 {
		t.Errorf("ratelimit_rejected=%v, want 2", v)
	}
	if c := testutil.CollectAndCount(r.completePhase); c == 0 {
		t.Errorf("complete_phase histogram has no observations")
	}
}

// fakeStatsProvider returns a fixed snapshot for collector tests.
type fakeStatsProvider struct {
	snap Snapshot
	err  error
}

func (f fakeStatsProvider) MetricsSnapshot() (Snapshot, error) { return f.snap, f.err }

func TestDomainCollectorEmitsGauges(t *testing.T) {
	p := fakeStatsProvider{snap: Snapshot{
		Files:            100,
		Dirs:             20,
		PathCacheEntries: 7,
		IndexDBBytes:     4096,
		Mounts: []MountSnapshot{
			{Name: "photos", UsedBytes: 1000, FreeBytes: 9000},
		},
	}}
	c := newDomainCollector(p)
	want := `
# HELP filegate_index_entities Indexed entities by type (files, dirs).
# TYPE filegate_index_entities gauge
filegate_index_entities{type="files"} 100
filegate_index_entities{type="dirs"} 20
# HELP filegate_index_db_bytes On-disk size of the Pebble index directory in bytes.
# TYPE filegate_index_db_bytes gauge
filegate_index_db_bytes 4096
# HELP filegate_path_cache_entries Current entries in the path resolution cache.
# TYPE filegate_path_cache_entries gauge
filegate_path_cache_entries 7
# HELP filegate_mount_used_bytes Used bytes on the filesystem backing a mount.
# TYPE filegate_mount_used_bytes gauge
filegate_mount_used_bytes{mount="photos"} 1000
# HELP filegate_mount_free_bytes Free bytes on the filesystem backing a mount.
# TYPE filegate_mount_free_bytes gauge
filegate_mount_free_bytes{mount="photos"} 9000
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Errorf("domain collector mismatch: %v", err)
	}
}

func TestDomainCollectorSkipsOnProviderError(t *testing.T) {
	c := newDomainCollector(fakeStatsProvider{err: http.ErrBodyNotAllowed})
	// CollectAndCount should be 0 — provider error → no domain samples.
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Errorf("collector emitted %d samples on provider error, want 0", n)
	}
}

func TestHandlerServesMetrics(t *testing.T) {
	r := New(BuildInfo{Version: "test"}, nil)
	r.RatelimitRejected()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"filegate_build_info",
		"filegate_s3_ratelimit_rejected_total 1",
		"go_goroutines",       // Go collector
		"process_open_fds",    // process collector — the FD-leak signal
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body missing %q", want)
		}
	}
}
