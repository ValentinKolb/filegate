package detect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBTRFSDetectorPollBuildsChangedEventsFromFindNew(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "a.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "subvolume" && args[1] == "show" {
			return []byte("Generation: 11\n"), nil
		}
		if len(args) >= 4 && args[0] == "subvolume" && args[1] == "find-new" {
			return []byte("inode 42 file offset 0 len 10 gen 11\ntransid marker was 11\n"), nil
		}
		if len(args) >= 4 && args[0] == "inspect-internal" && args[1] == "inode-resolve" {
			return []byte("a.txt\n"), nil
		}
		return nil, nil
	}

	d := NewBTRFSDetector([]string{base}, time.Second)
	d.lastGeneration[base] = 10

	batch := d.poll(context.Background())
	if len(batch) == 0 {
		t.Fatalf("expected changed event batch")
	}

	foundChanged := false
	for _, ev := range batch {
		if ev.Type != EventChanged {
			continue
		}
		foundChanged = true
		if ev.AbsPath != target {
			t.Fatalf("changed abs path=%q, want %q", ev.AbsPath, target)
		}
	}
	if !foundChanged {
		t.Fatalf("expected EventChanged in batch, got %v", batch)
	}
	if got := d.lastGeneration[base]; got != 11 {
		t.Fatalf("last generation=%d, want=11", got)
	}
}

func TestBTRFSDetectorAddsUnknownWhenInodeCannotBeResolved(t *testing.T) {
	base := t.TempDir()

	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "subvolume" && args[1] == "show" {
			return []byte("Generation: 12\n"), nil
		}
		if len(args) >= 4 && args[0] == "subvolume" && args[1] == "find-new" {
			return []byte("inode 77 file offset 0 len 10 gen 12\ntransid marker was 12\n"), nil
		}
		if len(args) >= 4 && args[0] == "inspect-internal" && args[1] == "inode-resolve" {
			return []byte(""), nil
		}
		return nil, nil
	}

	d := NewBTRFSDetector([]string{base}, time.Second)
	d.lastGeneration[base] = 11

	batch := d.poll(context.Background())
	foundUnknown := false
	for _, ev := range batch {
		if ev.Type == EventUnknown && strings.TrimSpace(ev.AbsPath) == base {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatalf("expected EventUnknown for unresolved inode, batch=%v", batch)
	}
}

func TestBTRFSDetectorPollRunsDeltaWhenGenerationUnchanged(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "same-gen.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "subvolume" && args[1] == "show" {
			return []byte("Generation: 20\n"), nil
		}
		if len(args) >= 4 && args[0] == "subvolume" && args[1] == "find-new" {
			return []byte("inode 101 file offset 0 len 1 gen 20\ntransid marker was 20\n"), nil
		}
		if len(args) >= 4 && args[0] == "inspect-internal" && args[1] == "inode-resolve" {
			return []byte("same-gen.txt\n"), nil
		}
		return nil, nil
	}

	d := NewBTRFSDetector([]string{base}, time.Second)
	d.lastGeneration[base] = 20

	batch := d.poll(context.Background())
	foundChanged := false
	for _, ev := range batch {
		if ev.Type == EventChanged && ev.AbsPath == target {
			foundChanged = true
			break
		}
	}
	if !foundChanged {
		t.Fatalf("expected changed event with unchanged generation, batch=%v", batch)
	}
}

func TestBTRFSDetectorAddsUnknownWhenOnlyMarkerAdvances(t *testing.T) {
	base := t.TempDir()

	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "subvolume" && args[1] == "show" {
			return []byte("Generation: 31\n"), nil
		}
		if len(args) >= 4 && args[0] == "subvolume" && args[1] == "find-new" {
			return []byte("transid marker was 31\n"), nil
		}
		return nil, nil
	}

	d := NewBTRFSDetector([]string{base}, time.Second)
	d.lastGeneration[base] = 30

	batch := d.poll(context.Background())
	foundUnknown := false
	for _, ev := range batch {
		if ev.Type == EventUnknown && strings.TrimSpace(ev.AbsPath) == base {
			foundUnknown = true
			break
		}
	}
	if !foundUnknown {
		t.Fatalf("expected EventUnknown when only marker advances, batch=%v", batch)
	}
	if got := d.lastGeneration[base]; got != 31 {
		t.Fatalf("last generation=%d, want=31", got)
	}
}
