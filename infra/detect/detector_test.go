package detect

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNewAutoSelectsBTRFSWhenAllPathsSupported(t *testing.T) {
	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "subvolume" && args[1] == "show" {
			return []byte("Generation: 77\n"), nil
		}
		return nil, fmt.Errorf("unexpected args: %v", args)
	}

	runner, err := New("auto", []string{"/a", "/b"}, time.Second)
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	if got := runner.Name(); got != "btrfs" {
		t.Fatalf("backend=%q, want btrfs", got)
	}
}

func TestNewAutoFallsBackToPollWhenBTRFSUnsupported(t *testing.T) {
	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "subvolume" && args[1] == "show" {
			if strings.Contains(args[2], "unsupported") {
				return []byte("ERROR: not a btrfs filesystem"), fmt.Errorf("exit status 1")
			}
			return []byte("Generation: 9\n"), nil
		}
		return nil, fmt.Errorf("unexpected args: %v", args)
	}

	runner, err := New("auto", []string{"/supported", "/unsupported"}, time.Second)
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	if got := runner.Name(); got != "poll" {
		t.Fatalf("backend=%q, want poll", got)
	}
}

func TestNewBTRFSReturnsErrorWhenUnsupported(t *testing.T) {
	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("ERROR: not a btrfs filesystem"), fmt.Errorf("exit status 1")
	}

	_, err := New("btrfs", []string{"/unsupported"}, time.Second)
	if err == nil {
		t.Fatalf("expected error for unsupported btrfs backend")
	}
}

func TestCurrentGenerationParsesGeneration(t *testing.T) {
	prev := runBTRFSCommand
	t.Cleanup(func() { runBTRFSCommand = prev })
	runBTRFSCommand = func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("Name: x\nGeneration: 1234\n"), nil
	}

	gen, err := currentGeneration(context.Background(), "/data")
	if err != nil {
		t.Fatalf("current generation: %v", err)
	}
	if gen != 1234 {
		t.Fatalf("generation=%d, want=1234", gen)
	}
}
