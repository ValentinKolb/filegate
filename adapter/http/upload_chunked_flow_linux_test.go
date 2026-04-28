//go:build linux

package httpadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func sha256Prefixed(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func chunkedStart(t *testing.T, r http.Handler, parentID, filename string, size, chunkSize int64, checksum string) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"parentId":  parentID,
		"filename":  filename,
		"size":      size,
		"checksum":  checksum,
		"chunkSize": chunkSize,
	})
	if err != nil {
		t.Fatalf("marshal start body: %v", err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/uploads/chunked/start", body))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("start status=%d body=%s", w.Result().StatusCode, w.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&out); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	return out
}

func TestChunkedUploadOutOfOrderDuplicateAndAutoComplete(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	content := []byte("abcdefghijkl")
	checksum := sha256Prefixed(content)

	start := chunkedStart(t, r, root.ID.String(), "doc.bin", int64(len(content)), 4, checksum)
	uploadID, _ := start["uploadId"].(string)
	if uploadID == "" {
		t.Fatalf("missing uploadId")
	}

	chunks := [][]byte{content[0:4], content[4:8], content[8:12]}

	// Out-of-order chunk #2
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPut, "/v1/uploads/chunked/"+uploadID+"/chunks/2", bytes.NewReader(chunks[2]))
	req2.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w2, req2)
	if w2.Result().StatusCode != http.StatusOK {
		t.Fatalf("chunk2 status=%d body=%s", w2.Result().StatusCode, w2.Body.String())
	}

	// Chunk #0
	w0 := httptest.NewRecorder()
	req0 := httptest.NewRequest(http.MethodPut, "/v1/uploads/chunked/"+uploadID+"/chunks/0", bytes.NewReader(chunks[0]))
	req0.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w0, req0)
	if w0.Result().StatusCode != http.StatusOK {
		t.Fatalf("chunk0 status=%d body=%s", w0.Result().StatusCode, w0.Body.String())
	}

	// Duplicate chunk #0 (same content) should be idempotent and still OK.
	w0dup := httptest.NewRecorder()
	req0dup := httptest.NewRequest(http.MethodPut, "/v1/uploads/chunked/"+uploadID+"/chunks/0", bytes.NewReader(chunks[0]))
	req0dup.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w0dup, req0dup)
	if w0dup.Result().StatusCode != http.StatusOK {
		t.Fatalf("chunk0 duplicate status=%d body=%s", w0dup.Result().StatusCode, w0dup.Body.String())
	}

	// Final chunk #1 triggers server auto-complete.
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPut, "/v1/uploads/chunked/"+uploadID+"/chunks/1", bytes.NewReader(chunks[1]))
	req1.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w1, req1)
	if w1.Result().StatusCode != http.StatusOK {
		t.Fatalf("chunk1 status=%d body=%s", w1.Result().StatusCode, w1.Body.String())
	}

	var complete map[string]any
	if err := json.NewDecoder(w1.Result().Body).Decode(&complete); err != nil {
		t.Fatalf("decode complete: %v", err)
	}
	if ok, _ := complete["completed"].(bool); !ok {
		t.Fatalf("expected completed=true, got %#v", complete)
	}

	fileObj, _ := complete["file"].(map[string]any)
	if fileObj == nil {
		t.Fatalf("missing file object: %#v", complete)
	}
	if got, _ := fileObj["checksum"].(string); got != checksum {
		t.Fatalf("checksum=%q want=%q", got, checksum)
	}

	id, _ := fileObj["id"].(string)
	if id == "" {
		t.Fatalf("missing id in file object: %#v", fileObj)
	}

	download := httptest.NewRecorder()
	r.ServeHTTP(download, authedRequest(http.MethodGet, "/v1/nodes/"+id+"/content"))
	if download.Result().StatusCode != http.StatusOK {
		t.Fatalf("download status=%d", download.Result().StatusCode)
	}
	if !bytes.Equal(download.Body.Bytes(), content) {
		t.Fatalf("downloaded content mismatch")
	}

	status := httptest.NewRecorder()
	r.ServeHTTP(status, authedRequest(http.MethodGet, "/v1/uploads/chunked/"+uploadID))
	if status.Result().StatusCode != http.StatusOK {
		t.Fatalf("status after complete=%d body=%s", status.Result().StatusCode, status.Body.String())
	}
	var st map[string]any
	if err := json.NewDecoder(status.Result().Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if done, _ := st["completed"].(bool); !done {
		t.Fatalf("expected completed status=true got %#v", st)
	}

	rootAbs, err := svc.ResolveAbsPath(root.ID)
	if err != nil {
		t.Fatalf("resolve root abs: %v", err)
	}
	manifest := filepath.Join(rootAbs, uploadStagingDirName, uploadID, uploadManifestFileName)
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("expected manifest at %s: %v", manifest, err)
	}
}

func TestChunkedUploadRejectsDuplicateChunkWithDifferentContent(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	content := []byte("abcdefgh")
	checksum := sha256Prefixed(content)

	start := chunkedStart(t, r, root.ID.String(), "dup.bin", int64(len(content)), 4, checksum)
	uploadID, _ := start["uploadId"].(string)
	if uploadID == "" {
		t.Fatalf("missing uploadId")
	}

	w0 := httptest.NewRecorder()
	req0 := httptest.NewRequest(http.MethodPut, "/v1/uploads/chunked/"+uploadID+"/chunks/0", bytes.NewReader(content[0:4]))
	req0.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w0, req0)
	if w0.Result().StatusCode != http.StatusOK {
		t.Fatalf("first chunk status=%d body=%s", w0.Result().StatusCode, w0.Body.String())
	}

	bad := []byte("WXYZ")
	wdup := httptest.NewRecorder()
	reqdup := httptest.NewRequest(http.MethodPut, "/v1/uploads/chunked/"+uploadID+"/chunks/0", bytes.NewReader(bad))
	reqdup.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(wdup, reqdup)
	if wdup.Result().StatusCode != http.StatusConflict {
		t.Fatalf("duplicate different chunk status=%d body=%s", wdup.Result().StatusCode, wdup.Body.String())
	}
}
