package cli

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWarnOrphanVersionDirsLogsDetachedBlobs pins the operator-safety
// signal that fires when a Pebble format-version bump triggers a full
// index rebuild on a btrfs mount that already has captured version
// blobs. Without the warning the operator only finds out via "where
// did my disk space go" weeks later. The blobs themselves are NOT
// auto-removed (could contain recoverable data) — the warn is the
// whole signal.
func TestWarnOrphanVersionDirsLogsDetachedBlobs(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".fg-versions", "file-id"), 0o700); err != nil {
		t.Fatalf("seed orphan dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, ".fg-versions", "file-id", "blob.bin"),
		[]byte("payload"), 0o600); err != nil {
		t.Fatalf("seed blob: %v", err)
	}

	// Capture the standard logger's output for the duration of the call.
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	prevPrefix := log.Prefix()
	defer func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	}()
	var buf bytes.Buffer
	log.SetOutput(&buf)

	warnOrphanVersionDirs([]string{base})

	got := buf.String()
	if !strings.Contains(got, "WARNING") {
		t.Fatalf("warn missing WARNING prefix: %q", got)
	}
	if !strings.Contains(got, filepath.Join(base, ".fg-versions")) {
		t.Fatalf("warn missing path %q: %q", filepath.Join(base, ".fg-versions"), got)
	}
	if !strings.Contains(got, "version blob") {
		t.Fatalf("warn doesn't mention version blobs: %q", got)
	}
}

// TestWarnOrphanVersionDirsSilentWithoutOrphans pins that the warning
// fires only when there's actually something detached. A noisy WARN on
// every clean restart would train operators to ignore it.
func TestWarnOrphanVersionDirsSilentWithoutOrphans(t *testing.T) {
	base := t.TempDir()

	prevOutput := log.Writer()
	defer log.SetOutput(prevOutput)
	var buf bytes.Buffer
	log.SetOutput(&buf)

	warnOrphanVersionDirs([]string{base})

	if buf.Len() != 0 {
		t.Fatalf("warn fired with no orphans: %q", buf.String())
	}
}
