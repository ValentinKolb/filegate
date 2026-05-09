//go:build linux

package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/eventbus"
	"github.com/valentinkolb/filegate/infra/filesystem"
	indexpebble "github.com/valentinkolb/filegate/infra/pebble"
)

// newMultiTenantTestServer wires up a service with multiple mounts +
// a router configured with the given Keys list. Mount names are
// "alpha", "beta", "gamma" so tests can pin per-key access patterns.
func newMultiTenantTestServer(t *testing.T, keys []KeyEntry) (svc *domain.Service, handler http.Handler, cleanup func()) {
	t.Helper()
	baseDir := t.TempDir()
	indexDir := t.TempDir()
	idx, err := indexpebble.Open(indexDir, 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	bus := eventbus.New()
	mountPaths := []string{baseDir + "/alpha", baseDir + "/beta", baseDir + "/gamma"}
	for _, p := range mountPaths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	svc, err = domain.NewService(idx, filesystem.New(), bus, mountPaths, 1000)
	if err != nil {
		_ = idx.Close()
		t.Fatalf("new service: %v", err)
	}
	handler, err = NewHandler(svc, Options{
		Region: testRegion,
		Keys:   keys,
	})
	if err != nil {
		_ = idx.Close()
		t.Fatalf("new handler: %v", err)
	}
	cleanup = func() {
		testSvcGlobal = nil
		bus.Close()
		_ = idx.Close()
	}
	testSvcGlobal = svc
	return svc, handler, cleanup
}

// signWithKey signs a request using the supplied (accessKey, secretKey)
// pair so multi-tenant tests can simulate different callers without
// touching the global testAccessKey/testSecretKey.
func signWithKey(req *http.Request, payload []byte, accessKey, secretKey string) {
	hash := sha256.Sum256(payload)
	signRequest(req, accessKey, secretKey, testRegion, hex.EncodeToString(hash[:]), time.Now())
}

// TestMultiTenantListBucketsFilteredByKey: ListBuckets returns only
// the buckets in the requesting key's whitelist — never the full
// mount list. A second key with a disjoint whitelist sees its own
// subset; an admin key with "*" sees all three.
func TestMultiTenantListBucketsFilteredByKey(t *testing.T) {
	const (
		k1Access = "AKIAFIRSTKEY00000001"
		k1Secret = "secret-for-first-key-fillerfillerfiller"
		k2Access = "AKIASECONDKEY0000002"
		k2Secret = "secret-for-second-key-fillerfillerfille"
		kAAccess = "AKIAADMINKEY00000003"
		kASecret = "secret-for-admin-key-fillerfillerfillrr"
	)
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: k1Access, SecretKey: k1Secret, Buckets: []string{"alpha"}},
		{AccessKey: k2Access, SecretKey: k2Secret, Buckets: []string{"beta", "gamma"}},
		{AccessKey: kAAccess, SecretKey: kASecret, Buckets: []string{"*"}},
	})
	defer cleanup()

	listFor := func(access, secret string) []string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "example.com"
		signWithKey(req, nil, access, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ListBuckets status=%d body=%s", rec.Code, rec.Body.String())
		}
		var res listAllMyBucketsResult
		_ = xml.Unmarshal(rec.Body.Bytes(), &res)
		out := []string{}
		for _, b := range res.Buckets.Bucket {
			out = append(out, b.Name)
		}
		return out
	}

	if got := listFor(k1Access, k1Secret); !equalSorted(got, []string{"alpha"}) {
		t.Errorf("k1 ListBuckets=%v, want [alpha]", got)
	}
	if got := listFor(k2Access, k2Secret); !equalSorted(got, []string{"beta", "gamma"}) {
		t.Errorf("k2 ListBuckets=%v, want [beta gamma]", got)
	}
	if got := listFor(kAAccess, kASecret); !equalSorted(got, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("admin ListBuckets=%v, want [alpha beta gamma]", got)
	}
}

// TestMultiTenantBucketAccessForbidden: a key trying to access a
// bucket NOT in its whitelist gets AccessDenied — and crucially,
// the response cannot distinguish "bucket exists" from "bucket
// doesn't exist". Both forbidden + nonexistent return the same 403
// AccessDenied so an attacker can't probe for bucket names.
func TestMultiTenantBucketAccessForbidden(t *testing.T) {
	const (
		access = "AKIASCOPEDKEY0000001"
		secret = "secret-for-scoped-key-fillerfillerfill"
	)
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: access, SecretKey: secret, Buckets: []string{"alpha"}},
	})
	defer cleanup()

	probe := func(method, path string) (*httptest.ResponseRecorder, []byte) {
		body := []byte("dummy")
		var r *http.Request
		switch method {
		case http.MethodPut:
			r = httptest.NewRequest(method, path, bytes.NewReader(body))
		default:
			body = nil
			r = httptest.NewRequest(method, path, nil)
		}
		r.Host = "example.com"
		signWithKey(r, body, access, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec, body
	}

	// Permitted bucket: should NOT be 403 (anything else is fine —
	// we're testing the authorization gate, not the operation).
	rec, _ := probe(http.MethodGet, "/alpha")
	if rec.Code == http.StatusForbidden {
		t.Errorf("permitted bucket should NOT return 403, got body=%s", rec.Body.String())
	}

	// Forbidden but EXISTING bucket: 403 AccessDenied.
	rec, _ = probe(http.MethodGet, "/beta")
	if rec.Code != http.StatusForbidden {
		t.Errorf("forbidden bucket GET status=%d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AccessDenied") {
		t.Errorf("forbidden bucket body=%s, want AccessDenied", rec.Body.String())
	}

	// Nonexistent bucket (also not in whitelist): same 403 — does
	// NOT leak that the bucket is missing.
	rec, _ = probe(http.MethodGet, "/nosuchbucket")
	if rec.Code != http.StatusForbidden {
		t.Errorf("nonexistent bucket GET status=%d, want 403 (existence must not leak)", rec.Code)
	}

	// Same isolation on object-level ops.
	rec, _ = probe(http.MethodPut, "/beta/file.txt")
	if rec.Code != http.StatusForbidden {
		t.Errorf("forbidden object PUT status=%d, want 403", rec.Code)
	}
	rec, _ = probe(http.MethodGet, "/beta/anything")
	if rec.Code != http.StatusForbidden {
		t.Errorf("forbidden object GET status=%d, want 403", rec.Code)
	}
	rec, _ = probe(http.MethodDelete, "/beta/anything")
	if rec.Code != http.StatusForbidden {
		t.Errorf("forbidden object DELETE status=%d, want 403", rec.Code)
	}
	rec, _ = probe(http.MethodHead, "/beta")
	if rec.Code != http.StatusForbidden {
		t.Errorf("forbidden bucket HEAD status=%d, want 403", rec.Code)
	}
}

// TestMultiTenantWildcardAccess: a key with the "*" wildcard can
// reach every configured mount.
func TestMultiTenantWildcardAccess(t *testing.T) {
	const (
		access = "AKIAWILDCARDKEY00001"
		secret = "secret-wildcard-key-fillerfillerfillerfill"
	)
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: access, SecretKey: secret, Buckets: []string{"*"}},
	})
	defer cleanup()

	for _, b := range []string{"alpha", "beta", "gamma"} {
		req := httptest.NewRequest(http.MethodHead, "/"+b, nil)
		req.Host = "example.com"
		signWithKey(req, nil, access, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("wildcard key was forbidden on bucket %q", b)
		}
	}
}

// TestMultiTenantEmptyWhitelistDenies: a key with an empty Buckets
// slice authenticates successfully but every bucket op returns 403.
// Useful for staging revocation without removing the entry.
func TestMultiTenantEmptyWhitelistDenies(t *testing.T) {
	const (
		access = "AKIANOACCESSKEY00001"
		secret = "secret-no-access-key-fillerfillerfilfil"
	)
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: access, SecretKey: secret, Buckets: nil},
	})
	defer cleanup()

	// ListBuckets should succeed but return zero buckets (the key
	// itself is valid).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	signWithKey(req, nil, access, secret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListBuckets with empty whitelist status=%d, want 200", rec.Code)
	}
	var res listAllMyBucketsResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Buckets.Bucket) != 0 {
		t.Errorf("empty whitelist returned %d buckets, want 0", len(res.Buckets.Bucket))
	}

	// Any specific-bucket op is 403.
	req = httptest.NewRequest(http.MethodHead, "/alpha", nil)
	req.Host = "example.com"
	signWithKey(req, nil, access, secret)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("empty-whitelist HEAD bucket status=%d, want 403", rec.Code)
	}
}

// TestNewHandlerRejectsDuplicateAccessKey: two Keys entries with
// the same access key must be rejected at startup. This catches an
// operator who pasted a key twice — silent override would be a
// confusing security hazard.
func TestNewHandlerRejectsDuplicateAccessKey(t *testing.T) {
	baseDir := t.TempDir()
	indexDir := t.TempDir()
	idx, err := indexpebble.Open(indexDir, 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()
	bus := eventbus.New()
	defer bus.Close()
	mountPath := baseDir + "/data"
	_ = os.MkdirAll(mountPath, 0o755)
	svc, err := domain.NewService(idx, filesystem.New(), bus, []string{mountPath}, 1000)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	_, err = NewHandler(svc, Options{
		Region: testRegion,
		Keys: []KeyEntry{
			{AccessKey: "DUPLICATEKEY00000001", SecretKey: "secret-1", Buckets: []string{"data"}},
			{AccessKey: "DUPLICATEKEY00000001", SecretKey: "secret-2", Buckets: []string{"data"}},
		},
	})
	if err == nil {
		t.Fatalf("duplicate access key should have been rejected at construction")
	}
	if !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("error=%q, want mention of duplicate", err.Error())
	}
}

// TestNewHandlerRejectsUnknownBucketInWhitelist: a Keys entry whose
// whitelist references a mount that doesn't exist must fail at
// startup. Catches typos and misconfigurations early — an operator
// who whitelists "buckte" instead of "bucket" gets a clean error
// instead of silently no-access for that key.
func TestNewHandlerRejectsUnknownBucketInWhitelist(t *testing.T) {
	baseDir := t.TempDir()
	indexDir := t.TempDir()
	idx, err := indexpebble.Open(indexDir, 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()
	bus := eventbus.New()
	defer bus.Close()
	mountPath := baseDir + "/data"
	_ = os.MkdirAll(mountPath, 0o755)
	svc, err := domain.NewService(idx, filesystem.New(), bus, []string{mountPath}, 1000)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	_, err = NewHandler(svc, Options{
		Region: testRegion,
		Keys: []KeyEntry{
			{AccessKey: "AKIA00000000000000A1", SecretKey: "valid-secret", Buckets: []string{"buckte"}},
		},
	})
	if err == nil {
		t.Fatalf("unknown bucket in whitelist should be rejected")
	}
	if !strings.Contains(err.Error(), "buckte") {
		t.Errorf("error=%q, want mention of unknown bucket", err.Error())
	}
}

// TestLegacySingleTenantStillWorks: AccessKey/SecretKey at the top
// level (M1 backward compatibility) still grants access to every
// mount via the synthesized "*" entry.
func TestLegacySingleTenantStillWorks(t *testing.T) {
	const (
		legacyAccess = "AKIALEGACYKEY0000001"
		legacySecret = "secret-legacy-fillerfillerfillerfillerff"
	)
	baseDir := t.TempDir()
	indexDir := t.TempDir()
	idx, err := indexpebble.Open(indexDir, 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	bus := eventbus.New()
	defer bus.Close()
	defer idx.Close()
	mountPaths := []string{baseDir + "/alpha", baseDir + "/beta"}
	for _, p := range mountPaths {
		_ = os.MkdirAll(p, 0o755)
	}
	svc, err := domain.NewService(idx, filesystem.New(), bus, mountPaths, 1000)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	handler, err := NewHandler(svc, Options{
		Region:    testRegion,
		AccessKey: legacyAccess,
		SecretKey: legacySecret,
	})
	if err != nil {
		t.Fatalf("NewHandler legacy single-tenant: %v", err)
	}

	for _, b := range []string{"alpha", "beta"} {
		req := httptest.NewRequest(http.MethodHead, "/"+b, nil)
		req.Host = "example.com"
		signWithKey(req, nil, legacyAccess, legacySecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("legacy single-tenant key forbidden on bucket %q", b)
		}
	}
}

// equalSorted reports whether a and b contain the same strings,
// order-independent.
func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := map[string]int{}
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
	}
	for _, c := range count {
		if c != 0 {
			return false
		}
	}
	return true
}
