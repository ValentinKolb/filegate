package detect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPollerDiscoversNestedTreeCreatedWithinOneInterval pins the
// subtree-adoption behavior: a directory tree that appears fully
// formed between two polls (mkdir -p burst, mv into the mount) used
// to be registered with its final mtime after one level — scanDirectory
// never descended, so deeper contents stayed undiscovered until a
// manual rescan.
func TestPollerDiscoversNestedTreeCreatedWithinOneInterval(t *testing.T) {
	root := t.TempDir()
	p := NewPoller([]string{root}, time.Second)
	p.initialize()

	// Build the tree outside the mount, then move it in with one
	// rename so the poller can only ever observe the finished tree.
	staging := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staging, "deep", "a"), 0o755); err != nil {
		t.Fatalf("mkdir staging tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "deep", "a", "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write leaf: %v", err)
	}
	if err := os.Rename(filepath.Join(staging, "deep"), filepath.Join(root, "deep")); err != nil {
		t.Fatalf("move tree into mount: %v", err)
	}
	// Force the root's mtime past the initialized snapshot so the
	// first poll sees it dirty even at millisecond granularity.
	bump := time.Now().Add(10 * time.Millisecond)
	if err := os.Chtimes(root, bump, bump); err != nil {
		t.Fatalf("bump root mtime: %v", err)
	}

	batch := p.poll()

	got := make(map[string]EventType, len(batch))
	for _, ev := range batch {
		got[ev.AbsPath] = ev.Type
	}
	for _, want := range []string{
		filepath.Join(root, "deep"),
		filepath.Join(root, "deep", "a"),
		filepath.Join(root, "deep", "a", "leaf.txt"),
	} {
		if typ, ok := got[want]; !ok || typ != EventCreated {
			t.Fatalf("missing EventCreated for %s, batch=%v", want, batch)
		}
	}

	// The adopted tree must be tracked: deleting the leaf surfaces as
	// EventDeleted on a later poll.
	if err := os.Remove(filepath.Join(root, "deep", "a", "leaf.txt")); err != nil {
		t.Fatalf("remove leaf: %v", err)
	}
	batch = p.poll()
	foundDelete := false
	for _, ev := range batch {
		if ev.Type == EventDeleted && ev.AbsPath == filepath.Join(root, "deep", "a", "leaf.txt") {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("adopted leaf not tracked for deletion, batch=%v", batch)
	}
}
