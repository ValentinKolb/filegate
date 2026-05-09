//go:build linux

package s3

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// putForTest helper: PUT a small object and return on success.
func putForTest(t *testing.T, handler http.Handler, mount, key string, body []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/"+key, bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT %s status=%d body=%s", key, rec.Code, rec.Body.String())
	}
}

// postDeleteObjects helper: send a DeleteObjects bulk request with
// the given keys + quiet mode flag, return the recorder.
func postDeleteObjects(t *testing.T, handler http.Handler, mount string, quiet bool, keys ...string) *httptest.ResponseRecorder {
	t.Helper()
	body := deleteObjectsRequest{Quiet: quiet}
	for _, k := range keys {
		body.Objects = append(body.Objects, deleteRequestObject{Key: k})
	}
	raw, err := xml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"?delete", bytes.NewReader(raw))
	req.Host = "example.com"
	signRequestPayload(req, raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestDeleteObjectsHappyPath: mixed batch of (existing, missing,
// already-deleted) keys returns Deleted for all of them — missing
// keys are idempotent successes per AWS spec.
func TestDeleteObjectsHappyPath(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "a.txt", []byte("AAA"))
	putForTest(t, handler, mount, "b.txt", []byte("BBB"))
	putForTest(t, handler, mount, "c.txt", []byte("CCC"))

	rec := postDeleteObjects(t, handler, mount, false,
		"a.txt", "b.txt", "missing.txt", "c.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("DeleteObjects status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res deleteObjectsResult
	if err := xml.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors=%v, want none", res.Errors)
	}
	deleted := map[string]bool{}
	for _, d := range res.Deleted {
		deleted[d.Key] = true
	}
	for _, k := range []string{"a.txt", "b.txt", "c.txt", "missing.txt"} {
		if !deleted[k] {
			t.Errorf("Deleted is missing %q (have %v)", k, deleted)
		}
	}

	// Verify GETs return 404 now.
	for _, k := range []string{"a.txt", "b.txt", "c.txt"} {
		req := httptest.NewRequest(http.MethodGet, "/"+mount+"/"+k, nil)
		req.Host = "example.com"
		signRequestPayload(req, nil)
		gRec := httptest.NewRecorder()
		handler.ServeHTTP(gRec, req)
		if gRec.Code != http.StatusNotFound {
			t.Errorf("GET %s after delete status=%d, want 404", k, gRec.Code)
		}
	}
}

// TestDeleteObjectsQuietMode: quiet=true suppresses the Deleted
// entries but errors still appear.
func TestDeleteObjectsQuietMode(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "x.txt", []byte("X"))

	// Quiet, all-success: Deleted should be empty.
	rec := postDeleteObjects(t, handler, mount, true, "x.txt", "missing.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var res deleteObjectsResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Deleted) != 0 {
		t.Errorf("quiet mode Deleted=%d, want 0 (suppressed)", len(res.Deleted))
	}
	if len(res.Errors) != 0 {
		t.Errorf("quiet mode Errors=%d, want 0 (no errors here)", len(res.Errors))
	}
}

// TestDeleteObjectsRejectsVersionId: VersionId in a request entry
// surfaces as a per-entry InvalidArgument — bulk-delete returns 200
// overall but with that key in Errors. Important: filegate exposes
// no per-object versions on S3, and silently dropping VersionId
// would mislead clients into thinking they removed a specific
// version.
func TestDeleteObjectsRejectsVersionId(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := deleteObjectsRequest{
		Objects: []deleteRequestObject{
			{Key: "k.txt", VersionID: "some-version-id"},
		},
	}
	raw, _ := xml.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"?delete", bytes.NewReader(raw))
	req.Host = "example.com"
	signRequestPayload(req, raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res deleteObjectsResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Errors) != 1 || res.Errors[0].Key != "k.txt" {
		t.Fatalf("expected one Error entry for k.txt, got %v", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "VersionId") {
		t.Errorf("error message=%q, want VersionId mention", res.Errors[0].Message)
	}
}

// TestDeleteObjectsValidationFailures: a key with embedded "../"
// or other forbidden segments surfaces as InvalidArgument in the
// per-entry Error list — does NOT abort the whole batch.
func TestDeleteObjectsValidationFailures(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "ok.txt", []byte("ok"))

	body := deleteObjectsRequest{
		Objects: []deleteRequestObject{
			{Key: "ok.txt"},
			{Key: "../escape"},
			{Key: ".fg-versions/secret"},
		},
	}
	raw, _ := xml.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"?delete", bytes.NewReader(raw))
	req.Host = "example.com"
	signRequestPayload(req, raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var res deleteObjectsResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Deleted) != 1 || res.Deleted[0].Key != "ok.txt" {
		t.Errorf("Deleted=%v, want [ok.txt]", res.Deleted)
	}
	if len(res.Errors) != 2 {
		t.Errorf("Errors=%d, want 2", len(res.Errors))
	}
}

// TestDeleteObjectsRejectsEmptyBatch: a Delete document with no
// Object entries is MalformedXML. Mirrors AWS behaviour.
func TestDeleteObjectsRejectsEmptyBatch(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := deleteObjectsRequest{}
	raw, _ := xml.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"?delete", bytes.NewReader(raw))
	req.Host = "example.com"
	signRequestPayload(req, raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-batch status=%d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "MalformedXML") {
		t.Errorf("body should mention MalformedXML, got %s", rec.Body.String())
	}
}

// TestDeleteObjectsCapEnforced: requests exceeding the 1000-key
// cap are rejected upfront with MalformedXML. Bounding the cap
// here is a defensive measure against pathological clients —
// real S3 clients honour the limit.
func TestDeleteObjectsCapEnforced(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := deleteObjectsRequest{}
	for i := 0; i < deleteObjectsMaxKeys+1; i++ {
		body.Objects = append(body.Objects, deleteRequestObject{
			Key: fmt.Sprintf("k%04d.txt", i),
		})
	}
	raw, _ := xml.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"?delete", bytes.NewReader(raw))
	req.Host = "example.com"
	signRequestPayload(req, raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-cap status=%d, want 400", rec.Code)
	}
}

// TestDeleteObjectsForbiddenBucket: a key without access to the
// bucket gets AccessDenied at the dispatcher — the request never
// reaches handleDeleteObjects.
func TestDeleteObjectsForbiddenBucket(t *testing.T) {
	const (
		access = "AKIASCOPEDDELETE0001"
		secret = "secret-scoped-delete-fillerfillerfille"
	)
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: access, SecretKey: secret, Buckets: []string{"alpha"}},
	})
	defer cleanup()

	body := deleteObjectsRequest{
		Objects: []deleteRequestObject{{Key: "anything.txt"}},
	}
	raw, _ := xml.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/beta?delete", bytes.NewReader(raw))
	req.Host = "example.com"
	signWithKey(req, raw, access, secret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("DeleteObjects on forbidden bucket status=%d, want 403", rec.Code)
	}
}

// TestDeleteObjectsMalformedBody: a non-XML body returns
// MalformedXML — the request never reaches per-entry processing.
func TestDeleteObjectsMalformedBody(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("this is not XML")
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"?delete", bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body status=%d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "MalformedXML") {
		t.Errorf("body should mention MalformedXML, got %s", rec.Body.String())
	}
}
