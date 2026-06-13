package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func remoteAddrSeenBy(t *testing.T, trusted []string, peer string, headers map[string]string) string {
	t.Helper()
	prefixes, err := ParseTrustedProxies(trusted)
	if err != nil {
		t.Fatalf("parse trusted proxies: %v", err)
	}
	mw := realIPMiddleware(prefixes)
	var seen string
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	})
	var h http.Handler = handler
	if mw != nil {
		h = mw(handler)
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = peer
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

// TestRealIPIgnoresForwardHeadersWithoutTrustedProxies pins the secure
// default: with no trusted proxies configured, X-Forwarded-For and
// X-Real-Ip never rewrite the peer address — a direct client used to be
// able to spoof its logged IP.
func TestRealIPIgnoresForwardHeadersWithoutTrustedProxies(t *testing.T) {
	got := remoteAddrSeenBy(t, nil, "203.0.113.7:4711", map[string]string{
		"X-Forwarded-For": "10.0.0.1",
		"X-Real-Ip":       "10.0.0.2",
	})
	if got != "203.0.113.7:4711" {
		t.Fatalf("RemoteAddr=%q, want untouched peer", got)
	}
}

func TestRealIPIgnoresForwardHeadersFromUntrustedPeer(t *testing.T) {
	got := remoteAddrSeenBy(t, []string{"192.0.2.10"}, "203.0.113.7:4711", map[string]string{
		"X-Forwarded-For": "10.0.0.1",
	})
	if got != "203.0.113.7:4711" {
		t.Fatalf("RemoteAddr=%q, want untouched peer", got)
	}
}

func TestRealIPHonorsTrustedPeerAndSkipsTrustedHops(t *testing.T) {
	// Client-forged left entry, real client, then a second trusted
	// proxy hop: the walk from the right must skip the trusted hop and
	// pick 198.51.100.9, never the forged 1.2.3.4.
	got := remoteAddrSeenBy(t, []string{"192.0.2.0/24"}, "192.0.2.10:9999", map[string]string{
		"X-Forwarded-For": "1.2.3.4, 198.51.100.9, 192.0.2.11",
	})
	if got != "198.51.100.9" {
		t.Fatalf("RemoteAddr=%q, want 198.51.100.9", got)
	}

	// X-Real-Ip fallback when no X-Forwarded-For is present.
	got = remoteAddrSeenBy(t, []string{"192.0.2.10"}, "192.0.2.10:9999", map[string]string{
		"X-Real-Ip": "198.51.100.20",
	})
	if got != "198.51.100.20" {
		t.Fatalf("RemoteAddr=%q, want 198.51.100.20", got)
	}
}

func TestRealIPMalformedChainHandling(t *testing.T) {
	// Garbage LEFT of the client entry is irrelevant: the rightmost
	// untrusted hop was appended by our own proxy and is authentic.
	got := remoteAddrSeenBy(t, []string{"192.0.2.10"}, "192.0.2.10:9999", map[string]string{
		"X-Forwarded-For": "not-an-ip, 198.51.100.9",
	})
	if got != "198.51.100.9" {
		t.Fatalf("RemoteAddr=%q, want 198.51.100.9 (garbage left of client is ignored)", got)
	}

	// A malformed entry in the walked (rightmost) positions distrusts
	// the whole chain — the peer address is kept.
	got = remoteAddrSeenBy(t, []string{"192.0.2.10"}, "192.0.2.10:9999", map[string]string{
		"X-Forwarded-For": "198.51.100.9, not-an-ip",
	})
	if got != "192.0.2.10:9999" {
		t.Fatalf("RemoteAddr=%q, want untouched peer on malformed rightmost entry", got)
	}
}

func TestParseTrustedProxiesRejectsGarbage(t *testing.T) {
	if _, err := ParseTrustedProxies([]string{"10.0.0.0/8", "nope"}); err == nil {
		t.Fatal("expected error for malformed entry")
	}
	prefixes, err := ParseTrustedProxies([]string{" 10.0.0.1 ", "fd00::/8", ""})
	if err != nil {
		t.Fatalf("parse valid entries: %v", err)
	}
	if len(prefixes) != 2 {
		t.Fatalf("prefixes=%d, want 2", len(prefixes))
	}
	if !prefixes[0].Contains(netip.MustParseAddr("10.0.0.1")) {
		t.Fatalf("bare IP not converted to single-address prefix")
	}
}
