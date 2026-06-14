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

// TestUploadSessionRejectsSegmentAfterCommit proves that once an upload has
// been committed, replaying a segment with altered content cannot corrupt the
// persisted file.
func TestUploadSessionRejectsSegmentAfterCommit(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	content := []byte("ABCDEFGH")
	session := createUploadSession(t, r, root.Name+"/auto.bin", content, 4, false)

	for i := 0; i < 2; i++ {
		w := putSessionSegment(t, r, session.ID, i, content[i*4:(i+1)*4])
		if w.Result().StatusCode != http.StatusOK {
			t.Fatalf("seed segment %d status=%d body=%s", i, w.Result().StatusCode, w.Body.String())
		}
	}

	commit := httptest.NewRecorder()
	r.ServeHTTP(commit, authedJSONRequest(http.MethodPost, "/v1/uploads/sessions/"+session.ID+"/commit", nil))
	if commit.Result().StatusCode != http.StatusOK {
		t.Fatalf("commit status=%d body=%s", commit.Result().StatusCode, commit.Body.String())
	}

	// Try to replay segment 0 with completely different content. The upload is
	// already finalized; the server must not allow this to mutate the file.
	tampered := []byte("XXXX")
	w := putSessionSegment(t, r, session.ID, 0, tampered)
	if w.Result().StatusCode != http.StatusConflict {
		t.Fatalf("replay status=%d want=%d body=%s", w.Result().StatusCode, http.StatusConflict, w.Body.String())
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

// TestUploadSessionEnforcesQuotaWhenStorageFull simulates a full filesystem
// during resumable upload. The create endpoint must return InsufficientStorage
// rather than accept an upload that cannot fit. We use a quota-enforcing
// router built on top of a tmpfs-bound mount via UploadMinFreeBytes, which is
// the production knob for this safety property.
func TestUploadSessionEnforcesQuotaWhenStorageFull(t *testing.T) {
	baseDir := t.TempDir()
	indexDir := t.TempDir()
	r, svc, cleanup := newTestRouterWithCustomLimits(t, baseDir, indexDir, RouterOptions{
		BearerToken:           "test-token",
		MaxChunkBytes:         1 << 20,
		MaxUploadBytes:        4 << 20,
		MaxSessionUploadBytes: 4 << 20,
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
		"path":        root.Name + "/huge.bin",
		"size":        int64(1 << 20),
		"checksum":    "sha256:" + strings.Repeat("0", 64),
		"segmentSize": int64(1 << 20),
	})
	if err != nil {
		t.Fatalf("marshal start body: %v", err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/uploads/sessions", body))
	if w.Result().StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status=%d, want=%d body=%s", w.Result().StatusCode, http.StatusInsufficientStorage, w.Body.String())
	}
	// The stage root may be created before the quota check fires; that's fine.
	// What is NOT fine is leaving per-session segment files after rejection.
	staging := filepath.Join(baseDir, uploadSessionSegmentsDir, uploadSessionSegmentSubdir)
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
