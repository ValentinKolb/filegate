//go:build linux

package httpadapter

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDirectoryTarTerminatesOnSymlinkCycle ensures the tar streamer does not
// follow symlink loops indefinitely. Two directories link back to each other;
// the tar entry list must be finite and exclude the symlink entries entirely.
func TestDirectoryTarTerminatesOnSymlinkCycle(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	mount := svc.ListRoot()[0]
	baseAbs, err := svc.ResolveAbsPath(mount.ID)
	if err != nil {
		t.Fatalf("resolve mount path: %v", err)
	}

	bundle := filepath.Join(baseAbs, "cycle-bundle")
	a := filepath.Join(bundle, "A")
	b := filepath.Join(bundle, "B")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatalf("mkdir A: %v", err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatalf("mkdir B: %v", err)
	}
	if err := os.WriteFile(filepath.Join(a, "real.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write real.txt: %v", err)
	}
	// Closed cycle: A/loop -> B, B/loop -> A.
	if err := os.Symlink(b, filepath.Join(a, "loop")); err != nil {
		t.Fatalf("symlink A->B: %v", err)
	}
	if err := os.Symlink(a, filepath.Join(b, "loop")); err != nil {
		t.Fatalf("symlink B->A: %v", err)
	}
	if err := svc.Rescan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	dirID, err := svc.ResolvePath(mount.Name + "/cycle-bundle")
	if err != nil {
		t.Fatalf("resolve bundle id: %v", err)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/v1/nodes/"+dirID.String()+"/content"))
		done <- w
	}()

	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("tar stream hung on symlink cycle")
	}

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want=%d", w.Result().StatusCode, http.StatusOK)
	}

	tr := tar.NewReader(bytes.NewReader(w.Body.Bytes()))
	foundReal := false
	const maxEntries = 1000
	for i := 0; i < maxEntries; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if strings.Contains(hdr.Name, "loop") {
			t.Fatalf("tar must not contain symlink entries forming the cycle, got %q", hdr.Name)
		}
		if strings.HasSuffix(hdr.Name, "real.txt") {
			foundReal = true
		}
		if i == maxEntries-1 {
			t.Fatalf("tar stream produced more than %d entries — cycle is being followed", maxEntries)
		}
	}
	if !foundReal {
		t.Fatalf("expected real.txt entry in tar")
	}
}

// TestChunkedUploadRejectsChunkAfterAutoComplete proves that once an upload
// has been finalized, replaying a chunk with altered content cannot corrupt
// the persisted file. The server must either echo the completed file (for
// idempotent retries) or reject the change — but never re-write the file.
func TestChunkedUploadRejectsChunkAfterAutoComplete(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	content := []byte("ABCDEFGH")
	checksum := sha256Prefixed(content)

	start := chunkedStart(t, r, root.ID.String(), "auto.bin", int64(len(content)), 4, checksum)
	uploadID, _ := start["uploadId"].(string)
	if uploadID == "" {
		t.Fatalf("missing uploadId")
	}

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut,
			"/v1/uploads/chunked/"+uploadID+"/chunks/"+itoa(i),
			bytes.NewReader(content[i*4:(i+1)*4]),
		)
		req.Header.Set("Authorization", "Bearer test-token")
		r.ServeHTTP(w, req)
		if w.Result().StatusCode != http.StatusOK {
			t.Fatalf("seed chunk %d status=%d body=%s", i, w.Result().StatusCode, w.Body.String())
		}
	}

	statusW := httptest.NewRecorder()
	r.ServeHTTP(statusW, authedRequest(http.MethodGet, "/v1/uploads/chunked/"+uploadID))
	if statusW.Result().StatusCode != http.StatusOK {
		t.Fatalf("status before replay=%d", statusW.Result().StatusCode)
	}
	var complete map[string]any
	if err := json.NewDecoder(statusW.Result().Body).Decode(&complete); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if done, _ := complete["completed"].(bool); !done {
		t.Fatalf("expected completed=true before replay, got %#v", complete)
	}

	// Try to replay chunk 0 with completely different content. The upload is
	// already finalized; the server must not allow this to mutate the file.
	tampered := []byte("XXXX")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		"/v1/uploads/chunked/"+uploadID+"/chunks/0",
		bytes.NewReader(tampered),
	)
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)
	// Two outcomes are acceptable: 200 (idempotent — server returns the
	// already-completed file unchanged) or a 4xx rejection. What is NOT
	// acceptable is the file content actually changing.
	if w.Result().StatusCode >= 500 {
		t.Fatalf("replay caused 5xx: %d body=%s", w.Result().StatusCode, w.Body.String())
	}

	// The on-disk file must still match the original content. Locate it via
	// the mount and the original filename.
	rootAbs, err := svc.ResolveAbsPath(root.ID)
	if err != nil {
		t.Fatalf("resolve root abs: %v", err)
	}
	finalPath := filepath.Join(rootAbs, "auto.bin")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read finalized file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("file content was mutated by post-completion replay: got %q want %q", got, content)
	}
}

// TestChunkedUploadEnforcesQuotaWhenStorageFull simulates a full filesystem
// during chunked upload. The /start endpoint must return InsufficientStorage
// rather than accept an upload that cannot fit. We use a quota-enforcing
// router built on top of a tmpfs-bound mount via the MaxChunkedUploadBytes
// limit, which is the production knob for this safety property.
func TestChunkedUploadEnforcesQuotaWhenStorageFull(t *testing.T) {
	baseDir := t.TempDir()
	indexDir := t.TempDir()
	r, svc, cleanup := newTestRouterWithCustomLimits(t, baseDir, indexDir, RouterOptions{
		BearerToken:           "test-token",
		MaxChunkBytes:         1 << 20,
		MaxUploadBytes:        4 << 20,
		MaxChunkedUploadBytes: 4 << 20,
		// A very large min-free-bytes guarantees the start handler will
		// believe storage is exhausted even on a roomy build host.
		UploadMinFreeBytes:    1 << 62,
		UploadExpiry:          time.Hour,
		UploadCleanupInterval: time.Hour,
		Rescan:                func() error { return nil },
	})
	defer cleanup()

	root := svc.ListRoot()[0]
	body, err := json.Marshal(map[string]any{
		"parentId":  root.ID.String(),
		"filename":  "huge.bin",
		"size":      int64(1 << 20),
		"checksum":  "sha256:" + strings.Repeat("0", 64),
		"chunkSize": 1 << 20,
	})
	if err != nil {
		t.Fatalf("marshal start body: %v", err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/uploads/chunked/start", body))
	if w.Result().StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status=%d, want=%d body=%s", w.Result().StatusCode, http.StatusInsufficientStorage, w.Body.String())
	}
	// The .fg-uploads root may be created by handleStart before the quota
	// check fires; that's fine. What is NOT fine is leaving a per-upload
	// directory inside it after rejection, because that would leak quota
	// across attempts.
	staging := filepath.Join(baseDir, uploadStagingDirName)
	entries, err := os.ReadDir(staging)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat staging: %v", err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("rejected upload should not leave a per-upload staging dir, found %q in %s", e.Name(), staging)
		}
	}
}

func itoa(i int) string {
	switch i {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	}
	return "0"
}
