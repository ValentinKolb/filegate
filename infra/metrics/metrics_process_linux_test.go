//go:build linux

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProcessCollectorOpenFDs asserts the process collector emits
// process_open_fds — the file-descriptor-leak signal that matters most
// for a file gateway. Linux-only: client_golang's process collector is
// /proc-backed and silently emits nothing on macOS/BSD, so this lives
// behind the linux build tag.
func TestProcessCollectorOpenFDs(t *testing.T) {
	r := New(BuildInfo{}, nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "process_open_fds") {
		t.Errorf("/metrics missing process_open_fds (FD-leak signal)")
	}
}
