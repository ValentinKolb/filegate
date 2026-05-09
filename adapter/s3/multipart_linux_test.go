//go:build linux

package s3

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/valentinkolb/filegate/domain"
)

// initMultipart helper: POST ?uploads → returns uploadId and asserts
// success.
func initMultipart(t *testing.T, handler http.Handler, mount, key string, headers map[string]string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/"+mount+"/"+key+"?uploads", nil)
	req.Host = "example.com"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateMultipartUpload status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res initiateMultipartUploadResult
	if err := xml.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("XML decode: %v", err)
	}
	if res.UploadID == "" {
		t.Fatalf("empty UploadId")
	}
	return res.UploadID
}

// uploadPart helper: PUT ?partNumber=N&uploadId=X → returns ETag.
func uploadPart(t *testing.T, handler http.Handler, mount, key, uploadID string, partNumber int, body []byte) string {
	t.Helper()
	url := fmt.Sprintf("/%s/%s?partNumber=%d&uploadId=%s", mount, key, partNumber, uploadID)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("UploadPart part=%d status=%d body=%s", partNumber, rec.Code, rec.Body.String())
	}
	return strings.Trim(rec.Header().Get("ETag"), `"`)
}

// TestCreateMultipartUploadRoundTrip: CreateMultipartUpload returns
// uploadId; the on-disk staging dir + manifest are present after.
func TestCreateMultipartUploadRoundTrip(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "obj.bin", map[string]string{
		"Content-Type":      "application/octet-stream",
		"x-amz-meta-author": "alice",
	})

	// Verify on-disk shape: <mountAbs>/.fg-uploads/s3-<uploadID>/manifest.json
	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)
	if _, err := os.Stat(stageDir); err != nil {
		t.Fatalf("stage dir missing: %v", err)
	}
	manifest, err := readManifest(stageDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Bucket != mount || manifest.Key != "obj.bin" {
		t.Errorf("manifest bucket/key=%q/%q, want %q/obj.bin", manifest.Bucket, manifest.Key, mount)
	}
	if manifest.ContentType != "application/octet-stream" {
		t.Errorf("manifest ContentType=%q", manifest.ContentType)
	}
	if got := manifest.UserMetadata["author"]; got != "alice" {
		t.Errorf("UserMetadata[author]=%q, want alice", got)
	}
	if manifest.Phase != phaseInProgress {
		t.Errorf("Phase=%q, want in_progress", manifest.Phase)
	}
}

// TestUploadPartStoresMD5: each UploadPart writes its part body
// + records the part-MD5 in the manifest. Duplicate UploadPart for
// same partNumber overwrites.
func TestUploadPartStoresMD5(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)

	// Part 1
	p1 := []byte("part-one-bytes")
	p1MD5 := md5.Sum(p1)
	gotETag := uploadPart(t, handler, mount, "k.bin", uploadID, 1, p1)
	if gotETag != hex.EncodeToString(p1MD5[:]) {
		t.Errorf("part1 ETag=%q, want %q", gotETag, hex.EncodeToString(p1MD5[:]))
	}

	// Part 2
	p2 := []byte("part-two-bytes-different")
	uploadPart(t, handler, mount, "k.bin", uploadID, 2, p2)

	// Manifest should reflect both
	m, _ := readManifest(stageDir)
	if len(m.Parts) != 2 {
		t.Fatalf("manifest parts=%d, want 2", len(m.Parts))
	}
	if got := m.Parts[1].ETag; got != hex.EncodeToString(p1MD5[:]) {
		t.Errorf("manifest Parts[1].ETag=%q", got)
	}
	if got := m.Parts[1].Size; got != int64(len(p1)) {
		t.Errorf("manifest Parts[1].Size=%d, want %d", got, len(p1))
	}

	// Duplicate UploadPart for partNumber=1 with different bytes →
	// overwrites.
	p1New := []byte("DIFFERENT-PART-ONE-BYTES-NOW")
	p1NewMD5 := md5.Sum(p1New)
	gotNewETag := uploadPart(t, handler, mount, "k.bin", uploadID, 1, p1New)
	if gotNewETag != hex.EncodeToString(p1NewMD5[:]) {
		t.Errorf("duplicate part1 ETag=%q", gotNewETag)
	}
	m, _ = readManifest(stageDir)
	if got := m.Parts[1].ETag; got != hex.EncodeToString(p1NewMD5[:]) {
		t.Errorf("manifest after duplicate Parts[1].ETag=%q, want updated", got)
	}
}

// TestUploadPartContentMD5: client-supplied Content-MD5 must match
// the body; mismatch returns BadDigest and doesn't leak a part file.
func TestUploadPartContentMD5(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	body := []byte("real body")
	wrongMD5 := md5.Sum([]byte("DIFFERENT"))
	wrongB64 := base64.StdEncoding.EncodeToString(wrongMD5[:])

	url := fmt.Sprintf("/%s/k.bin?partNumber=1&uploadId=%s", mount, uploadID)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Host = "example.com"
	req.Header.Set("Content-MD5", wrongB64)
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong Content-MD5 UploadPart status=%d, want 400 (BadDigest)", rec.Code)
	}

	// Manifest should still have 0 parts.
	mountAbs := lookupMountAbs(t, handler, mount)
	m, _ := readManifest(stageDirFor(mountAbs, uploadID))
	if len(m.Parts) != 0 {
		t.Errorf("after bad-digest UploadPart manifest has %d parts", len(m.Parts))
	}
}

// TestUploadPartRejectsBadPartNumber: out-of-range partNumber → 400.
func TestUploadPartRejectsBadPartNumber(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	for _, n := range []string{"0", "10001", "abc", "-1"} {
		url := fmt.Sprintf("/%s/k.bin?partNumber=%s&uploadId=%s", mount, n, uploadID)
		req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("x")))
		req.Host = "example.com"
		signRequestPayload(req, []byte("x"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("partNumber=%s status=%d, want 400", n, rec.Code)
		}
	}
}

// TestListParts: ListParts returns parts in ascending PartNumber
// order with stored ETags.
func TestListParts(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	uploadPart(t, handler, mount, "k.bin", uploadID, 3, []byte("p3"))
	uploadPart(t, handler, mount, "k.bin", uploadID, 1, []byte("p1"))
	uploadPart(t, handler, mount, "k.bin", uploadID, 2, []byte("p2"))

	url := fmt.Sprintf("/%s/k.bin?uploadId=%s", mount, uploadID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListParts status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res listPartsResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Parts) != 3 {
		t.Fatalf("Parts count=%d, want 3", len(res.Parts))
	}
	for i, want := range []int{1, 2, 3} {
		if res.Parts[i].PartNumber != want {
			t.Errorf("Parts[%d].PartNumber=%d, want %d", i, res.Parts[i].PartNumber, want)
		}
	}
}

// TestListMultipartUploads: lists all in-progress uploads in the
// bucket, sorted oldest-first.
func TestListMultipartUploads(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	u1 := initMultipart(t, handler, mount, "first.bin", nil)
	u2 := initMultipart(t, handler, mount, "second.bin", nil)

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?uploads", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListMultipartUploads status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res listMultipartUploadsResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Uploads) != 2 {
		t.Fatalf("Uploads=%d, want 2", len(res.Uploads))
	}
	// oldest first → u1 before u2 (unless they shared the same ms
	// timestamp; t.UnixMilli has ms resolution so it's possible.
	// just verify both ids are present)
	got := map[string]bool{}
	for _, u := range res.Uploads {
		got[u.UploadID] = true
	}
	if !got[u1] || !got[u2] {
		t.Errorf("missing upload id in listing: have %v", got)
	}
}

// TestAbortMultipartUpload: removes the staging dir; idempotent on
// a missing upload.
func TestAbortMultipartUpload(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "doomed.bin", nil)
	uploadPart(t, handler, mount, "doomed.bin", uploadID, 1, []byte("x"))

	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)
	if _, err := os.Stat(stageDir); err != nil {
		t.Fatalf("stage dir should exist before abort: %v", err)
	}

	url := fmt.Sprintf("/%s/doomed.bin?uploadId=%s", mount, uploadID)
	req := httptest.NewRequest(http.MethodDelete, url, nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Abort status=%d, want 204", rec.Code)
	}
	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		t.Errorf("stage dir should be gone after abort, got err=%v", err)
	}

	// Idempotent: second abort same uploadId → still 204.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, url, nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("idempotent Abort status=%d, want 204", rec.Code)
	}
}

// TestUploadPartUnknownUploadId: UploadPart with an uploadId that
// doesn't exist returns NoSuchUpload (NoSuchKey 404 in our wire
// for now; AWS uses NoSuchUpload but the HTTP status is the same).
func TestUploadPartUnknownUploadId(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	url := fmt.Sprintf("/%s/x.bin?partNumber=1&uploadId=00000000000000000000000000000000", mount)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("x")))
	req.Host = "example.com"
	signRequestPayload(req, []byte("x"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown uploadId status=%d, want 404", rec.Code)
	}
}

// completeMultipart helper: POST ?uploadId=X with the given parts
// XML and returns the response recorder.
func completeMultipart(t *testing.T, handler http.Handler, mount, key, uploadID string, parts []completeRequestPart) *httptest.ResponseRecorder {
	t.Helper()
	body := completeMultipartUploadRequest{Parts: parts}
	raw, err := xml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal complete body: %v", err)
	}
	url := fmt.Sprintf("/%s/%s?uploadId=%s", mount, key, uploadID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Host = "example.com"
	signRequestPayload(req, raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// makePartBody returns a deterministic byte slice of the given size.
// Pattern is repeating "P<n>." so different parts produce different
// MD5s even at identical sizes.
func makePartBody(partNum, size int) []byte {
	out := make([]byte, size)
	tag := byte('A' + (partNum-1)%26)
	for i := range out {
		out[i] = tag
	}
	return out
}

// expectedCompositeETag computes the composite ETag the same way
// Complete does: hex(MD5(concat-of-part-md5-bytes)) + "-N".
func expectedCompositeETag(parts [][]byte) string {
	composite := md5.New()
	for _, p := range parts {
		s := md5.Sum(p)
		composite.Write(s[:])
	}
	return fmt.Sprintf("%s-%d", hex.EncodeToString(composite.Sum(nil)), len(parts))
}

// TestCompleteMultipartUploadRoundTrip pins the happy path: two
// 5 MiB parts + one small final part → Complete succeeds, returns
// composite ETag, GET returns assembled body with same ETag.
func TestCompleteMultipartUploadRoundTrip(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "big.bin", map[string]string{
		"Content-Type":      "application/octet-stream",
		"x-amz-meta-author": "alice",
	})

	const minSize = 5 * 1024 * 1024
	p1 := makePartBody(1, minSize)
	p2 := makePartBody(2, minSize)
	p3 := makePartBody(3, 1024) // last part can be small

	e1 := uploadPart(t, handler, mount, "big.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "big.bin", uploadID, 2, p2)
	e3 := uploadPart(t, handler, mount, "big.bin", uploadID, 3, p3)

	rec := completeMultipart(t, handler, mount, "big.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
		{PartNumber: 3, ETag: e3},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Complete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res completeMultipartUploadResult
	if err := xml.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode complete result: %v", err)
	}
	wantETag := expectedCompositeETag([][]byte{p1, p2, p3})
	gotETag := strings.Trim(res.ETag, `"`)
	if gotETag != wantETag {
		t.Errorf("composite ETag=%q, want %q", gotETag, wantETag)
	}
	if res.Bucket != mount || res.Key != "big.bin" {
		t.Errorf("Bucket/Key=%q/%q", res.Bucket, res.Key)
	}

	// GET should return the assembled body and the same composite ETag.
	getReq := httptest.NewRequest(http.MethodGet, "/"+mount+"/big.bin", nil)
	getReq.Host = "example.com"
	signRequestPayload(getReq, nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET after Complete status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	gotBody := getRec.Body.Bytes()
	wantBody := append(append(append([]byte{}, p1...), p2...), p3...)
	if !bytes.Equal(gotBody, wantBody) {
		t.Errorf("GET body length=%d, want %d", len(gotBody), len(wantBody))
	}
	if got := strings.Trim(getRec.Header().Get("ETag"), `"`); got != wantETag {
		t.Errorf("GET ETag=%q, want %q", got, wantETag)
	}
	if got := getRec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("GET Content-Type=%q", got)
	}
	if got := getRec.Header().Get("X-Amz-Meta-Author"); got != "alice" {
		t.Errorf("GET x-amz-meta-author=%q, want alice", got)
	}
}

// TestCompleteMultipartUploadIdempotent: a retried Complete with the
// same uploadId returns the same result (composite ETag) without
// re-doing the install.
func TestCompleteMultipartUploadIdempotent(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "obj.bin", nil)
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 256)
	e1 := uploadPart(t, handler, mount, "obj.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "obj.bin", uploadID, 2, p2)

	parts := []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	}

	first := completeMultipart(t, handler, mount, "obj.bin", uploadID, parts)
	if first.Code != http.StatusOK {
		t.Fatalf("first Complete status=%d body=%s", first.Code, first.Body.String())
	}
	var firstRes completeMultipartUploadResult
	_ = xml.Unmarshal(first.Body.Bytes(), &firstRes)

	second := completeMultipart(t, handler, mount, "obj.bin", uploadID, parts)
	if second.Code != http.StatusOK {
		t.Fatalf("second Complete status=%d body=%s", second.Code, second.Body.String())
	}
	var secondRes completeMultipartUploadResult
	_ = xml.Unmarshal(second.Body.Bytes(), &secondRes)
	if firstRes.ETag != secondRes.ETag {
		t.Errorf("retry ETag=%q, first=%q (must be identical)", secondRes.ETag, firstRes.ETag)
	}
}

// TestCompleteMultipartUploadInvalidPart: Complete fails when a
// referenced PartNumber wasn't uploaded.
func TestCompleteMultipartUploadInvalidPart(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	uploadPart(t, handler, mount, "k.bin", uploadID, 1, makePartBody(1, 5*1024*1024))

	rec := completeMultipart(t, handler, mount, "k.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: "deadbeefdeadbeefdeadbeefdeadbeef"},  // wrong ETag
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-etag Complete status=%d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "InvalidPart") {
		t.Errorf("body should mention InvalidPart, got %s", rec.Body.String())
	}
}

// TestCompleteMultipartUploadOutOfOrder: parts in descending order
// → InvalidPartOrder.
func TestCompleteMultipartUploadOutOfOrder(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 5*1024*1024)
	e1 := uploadPart(t, handler, mount, "k.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "k.bin", uploadID, 2, p2)

	rec := completeMultipart(t, handler, mount, "k.bin", uploadID, []completeRequestPart{
		{PartNumber: 2, ETag: e2},
		{PartNumber: 1, ETag: e1},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("descending parts Complete status=%d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "InvalidPartOrder") {
		t.Errorf("body should mention InvalidPartOrder, got %s", rec.Body.String())
	}
}

// TestCompleteMultipartUploadEntityTooSmall: a non-final part under
// 5 MiB → EntityTooSmall.
func TestCompleteMultipartUploadEntityTooSmall(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "k.bin", nil)
	tinyP1 := makePartBody(1, 1024) // << 5 MiB
	tailP2 := makePartBody(2, 1024)
	e1 := uploadPart(t, handler, mount, "k.bin", uploadID, 1, tinyP1)
	e2 := uploadPart(t, handler, mount, "k.bin", uploadID, 2, tailP2)

	rec := completeMultipart(t, handler, mount, "k.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("small-part Complete status=%d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "EntityTooSmall") {
		t.Errorf("body should mention EntityTooSmall, got %s", rec.Body.String())
	}
}

// TestCompleteMultipartUploadSinglePart: a single small part (the
// final and only part) is allowed — the 5 MiB rule applies only to
// non-final parts.
func TestCompleteMultipartUploadSinglePart(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "tiny.bin", nil)
	p1 := makePartBody(1, 1024)
	e1 := uploadPart(t, handler, mount, "tiny.bin", uploadID, 1, p1)

	rec := completeMultipart(t, handler, mount, "tiny.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("single-part Complete status=%d body=%s", rec.Code, rec.Body.String())
	}
	wantETag := expectedCompositeETag([][]byte{p1})
	var res completeMultipartUploadResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if got := strings.Trim(res.ETag, `"`); got != wantETag {
		t.Errorf("ETag=%q, want %q", got, wantETag)
	}
}

// TestCompleteMultipartUploadUnknownUploadID: a Complete with an
// uploadId that doesn't exist returns NoSuchUpload.
func TestCompleteMultipartUploadUnknownUploadID(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	rec := completeMultipart(t, handler, mount, "x.bin", "00000000000000000000000000000000",
		[]completeRequestPart{{PartNumber: 1, ETag: "deadbeefdeadbeefdeadbeefdeadbeef"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown uploadId Complete status=%d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "NoSuchUpload") {
		t.Errorf("body should mention NoSuchUpload, got %s", rec.Body.String())
	}
}

// TestCompleteMultipartUploadCleansStagingOnSuccess: after a
// successful Complete, parts/ and complete.tmp are gone — only
// the manifest survives for the idempotent-retry short-circuit.
// Pre-fix the parts directory leaked permanently, doubling the
// effective storage of every multipart upload.
func TestCompleteMultipartUploadCleansStagingOnSuccess(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "obj.bin", nil)
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 1024)
	e1 := uploadPart(t, handler, mount, "obj.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "obj.bin", uploadID, 2, p2)

	rec := completeMultipart(t, handler, mount, "obj.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Complete status=%d body=%s", rec.Code, rec.Body.String())
	}

	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)

	// parts/ must be gone.
	partsDir := stageDir + "/" + multipartPartsDirName
	if _, err := os.Stat(partsDir); !os.IsNotExist(err) {
		t.Errorf("parts dir should be removed after Complete, got err=%v", err)
	}
	// complete.tmp must be gone.
	if _, err := os.Stat(stageDir + "/" + multipartCompleteTmp); !os.IsNotExist(err) {
		t.Errorf("complete.tmp should be removed after Complete, got err=%v", err)
	}
	// Manifest must still exist (idempotent retry needs it).
	got, err := readManifest(stageDir)
	if err != nil {
		t.Fatalf("manifest should remain after Complete: %v", err)
	}
	if got.Phase != phaseDone {
		t.Errorf("manifest Phase=%q, want done", got.Phase)
	}

	// Idempotent retry still works (the phaseDone short-circuit reads
	// the surviving manifest, not the deleted parts).
	rec2 := completeMultipart(t, handler, mount, "obj.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("retry after staging cleanup status=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestUploadPartRejectedDuringCommit: once Complete starts (manifest
// flipped to phase=committing), concurrent UploadPart for the same
// uploadId must be rejected. Pre-fix UploadPart could overwrite a
// part-file between Complete's validate and concat steps, breaking
// the composite-ETag invariant.
func TestUploadPartRejectedDuringCommit(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "obj.bin", nil)
	uploadPart(t, handler, mount, "obj.bin", uploadID, 1, makePartBody(1, 5*1024*1024))

	// Force the manifest into committing — exactly the state Complete
	// installs before validating.
	mountAbs := lookupMountAbs(t, handler, mount)
	m, _ := readManifest(stageDirFor(mountAbs, uploadID))
	m.Phase = phaseCommitting
	if err := writeManifest(stageDirFor(mountAbs, uploadID), m); err != nil {
		t.Fatalf("force committing manifest: %v", err)
	}

	body := []byte("racey")
	url := fmt.Sprintf("/%s/obj.bin?partNumber=2&uploadId=%s", mount, uploadID)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("UploadPart accepted while phase=committing — race not closed")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("UploadPart during committing status=%d, want 400 InvalidRequest", rec.Code)
	}
}

// TestMultipartETagSurvivesDetectorResync pins the regression
// surfaced by the M4 Docker smoke test: a poll-detector cycle that
// re-syncs a freshly-multipart-uploaded file used to clobber the
// composite ETag (and every other S3-extension field) by running
// the entity through buildEntityMetadata, which has no concept
// of S3 fields. Pre-fix HEAD returned an empty ETag a few seconds
// after Complete; post-fix the composite survives detector
// re-syncs as long as the on-disk size + mtime + inode haven't
// changed.
func TestMultipartETagSurvivesDetectorResync(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "obj.bin", map[string]string{
		"Content-Type":      "image/png",
		"x-amz-meta-author": "alice",
	})
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 1024)
	e1 := uploadPart(t, handler, mount, "obj.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "obj.bin", uploadID, 2, p2)
	rec := completeMultipart(t, handler, mount, "obj.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Complete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var compRes completeMultipartUploadResult
	_ = xml.Unmarshal(rec.Body.Bytes(), &compRes)
	wantETag := strings.Trim(compRes.ETag, `"`)
	if !strings.Contains(wantETag, "-") {
		t.Fatalf("Complete returned non-composite ETag %q — test setup wrong", wantETag)
	}

	// Simulate a detector poll touching the file.
	mountAbs := lookupMountAbs(t, handler, mount)
	if err := svc.SyncAbsPath(mountAbs + "/obj.bin"); err != nil {
		t.Fatalf("SyncAbsPath: %v", err)
	}

	// HEAD should still report the composite ETag + S3 metadata.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/obj.bin", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	if hRec.Code != http.StatusOK {
		t.Fatalf("HEAD after sync status=%d", hRec.Code)
	}
	gotETag := strings.Trim(hRec.Header().Get("ETag"), `"`)
	if gotETag != wantETag {
		t.Errorf("HEAD ETag=%q after detector sync, want preserved %q", gotETag, wantETag)
	}
	if got := hRec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("HEAD Content-Type=%q after detector sync, want preserved image/png", got)
	}
	if got := hRec.Header().Get("X-Amz-Meta-Author"); got != "alice" {
		t.Errorf("HEAD x-amz-meta-author=%q after detector sync, want preserved 'alice'", got)
	}
}

// TestRecoverCommittingManifestNoRecord: a manifest in phase=committing
// with no durable record is left untouched (the client will retry
// Complete to redrive the install).
func TestRecoverCommittingManifestNoRecord(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	uploadID := initMultipart(t, handler, mount, "stuck.bin", nil)
	uploadPart(t, handler, mount, "stuck.bin", uploadID, 1, makePartBody(1, 5*1024*1024))

	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)
	m, _ := readManifest(stageDir)
	m.Phase = phaseCommitting
	m.CompositeETag = "fake-etag-1"
	if err := writeManifest(stageDir, m); err != nil {
		t.Fatalf("force committing manifest: %v", err)
	}

	recoverPendingMultipartUploads(svc)

	// Manifest should still be in committing — no durable record exists.
	got, err := readManifest(stageDir)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if got.Phase != phaseCommitting {
		t.Errorf("Phase=%q after recovery, want committing (no record means client retry redrives)", got.Phase)
	}
}

// TestRecoverCommittingManifestWithRecord: a manifest in phase=committing
// whose durable record DOES exist is promoted to phase=done so listing
// stops showing it as in-progress.
func TestRecoverCommittingManifestWithRecord(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Run a complete Complete to land a durable record.
	uploadID := initMultipart(t, handler, mount, "obj.bin", nil)
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 1024)
	e1 := uploadPart(t, handler, mount, "obj.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "obj.bin", uploadID, 2, p2)
	rec := completeMultipart(t, handler, mount, "obj.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Complete status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Simulate a crash that only updated phase to committing (not done).
	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)
	m, _ := readManifest(stageDir)
	m.Phase = phaseCommitting
	m.WholeBodyMD5 = ""
	m.CompletedFileID = ""
	m.CompletedAt = 0
	if err := writeManifest(stageDir, m); err != nil {
		t.Fatalf("force committing manifest: %v", err)
	}

	recoverPendingMultipartUploads(svc)

	got, err := readManifest(stageDir)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if got.Phase != phaseDone {
		t.Errorf("Phase=%q after recovery, want done", got.Phase)
	}
	if got.CompletedFileID == "" {
		t.Errorf("CompletedFileID should be backfilled from durable record")
	}
}

// TestCompleteMultipartUploadOverwritesExisting: Complete onto a
// pre-existing object overwrites it, preserving the original fileID.
func TestCompleteMultipartUploadOverwritesExisting(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Pre-write an object via PUT so it has a stable fileID.
	body := []byte("v1")
	url := "/" + mount + "/obj.bin"
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT status=%d", rec.Code)
	}
	originalID, err := svc.ResolvePath("/" + mount + "/obj.bin")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}

	// Now do a multipart Complete with new bytes.
	uploadID := initMultipart(t, handler, mount, "obj.bin", nil)
	p1 := makePartBody(1, 5*1024*1024)
	p2 := makePartBody(2, 1024)
	e1 := uploadPart(t, handler, mount, "obj.bin", uploadID, 1, p1)
	e2 := uploadPart(t, handler, mount, "obj.bin", uploadID, 2, p2)

	cRec := completeMultipart(t, handler, mount, "obj.bin", uploadID, []completeRequestPart{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if cRec.Code != http.StatusOK {
		t.Fatalf("overwrite Complete status=%d body=%s", cRec.Code, cRec.Body.String())
	}

	// FileID should be preserved across the overwrite.
	newID, err := svc.ResolvePath("/" + mount + "/obj.bin")
	if err != nil {
		t.Fatalf("ResolvePath after Complete: %v", err)
	}
	if newID != originalID {
		t.Errorf("fileID after overwrite=%v, want preserved %v", newID, originalID)
	}
}

// helpers

// lookupMountAbs reads the mount's abs path from the test service.
// The handler doesn't expose it; in tests we pass svc to the helper
// as a workaround. The newTestServer setup stashes the svc into
// testSvcGlobal for tests that don't want to thread it through
// every helper call.
func lookupMountAbs(t *testing.T, _ http.Handler, mount string) string {
	t.Helper()
	if testSvcGlobal == nil {
		t.Fatalf("testSvcGlobal not set; helpers must run after newTestServer")
	}
	for _, root := range testSvcGlobal.ListRoot() {
		if root.Name == mount {
			abs, err := testSvcGlobal.ResolveAbsPath(root.ID)
			if err != nil {
				t.Fatalf("ResolveAbsPath: %v", err)
			}
			return abs
		}
	}
	t.Fatalf("mount %q not found", mount)
	return ""
}

// testSvcGlobal: set by newTestServer so multipart tests can look
// up the mount-abs path without threading svc through every helper.
// Tests run sequentially in this package so the global is safe.
var testSvcGlobal *domain.Service
