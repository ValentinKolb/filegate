//go:build linux

package s3

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// copyObject helper: performs a CopyObject (PUT with x-amz-copy-source)
// and returns the recorder. Headers map allows tests to attach
// preconditions / metadata directives.
func copyObject(t *testing.T, handler http.Handler, srcBucket, srcKey, destBucket, destKey string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/"+destBucket+"/"+destKey, nil)
	req.Host = "example.com"
	req.Header.Set("x-amz-copy-source", "/"+srcBucket+"/"+srcKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestCopyObjectHappyPath: copy within the same mount; dest gets
// a new entity, source is untouched, ETags match (single-MD5).
func TestCopyObjectHappyPath(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("source bytes for copy")
	expectedMD5 := md5.Sum(body)
	expectedETag := hex.EncodeToString(expectedMD5[:])
	putForTest(t, handler, mount, "src.txt", body)

	rec := copyObject(t, handler, mount, "src.txt", mount, "dst.txt", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("CopyObject status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res copyObjectResult
	if err := xml.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotETag := strings.Trim(res.ETag, `"`)
	if gotETag != expectedETag {
		t.Errorf("dest ETag=%q, want %q (must equal source MD5)", gotETag, expectedETag)
	}
	if res.LastModified == "" {
		t.Errorf("LastModified missing in response")
	}

	// Verify GET on dest returns the same bytes + ETag.
	gReq := httptest.NewRequest(http.MethodGet, "/"+mount+"/dst.txt", nil)
	gReq.Host = "example.com"
	signRequestPayload(gReq, nil)
	gRec := httptest.NewRecorder()
	handler.ServeHTTP(gRec, gReq)
	if gRec.Code != http.StatusOK {
		t.Fatalf("GET dest status=%d", gRec.Code)
	}
	if !bytes.Equal(gRec.Body.Bytes(), body) {
		t.Errorf("GET dest body length=%d, want %d", gRec.Body.Len(), len(body))
	}
	if got := strings.Trim(gRec.Header().Get("ETag"), `"`); got != expectedETag {
		t.Errorf("GET dest ETag=%q", got)
	}

	// Source must still exist.
	sReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/src.txt", nil)
	sReq.Host = "example.com"
	signRequestPayload(sReq, nil)
	sRec := httptest.NewRecorder()
	handler.ServeHTTP(sRec, sReq)
	if sRec.Code != http.StatusOK {
		t.Errorf("source HEAD after copy status=%d, want 200", sRec.Code)
	}
}

// TestCopyObjectMetadataDirectiveCopy: COPY directive (default)
// inherits source's content-type + user metadata.
func TestCopyObjectMetadataDirectiveCopy(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Seed source with explicit metadata.
	body := []byte("payload")
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/src.txt", bytes.NewReader(body))
	req.Host = "example.com"
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("x-amz-meta-author", "alice")
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT status=%d", rec.Code)
	}

	// Copy without metadata directive (defaults to COPY).
	rec = copyObject(t, handler, mount, "src.txt", mount, "dst.txt", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("CopyObject status=%d body=%s", rec.Code, rec.Body.String())
	}

	// HEAD dest, expect inherited metadata.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/dst.txt", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	if got := hRec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("dest Content-Type=%q, want inherited", got)
	}
	if got := getMetaHeader(hRec.Header(), "x-amz-meta-author"); got != "alice" {
		t.Errorf("dest x-amz-meta-author=%q, want inherited 'alice'", got)
	}
}

// TestCopyObjectMetadataDirectiveReplace: REPLACE directive uses
// the request headers, dropping the source's metadata.
func TestCopyObjectMetadataDirectiveReplace(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Seed source with one set of metadata.
	body := []byte("payload")
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/src.txt", bytes.NewReader(body))
	req.Host = "example.com"
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("x-amz-meta-original", "yes")
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT status=%d", rec.Code)
	}

	// Copy with REPLACE + new metadata.
	rec = copyObject(t, handler, mount, "src.txt", mount, "dst.txt", map[string]string{
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "image/png",
		"x-amz-meta-replaced":      "yes",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("CopyObject REPLACE status=%d body=%s", rec.Code, rec.Body.String())
	}

	// HEAD dest, expect REPLACE metadata.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/dst.txt", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	if got := hRec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("REPLACE dest Content-Type=%q, want image/png", got)
	}
	if got := getMetaHeader(hRec.Header(), "x-amz-meta-replaced"); got != "yes" {
		t.Errorf("REPLACE dest x-amz-meta-replaced=%q", got)
	}
	if got := getMetaHeader(hRec.Header(), "x-amz-meta-original"); got != "" {
		t.Errorf("REPLACE dest x-amz-meta-original=%q, want empty (source metadata dropped)", got)
	}
}

// TestCopyObjectSourceIfMatch: x-amz-copy-source-if-match with the
// source's actual ETag → succeeds; with a wrong ETag → 412.
func TestCopyObjectSourceIfMatch(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("etag conditional source")
	expected := md5.Sum(body)
	srcETag := hex.EncodeToString(expected[:])
	putForTest(t, handler, mount, "src.txt", body)

	// Matching ETag — succeeds.
	rec := copyObject(t, handler, mount, "src.txt", mount, "dst-ok.txt", map[string]string{
		"x-amz-copy-source-if-match": `"` + srcETag + `"`,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("copy with matching if-match status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Mismatching ETag — 412.
	rec = copyObject(t, handler, mount, "src.txt", mount, "dst-fail.txt", map[string]string{
		"x-amz-copy-source-if-match": `"deadbeefdeadbeefdeadbeefdeadbeef"`,
	})
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("copy with bogus if-match status=%d, want 412", rec.Code)
	}
}

// TestCopyObjectSourceIfModifiedSince: a source-if-modified-since
// in the future → 412 (source is older than the threshold).
func TestCopyObjectSourceIfModifiedSince(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "src.txt", []byte("data"))
	future := time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)

	rec := copyObject(t, handler, mount, "src.txt", mount, "dst.txt", map[string]string{
		"x-amz-copy-source-if-modified-since": future,
	})
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("copy with future modified-since status=%d, want 412", rec.Code)
	}
}

// TestCopyObjectDestIfNoneMatchExisting: dest If-None-Match: *
// fails when the dest already exists.
func TestCopyObjectDestIfNoneMatchExisting(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "src.txt", []byte("source"))
	putForTest(t, handler, mount, "dst.txt", []byte("preexisting"))

	rec := copyObject(t, handler, mount, "src.txt", mount, "dst.txt", map[string]string{
		"If-None-Match": "*",
	})
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("copy onto existing with If-None-Match: * status=%d, want 412", rec.Code)
	}

	// Dest must NOT have been overwritten.
	gReq := httptest.NewRequest(http.MethodGet, "/"+mount+"/dst.txt", nil)
	gReq.Host = "example.com"
	signRequestPayload(gReq, nil)
	gRec := httptest.NewRecorder()
	handler.ServeHTTP(gRec, gReq)
	if !bytes.Equal(gRec.Body.Bytes(), []byte("preexisting")) {
		t.Errorf("dest body changed despite failed precondition")
	}
}

// TestCopyObjectInvalidCopySource: a malformed copy-source header
// returns InvalidArgument before any work happens.
func TestCopyObjectInvalidCopySource(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Note: an EMPTY x-amz-copy-source header is treated as "not a
	// copy" by handlePutObject and falls through to the regular PUT
	// path — that's correct behavior, so we don't test it here.
	cases := map[string]string{
		"missing slash":  "bucketonly",
		"trailing slash": "/bucket/",
		"versioned":      "/bucket/key?versionId=v123",
	}
	for name, src := range cases {
		req := httptest.NewRequest(http.MethodPut, "/"+mount+"/dst.txt", nil)
		req.Host = "example.com"
		req.Header.Set("x-amz-copy-source", src)
		signRequestPayload(req, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestCopyObjectURLEncodedSourceKey: source keys with spaces /
// unicode are URL-encoded on the wire and we must decode before
// resolving. This pins that behavior — without decoding, the
// source lookup fails with NoSuchKey.
func TestCopyObjectURLEncodedSourceKey(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Seed source via a URL with the path pre-encoded — httptest's
	// Request parser otherwise rejects raw spaces.
	body := []byte("encoded source")
	seedReq := httptest.NewRequest(http.MethodPut, "/"+mount+"/folder/with%20spaces.txt", bytes.NewReader(body))
	seedReq.Host = "example.com"
	signRequestPayload(seedReq, body)
	seedRec := httptest.NewRecorder()
	handler.ServeHTTP(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed PUT status=%d", seedRec.Code)
	}

	// PUT /{dest}/dst.txt with x-amz-copy-source: /mount/folder/with%20spaces.txt
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/dst.txt", nil)
	req.Host = "example.com"
	req.Header.Set("x-amz-copy-source", "/"+mount+"/folder/with%20spaces.txt")
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("URL-encoded source copy status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCopyObjectForbiddenSourceBucket: a key without source-bucket
// access cannot read it via copy. The check must NOT leak source
// existence (returns AccessDenied even for a nonexistent source
// bucket).
func TestCopyObjectForbiddenSourceBucket(t *testing.T) {
	const (
		access = "AKIASCOPEDCOPY000001"
		secret = "secret-scoped-copy-fillerfillerfillerff"
	)
	// Key has access to alpha + beta but NOT gamma.
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: access, SecretKey: secret, Buckets: []string{"alpha", "beta"}},
	})
	defer cleanup()

	// Try to copy from gamma (forbidden) to alpha (permitted).
	req := httptest.NewRequest(http.MethodPut, "/alpha/dst.txt", nil)
	req.Host = "example.com"
	req.Header.Set("x-amz-copy-source", "/gamma/anything.txt")
	signWithKey(req, nil, access, secret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("copy from forbidden src status=%d, want 403", rec.Code)
	}
}

// TestCopyObjectSelfReplaceMetadata: self-copy with REPLACE rewrites
// metadata in place. Mirrors how AWS clients change content-type
// on existing objects (the "S3 metadata-update trick").
func TestCopyObjectSelfReplaceMetadata(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("self-copy bytes")
	putForTest(t, handler, mount, "obj.txt", body)

	rec := copyObject(t, handler, mount, "obj.txt", mount, "obj.txt", map[string]string{
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "application/json",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("self-REPLACE status=%d body=%s", rec.Code, rec.Body.String())
	}

	// HEAD: bytes must still be there (verified via Content-Length),
	// content-type must be the new one.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/obj.txt", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	if got := hRec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("self-REPLACE Content-Type=%q, want application/json", got)
	}
	if got := hRec.Header().Get("Content-Length"); got != fmt.Sprintf("%d", len(body)) {
		t.Errorf("self-REPLACE Content-Length=%q, want %d", got, len(body))
	}
}

// TestCopyObjectMultipartSourceETagIsMD5: a CopyObject from a
// multipart-uploaded source must produce a destination whose ETag
// is the whole-body MD5, NOT the source's composite "...-N" ETag.
// Pre-fix the response and stored ETagMD5 carried the composite
// value, breaking clients that validate CopyObject ETags as MD5
// hex.
func TestCopyObjectMultipartSourceETagIsMD5(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Create a multipart upload to seed a composite-ETag source.
	uploadID := initMultipart(t, handler, mount, "mp-src.bin", nil)
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 1024)
	e1 := uploadPart(t, handler, mount, "mp-src.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "mp-src.bin", uploadID, 2, p2)
	rec := completeMultipart(t, handler, mount, "mp-src.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed multipart Complete status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Copy multipart-source → single-object dest.
	rec = copyObject(t, handler, mount, "mp-src.bin", mount, "dst.bin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("CopyObject from multipart source status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res copyObjectResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	gotETag := strings.Trim(res.ETag, `"`)
	// The composite ETag has a "-N" suffix; the response ETag must
	// not.
	if strings.Contains(gotETag, "-") {
		t.Errorf("CopyObject response ETag=%q is the composite form, want plain MD5", gotETag)
	}
	if len(gotETag) != 32 {
		t.Errorf("CopyObject response ETag=%q is %d hex chars, want 32 (MD5)", gotETag, len(gotETag))
	}

	// Whole-body MD5 of (p1 || p2).
	wantBody := append(append([]byte{}, p1...), p2...)
	want := md5.Sum(wantBody)
	if gotETag != hex.EncodeToString(want[:]) {
		t.Errorf("CopyObject ETag=%q, want %x", gotETag, want)
	}

	// HEAD on dest must report the same plain-MD5 ETag.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/dst.bin", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	hETag := strings.Trim(hRec.Header().Get("ETag"), `"`)
	if strings.Contains(hETag, "-") {
		t.Errorf("HEAD dest ETag=%q still composite — multipart_etag was not cleared", hETag)
	}
}

// TestCopyObjectAtomicOnFailure: when the byte copy fails partway
// through and we're overwriting an existing dest, the existing
// dest must not be corrupted/truncated. We trigger the failure by
// pointing the source to an unreadable path via the bucket
// boundary (the explicit failure path here is the InternalError
// branch in the domain method when it can't read source bytes).
//
// This test is a little contrived: we can't easily make CloneFile
// fail mid-stream from the adapter level. So we exercise the
// atomic-rename path by checking the dest's bytes are byte-for-byte
// identical to the source's after a successful copy — i.e. that we
// never observed a truncated dest in the index. Combined with the
// tmp+rename code path, this is a smoke test that the new shape
// hasn't regressed.
func TestCopyObjectAtomicOverwrite(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Seed dest with bytes A.
	bytesA := bytes.Repeat([]byte{0xAA}, 4096)
	putForTest(t, handler, mount, "dst.bin", bytesA)

	// Seed source with bytes B (different size + content).
	bytesB := bytes.Repeat([]byte{0xBB}, 8192)
	putForTest(t, handler, mount, "src.bin", bytesB)

	rec := copyObject(t, handler, mount, "src.bin", mount, "dst.bin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Copy onto existing dest status=%d body=%s", rec.Code, rec.Body.String())
	}

	// GET dest, expect bytes B (full source content) — not a
	// truncated mix or a half-overwritten state.
	gReq := httptest.NewRequest(http.MethodGet, "/"+mount+"/dst.bin", nil)
	gReq.Host = "example.com"
	signRequestPayload(gReq, nil)
	gRec := httptest.NewRecorder()
	handler.ServeHTTP(gRec, gReq)
	if gRec.Code != http.StatusOK {
		t.Fatalf("GET dest status=%d", gRec.Code)
	}
	if !bytes.Equal(gRec.Body.Bytes(), bytesB) {
		t.Errorf("dest bytes after overwrite copy mismatch (len got=%d want=%d)", gRec.Body.Len(), len(bytesB))
	}
}

// TestCopyObjectSelfReplaceAdvancesLastModified: a self-copy
// REPLACE bumps the file's mtime so HEAD reports the new
// Last-Modified. Pre-fix the metadata-update trick left mtime
// unchanged, breaking clients that watch mtime for cache
// invalidation.
func TestCopyObjectSelfReplaceAdvancesLastModified(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "obj.txt", []byte("hello"))

	// Capture pre-copy Last-Modified.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/obj.txt", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	preLM := hRec.Header().Get("Last-Modified")
	if preLM == "" {
		t.Fatalf("Last-Modified missing pre-copy")
	}

	// Sleep just past 1 second so the new mtime crosses an HTTP-date
	// boundary (HTTP-date has 1-second resolution).
	time.Sleep(1100 * time.Millisecond)

	rec := copyObject(t, handler, mount, "obj.txt", mount, "obj.txt", map[string]string{
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "application/json",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("self-REPLACE status=%d", rec.Code)
	}

	// HEAD again — Last-Modified must have advanced.
	hReq2 := httptest.NewRequest(http.MethodHead, "/"+mount+"/obj.txt", nil)
	hReq2.Host = "example.com"
	signRequestPayload(hReq2, nil)
	hRec2 := httptest.NewRecorder()
	handler.ServeHTTP(hRec2, hReq2)
	postLM := hRec2.Header().Get("Last-Modified")
	if postLM == preLM {
		t.Errorf("Last-Modified did not advance after self-REPLACE: pre=%q post=%q", preLM, postLM)
	}
}

// TestCopyObjectRejectsNoOpSelfCopy pins the AWS-spec rule that a
// self-copy (same src + same dst) with metadata-directive=COPY is
// InvalidRequest. Without the guard, clients that send the no-op
// call (bug or stale config) get a silent success that masks their
// problem. The valid in-place metadata-update flow uses
// directive=REPLACE — covered by TestCopyObjectSelfReplaceMetadata.
func TestCopyObjectRejectsNoOpSelfCopy(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "obj.txt", []byte("hello"))

	// COPY directive (default) on a self-copy → 400 InvalidRequest.
	rec := copyObject(t, handler, mount, "obj.txt", mount, "obj.txt", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-copy with COPY directive status=%d, want 400 InvalidRequest", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "InvalidRequest") {
		t.Errorf("body should mention InvalidRequest, got %s", rec.Body.String())
	}

	// REPLACE on the same self-copy is still allowed (covered more
	// fully by TestCopyObjectSelfReplaceMetadata) — this is just a
	// sanity check that the guard is COPY-specific.
	rec = copyObject(t, handler, mount, "obj.txt", mount, "obj.txt", map[string]string{
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "text/x-keep-alive",
	})
	if rec.Code != http.StatusOK {
		t.Errorf("self-copy REPLACE status=%d, want 200", rec.Code)
	}
}

// TestCopyObjectRejectsUnknownDirective: a directive other than
// COPY or REPLACE is invalid. Catches typos like "MERGE".
func TestCopyObjectRejectsUnknownDirective(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	putForTest(t, handler, mount, "src.txt", []byte("x"))

	rec := copyObject(t, handler, mount, "src.txt", mount, "dst.txt", map[string]string{
		"x-amz-metadata-directive": "MERGE",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus directive status=%d, want 400", rec.Code)
	}
}
