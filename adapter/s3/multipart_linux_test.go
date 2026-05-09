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
