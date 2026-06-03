//go:build linux

package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubMetricsHandler is a trivial handler standing in for the real
// promhttp handler — these tests exercise the auth wrapping + route
// mount, not the metric rendering.
func stubMetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# metrics\n"))
	})
}

func newMetricsRouter(t *testing.T, opts RouterOptions) http.Handler {
	t.Helper()
	r, _, cleanup := newTestRouterWithCustomLimits(t, t.TempDir(), t.TempDir(), opts)
	t.Cleanup(cleanup)
	return r
}

func getMetrics(t *testing.T, h http.Handler, token string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestMetricsAuthMetricsTokenTakesPrecedence: when metrics.token is
// set, it is required — the REST bearer token does NOT grant access.
func TestMetricsAuthMetricsTokenTakesPrecedence(t *testing.T) {
	h := newMetricsRouter(t, RouterOptions{
		BearerToken:    "rest-bearer",
		MetricsHandler: stubMetricsHandler(),
		MetricsPath:    "/metrics",
		MetricsToken:   "metrics-secret",
	})
	if code := getMetrics(t, h, "metrics-secret"); code != http.StatusOK {
		t.Errorf("metrics token request status=%d, want 200", code)
	}
	if code := getMetrics(t, h, "rest-bearer"); code != http.StatusUnauthorized {
		t.Errorf("rest bearer against metrics-token-protected endpoint status=%d, want 401", code)
	}
	if code := getMetrics(t, h, ""); code != http.StatusUnauthorized {
		t.Errorf("no-token request status=%d, want 401", code)
	}
}

// TestMetricsAuthFallsBackToBearer: when metrics.token is empty but a
// REST bearer is configured, the bearer guards /metrics.
func TestMetricsAuthFallsBackToBearer(t *testing.T) {
	h := newMetricsRouter(t, RouterOptions{
		BearerToken:    "rest-bearer",
		MetricsHandler: stubMetricsHandler(),
		MetricsPath:    "/metrics",
		MetricsToken:   "",
	})
	if code := getMetrics(t, h, "rest-bearer"); code != http.StatusOK {
		t.Errorf("rest bearer fallback status=%d, want 200", code)
	}
	if code := getMetrics(t, h, ""); code != http.StatusUnauthorized {
		t.Errorf("no-token request status=%d, want 401", code)
	}
	if code := getMetrics(t, h, "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token status=%d, want 401", code)
	}
}

// TestMetricsAuthOpenWhenNoToken: both tokens empty → open endpoint.
func TestMetricsAuthOpenWhenNoToken(t *testing.T) {
	h := newMetricsRouter(t, RouterOptions{
		BearerToken:    "",
		MetricsHandler: stubMetricsHandler(),
		MetricsPath:    "/metrics",
		MetricsToken:   "",
	})
	if code := getMetrics(t, h, ""); code != http.StatusOK {
		t.Errorf("open endpoint no-token status=%d, want 200", code)
	}
}

// TestMetricsNotMountedWhenHandlerNil: no MetricsHandler → /metrics is
// a normal 404 (not registered).
func TestMetricsNotMountedWhenHandlerNil(t *testing.T) {
	h := newMetricsRouter(t, RouterOptions{
		BearerToken:    "rest-bearer",
		MetricsHandler: nil,
	})
	if code := getMetrics(t, h, "rest-bearer"); code != http.StatusNotFound {
		t.Errorf("disabled metrics endpoint status=%d, want 404", code)
	}
}

func TestValidateMetricsPath(t *testing.T) {
	good := []string{"/metrics", "/internal/metrics", "/m"}
	for _, p := range good {
		if err := ValidateMetricsPath(p); err != nil {
			t.Errorf("ValidateMetricsPath(%q)=%v, want nil", p, err)
		}
	}
	bad := []string{"", "metrics", "/health", "/v1", "/v1/metrics"}
	for _, p := range bad {
		if err := ValidateMetricsPath(p); err == nil {
			t.Errorf("ValidateMetricsPath(%q)=nil, want error", p)
		}
	}
}
