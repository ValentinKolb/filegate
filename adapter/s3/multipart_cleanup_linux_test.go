//go:build linux

package s3

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestManifest stages a manifest at the test mount's
// .fg-uploads/s3-<id> dir so the cleanup tests can pin every
// (phase, age) combination without going through the full
// multipart write path.
func writeTestManifest(t *testing.T, mountAbs string, m multipartManifest) string {
	t.Helper()
	if m.UploadID == "" {
		var b [16]byte
		_, _ = rand.Read(b[:])
		m.UploadID = hex.EncodeToString(b[:])
	}
	if m.Format == 0 {
		m.Format = multipartManifestFormat
	}
	if m.Kind == "" {
		m.Kind = multipartManifestKind
	}
	if m.Parts == nil {
		m.Parts = map[int]multipartPart{}
	}
	stageDir := stageDirFor(mountAbs, m.UploadID)
	if err := os.MkdirAll(filepath.Join(stageDir, multipartPartsDirName), 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := writeManifest(stageDir, &m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return m.UploadID
}

// TestDecideRetentionPolicy exhaustively pins the cleanup-policy
// matrix without filesystem state. Each row is (phase, age,
// expected action). A regression in the policy logic surfaces
// here without needing to drive the whole sweep.
func TestDecideRetentionPolicy(t *testing.T) {
	cfg := MultipartCleanupConfig{
		DoneRetention:     24 * time.Hour,
		AbortedRetention:  1 * time.Hour,
		StuckUploadMaxAge: 7 * 24 * time.Hour,
		Interval:          time.Hour,
	}
	now := time.Now()
	ms := func(t time.Time) int64 { return t.UnixMilli() }

	cases := []struct {
		name string
		m    multipartManifest
		want cleanupAction
	}{
		{"done within retention → keep",
			multipartManifest{Phase: phaseDone, CompletedAt: ms(now.Add(-1 * time.Hour))},
			cleanupKeep},
		{"done past retention → retire",
			multipartManifest{Phase: phaseDone, CompletedAt: ms(now.Add(-25 * time.Hour))},
			cleanupRetireDone},
		{"done with zero CompletedAt → keep (don't trip on bad data)",
			multipartManifest{Phase: phaseDone, CompletedAt: 0},
			cleanupKeep},
		{"aborted within retention → keep",
			multipartManifest{Phase: phaseAborted, Initiated: ms(now.Add(-30 * time.Minute))},
			cleanupKeep},
		{"aborted past retention via Initiated fallback → retire",
			multipartManifest{Phase: phaseAborted, Initiated: ms(now.Add(-2 * time.Hour))},
			cleanupRetireAborted},
		{"aborted with CompletedAt set → use that",
			multipartManifest{Phase: phaseAborted, CompletedAt: ms(now.Add(-2 * time.Hour))},
			cleanupRetireAborted},
		{"in_progress fresh → keep",
			multipartManifest{Phase: phaseInProgress, Initiated: ms(now.Add(-1 * time.Hour))},
			cleanupKeep},
		{"in_progress stuck → force-abort",
			multipartManifest{Phase: phaseInProgress, Initiated: ms(now.Add(-8 * 24 * time.Hour))},
			cleanupForceAbort},
		{"committing stuck → force-abort (transient phase doesn't survive 7d)",
			multipartManifest{Phase: phaseCommitting, Initiated: ms(now.Add(-8 * 24 * time.Hour))},
			cleanupForceAbort},
		{"committing fresh → keep (recovery sweep handles it)",
			multipartManifest{Phase: phaseCommitting, Initiated: ms(now.Add(-1 * time.Hour))},
			cleanupKeep},
		{"in_progress with zero Initiated → keep (don't trip on bad data)",
			multipartManifest{Phase: phaseInProgress, Initiated: 0},
			cleanupKeep},
	}
	for _, c := range cases {
		got := decideRetention(&c.m, now, cfg)
		if got != c.want {
			t.Errorf("%s: got=%d want=%d", c.name, got, c.want)
		}
	}
}

// TestSweepRetiresDoneManifest: a phase=done manifest past
// DoneRetention is removed AND its durable Pebble record is
// deleted. Critical: a stale 0x07 record without a manifest is
// NOT recoverable (recovery looks at manifests first), so we
// must delete both atomically-ish.
func TestSweepRetiresDoneManifest(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	// Run a real multipart Complete to get a durable record.
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
		t.Fatalf("Complete status=%d", rec.Code)
	}

	mountAbs := lookupMountAbs(t, handler, mount)
	stageDir := stageDirFor(mountAbs, uploadID)

	// Force CompletedAt back so the manifest looks expired.
	m, _ := readManifest(stageDir)
	m.CompletedAt = time.Now().Add(-48 * time.Hour).UnixMilli()
	if err := writeManifest(stageDir, m); err != nil {
		t.Fatalf("force expired: %v", err)
	}

	// Sanity: durable record exists pre-sweep.
	uploadIDBytes, _ := decodeUploadID(uploadID)
	if rec, err := svc.LookupMultipartUploadRecord(uploadIDBytes); err != nil || rec == nil {
		t.Fatalf("durable record should exist pre-sweep, err=%v rec=%v", err, rec)
	}

	res := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if res.DoneRetired != 1 {
		t.Errorf("DoneRetired=%d, want 1", res.DoneRetired)
	}
	if res.Errors != 0 {
		t.Errorf("Errors=%d, want 0", res.Errors)
	}
	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		t.Errorf("stage dir should be gone after retire, err=%v", err)
	}
	if rec, err := svc.LookupMultipartUploadRecord(uploadIDBytes); err == nil && rec != nil {
		t.Errorf("durable record should be deleted, got %+v", rec)
	}

	// Object itself must STILL exist — cleanup retires the
	// idempotency artifact, not the actual data.
	hReq := httptest.NewRequest(http.MethodHead, "/"+mount+"/obj.bin", nil)
	hReq.Host = "example.com"
	signRequestPayload(hReq, nil)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	if hRec.Code != http.StatusOK {
		t.Errorf("HEAD on object after cleanup status=%d, want 200 — cleanup must not delete the actual object", hRec.Code)
	}
}

// TestSweepRetiresAbortedManifest: phaseAborted manifests past
// AbortedRetention are removed. AbortMultipartUpload already
// deletes parts/, so the manifest is the only artifact.
func TestSweepRetiresAbortedManifest(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()
	mountAbs := lookupMountAbs(t, handler, mount)

	uploadID := writeTestManifest(t, mountAbs, multipartManifest{
		Bucket:    mount,
		Key:       "ditched.bin",
		Phase:     phaseAborted,
		Initiated: time.Now().Add(-2 * time.Hour).UnixMilli(),
	})

	res := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if res.AbortedRetired != 1 {
		t.Errorf("AbortedRetired=%d, want 1", res.AbortedRetired)
	}
	if _, err := os.Stat(stageDirFor(mountAbs, uploadID)); !os.IsNotExist(err) {
		t.Errorf("aborted stage dir should be gone, err=%v", err)
	}
}

// TestSweepForceAbortsStuckUpload: a phase=in_progress manifest
// older than StuckUploadMaxAge gets force-aborted by the cleanup
// loop. Catches abandoned multipart uploads (client crashed mid-
// stream, network died, etc.) that would otherwise pin parts/
// dirs forever.
func TestSweepForceAbortsStuckUpload(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()
	mountAbs := lookupMountAbs(t, handler, mount)

	stuckID := writeTestManifest(t, mountAbs, multipartManifest{
		Bucket:    mount,
		Key:       "stuck.bin",
		Phase:     phaseInProgress,
		Initiated: time.Now().Add(-10 * 24 * time.Hour).UnixMilli(),
	})
	// Also stage some bogus part data so we can verify it's gone.
	if err := os.WriteFile(filepath.Join(stageDirFor(mountAbs, stuckID), multipartPartsDirName, "00001.bin"), []byte("part bytes"), 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}

	res := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if res.StuckAborted != 1 {
		t.Errorf("StuckAborted=%d, want 1", res.StuckAborted)
	}
	if _, err := os.Stat(stageDirFor(mountAbs, stuckID)); !os.IsNotExist(err) {
		t.Errorf("stuck stage dir should be gone, err=%v", err)
	}
}

// TestSweepLeavesFreshUploadsAlone: in_progress manifests under
// the max-age threshold are NOT touched. A regression here would
// kill in-flight uploads of clients that just happen to be
// chunking slowly.
func TestSweepLeavesFreshUploadsAlone(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()
	mountAbs := lookupMountAbs(t, handler, mount)

	freshID := writeTestManifest(t, mountAbs, multipartManifest{
		Bucket:    mount,
		Key:       "active.bin",
		Phase:     phaseInProgress,
		Initiated: time.Now().Add(-10 * time.Minute).UnixMilli(),
	})

	res := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if res.StuckAborted != 0 || res.DoneRetired != 0 || res.AbortedRetired != 0 {
		t.Errorf("unexpected retirement: %+v", res)
	}
	if _, err := os.Stat(stageDirFor(mountAbs, freshID)); err != nil {
		t.Errorf("fresh stage dir should still exist, err=%v", err)
	}
}

// TestSweepHandlesUnparseableManifest: a malformed manifest must
// not crash the sweep nor be silently deleted. It's left alone
// (operator can investigate) and counted as an error so the loop
// log surfaces it.
func TestSweepHandlesUnparseableManifest(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()
	mountAbs := lookupMountAbs(t, handler, mount)

	// Hand-craft a stage dir with broken manifest JSON.
	stageDir := stageDirFor(mountAbs, "deadbeef00000000000000000000beef")
	if err := os.MkdirAll(filepath.Join(stageDir, multipartPartsDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(manifestPathFor(stageDir), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write garbage manifest: %v", err)
	}

	res := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if res.Errors != 1 {
		t.Errorf("Errors=%d, want 1 (unparseable manifest)", res.Errors)
	}
	if _, err := os.Stat(stageDir); err != nil {
		t.Errorf("unparseable stage dir should be left alone, err=%v", err)
	}
}

// TestSweepHandlesMultipleMounts: cleanup walks every mount the
// service knows about, not just the first. A single-mount
// shortcut would silently leak storage on multi-bucket
// deployments.
func TestSweepHandlesMultipleMounts(t *testing.T) {
	const (
		access = "AKIASWEEP000000000001"
		secret = "secret-sweep-000000000000000000000000000"
	)
	_, handler, cleanup := newMultiTenantTestServer(t, []KeyEntry{
		{AccessKey: access, SecretKey: secret, Buckets: []string{"*"}},
	})
	defer cleanup()
	svc := testSvcGlobal

	for _, bucket := range []string{"alpha", "beta", "gamma"} {
		mountAbs := lookupMountAbs(t, handler, bucket)
		writeTestManifest(t, mountAbs, multipartManifest{
			Bucket:      bucket,
			Key:         "expired.bin",
			Phase:       phaseDone,
			CompletedAt: time.Now().Add(-48 * time.Hour).UnixMilli(),
		})
	}

	res := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if res.DoneRetired != 3 {
		t.Errorf("DoneRetired=%d, want 3 (one per mount)", res.DoneRetired)
	}
}

// TestSweepIdempotent: running the sweep twice in a row produces
// no new effects on the second pass. Catches a leftover state
// bug where the sweep would count the same retirement twice.
func TestSweepIdempotent(t *testing.T) {
	svc, handler, mount, cleanup := newTestServer(t)
	defer cleanup()
	mountAbs := lookupMountAbs(t, handler, mount)

	writeTestManifest(t, mountAbs, multipartManifest{
		Bucket:      mount,
		Key:         "expired.bin",
		Phase:       phaseDone,
		CompletedAt: time.Now().Add(-48 * time.Hour).UnixMilli(),
	})

	first := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	second := SweepMultipartCleanup(svc, DefaultMultipartCleanupConfig())
	if first.DoneRetired != 1 {
		t.Errorf("first pass DoneRetired=%d, want 1", first.DoneRetired)
	}
	if second.DoneRetired != 0 {
		t.Errorf("second pass DoneRetired=%d, want 0", second.DoneRetired)
	}
}

// TestDecodeUploadIDMalformed: a manifest with a non-hex uploadID
// must not crash applyCleanupAction — we still want to remove the
// staging dir, just skip the durable-record delete.
func TestDecodeUploadIDMalformed(t *testing.T) {
	cases := []string{"", "tooshort", "g0000000000000000000000000000000", "00000000000000000000000000000000extra"}
	for _, c := range cases {
		if _, ok := decodeUploadID(c); ok {
			t.Errorf("decodeUploadID(%q) returned ok=true, want false", c)
		}
	}
	if _, ok := decodeUploadID("00000000000000000000000000000001"); !ok {
		t.Errorf("decodeUploadID of valid hex returned ok=false")
	}
}

// TestManifestStillReadsAfterRoundTripJSON pins that a manifest
// written and re-read preserves the cleanup-relevant fields. A
// schema bump that drops CompletedAt/Initiated would silently
// break the policy.
func TestManifestStillReadsAfterRoundTripJSON(t *testing.T) {
	tmpDir := t.TempDir()
	stageDir := filepath.Join(tmpDir, "s3-roundtrip")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := multipartManifest{
		Format:      multipartManifestFormat,
		Kind:        multipartManifestKind,
		UploadID:    "0123456789abcdef0123456789abcdef",
		Bucket:      "b",
		Key:         "k",
		Initiated:   12345,
		CompletedAt: 67890,
		Phase:       phaseDone,
		Parts:       map[int]multipartPart{},
	}
	if err := writeManifest(stageDir, &want); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	got, err := readManifest(stageDir)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if got.Initiated != want.Initiated {
		t.Errorf("Initiated round-trip lost: got=%d want=%d", got.Initiated, want.Initiated)
	}
	if got.CompletedAt != want.CompletedAt {
		t.Errorf("CompletedAt round-trip lost: got=%d want=%d", got.CompletedAt, want.CompletedAt)
	}
	// Belt-and-suspenders: also verify the on-disk JSON has the
	// expected key shapes.
	raw, _ := os.ReadFile(manifestPathFor(stageDir))
	var asMap map[string]any
	_ = json.Unmarshal(raw, &asMap)
	if _, ok := asMap["initiated_unix_ms"]; !ok {
		t.Errorf("manifest JSON missing initiated_unix_ms key (cleanup policy depends on it)")
	}
}
