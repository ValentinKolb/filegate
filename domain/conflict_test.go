package domain

import (
	"errors"
	"testing"
)

func TestParseConflictModeDefaultsToError(t *testing.T) {
	for _, allowed := range []ConflictAllowed{FileConflictModes, MkdirConflictModes} {
		got, err := ParseConflictMode("", allowed)
		if err != nil {
			t.Fatalf("empty input err=%v", err)
		}
		if got != ConflictError {
			t.Fatalf("empty input default=%q, want %q", got, ConflictError)
		}
	}
}

func TestParseConflictModeFileEndpointAccepts(t *testing.T) {
	for _, in := range []string{"error", "ERROR", " overwrite ", "Rename"} {
		if _, err := ParseConflictMode(in, FileConflictModes); err != nil {
			t.Errorf("ParseConflictMode(%q, file) err=%v", in, err)
		}
	}
}

func TestParseConflictModeFileEndpointRejectsSkip(t *testing.T) {
	_, err := ParseConflictMode("skip", FileConflictModes)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err=%v, want ErrInvalidArgument", err)
	}
}

func TestParseConflictModeMkdirEndpointAccepts(t *testing.T) {
	for _, in := range []string{"error", "skip", "rename"} {
		if _, err := ParseConflictMode(in, MkdirConflictModes); err != nil {
			t.Errorf("ParseConflictMode(%q, mkdir) err=%v", in, err)
		}
	}
}

func TestParseConflictModeMkdirEndpointRejectsOverwrite(t *testing.T) {
	_, err := ParseConflictMode("overwrite", MkdirConflictModes)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err=%v, want ErrInvalidArgument", err)
	}
}

func TestParseConflictModeRejectsUnknown(t *testing.T) {
	for _, in := range []string{"replace", "merge", "wat"} {
		if _, err := ParseConflictMode(in, FileConflictModes); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("ParseConflictMode(%q) err=%v, want ErrInvalidArgument", in, err)
		}
	}
}
