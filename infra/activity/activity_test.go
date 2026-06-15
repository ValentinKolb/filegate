package activity

import "testing"

func TestRingSnapshotNewestFirstAndBounded(t *testing.T) {
	r := NewRing(3)
	for _, op := range []string{"one", "two", "three", "four"} {
		r.Record(Event{Operation: op})
	}

	if got := r.Capacity(); got != 3 {
		t.Fatalf("capacity=%d, want 3", got)
	}
	if got := r.Len(); got != 3 {
		t.Fatalf("len=%d, want 3", got)
	}

	got := r.Snapshot(0)
	want := []string{"four", "three", "two"}
	if len(got) != len(want) {
		t.Fatalf("snapshot len=%d, want %d", len(got), len(want))
	}
	for i, event := range got {
		if event.Operation != want[i] {
			t.Fatalf("snapshot[%d]=%q, want %q", i, event.Operation, want[i])
		}
	}
}

func TestRingSnapshotLimit(t *testing.T) {
	r := NewRing(5)
	r.Record(Event{Operation: "one"})
	r.Record(Event{Operation: "two"})

	got := r.Snapshot(1)
	if len(got) != 1 || got[0].Operation != "two" {
		t.Fatalf("snapshot=%v, want newest event only", got)
	}
}

func TestCleanActorLabel(t *testing.T) {
	got := CleanActorLabel("  valentin\nadmin\t ")
	if got != "valentin admin" {
		t.Fatalf("label=%q, want sanitized single-line label", got)
	}
}
