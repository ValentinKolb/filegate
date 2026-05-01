package domain

import (
	"fmt"
	"testing"
	"time"
)

// TestComputeDirectorySizeBudgetCountsChildren pins the contract that the
// per-call budget bounds work in proportion to the number of entries
// touched, not just the number of directories popped. A regression here
// would let a single high-fanout directory blow past the caller's budget
// without the bound noticing — making ListNodeChildren's deadline/budget
// guards effectively meaningless under pathological input.
func TestComputeDirectorySizeBudgetCountsChildren(t *testing.T) {
	parentID := FileID{0x01}
	const fanout = 100
	const fileSize int64 = 1024

	idx := newFakeIndex()
	idx.entities[parentID] = Entity{ID: parentID, Name: "parent", IsDir: true}
	for i := 0; i < fanout; i++ {
		childID := FileID{}
		childID[0] = 0x02
		childID[1] = byte(i / 256)
		childID[2] = byte(i % 256)
		name := fmt.Sprintf("f%03d", i)
		idx.entities[childID] = Entity{ID: childID, ParentID: parentID, Name: name, Size: fileSize}
		idx.children[parentID] = append(idx.children[parentID], DirEntry{ID: childID, Name: name, Size: fileSize})
	}

	svc := &Service{idx: idx}

	t.Run("budget exceeds fanout returns full size", func(t *testing.T) {
		budget := fanout + 10
		size, ok := svc.computeDirectorySizeByIDBudget(parentID, &budget, time.Time{})
		if !ok {
			t.Fatalf("expected ok=true with sufficient budget")
		}
		if want := fileSize * fanout; size != want {
			t.Fatalf("size=%d, want=%d", size, want)
		}
	})

	t.Run("budget smaller than fanout aborts", func(t *testing.T) {
		// Old behavior decremented only once per directory popped, so a
		// budget of 5 against a single dir with 100 children still
		// returned (fileSize*100, true). With the fix the budget burns
		// per child and the call must abort with (0, false).
		budget := 5
		size, ok := svc.computeDirectorySizeByIDBudget(parentID, &budget, time.Time{})
		if ok {
			t.Fatalf("expected ok=false when budget=5 < fanout=%d, got size=%d", fanout, size)
		}
		if size != 0 {
			t.Fatalf("size=%d on aborted call, want 0", size)
		}
	})
}

// fakeIndex is a minimal in-memory Index implementation supporting only
// the read surface (GetEntity, ListChildren) needed for budget tests.
// Listing returns all children in one chunk; cursor handling matches what
// listAllChildren expects (an empty or short chunk terminates the loop).
type fakeIndex struct {
	entities map[FileID]Entity
	children map[FileID][]DirEntry
}

func newFakeIndex() *fakeIndex {
	return &fakeIndex{
		entities: make(map[FileID]Entity),
		children: make(map[FileID][]DirEntry),
	}
}

func (f *fakeIndex) GetEntity(id FileID) (*Entity, error) {
	if e, ok := f.entities[id]; ok {
		return &e, nil
	}
	return nil, ErrNotFound
}

func (f *fakeIndex) LookupChild(parentID FileID, name string) (*DirEntry, error) {
	for _, c := range f.children[parentID] {
		if c.Name == name {
			cp := c
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeIndex) ListChildren(parentID FileID, after string, limit int) ([]DirEntry, error) {
	all := f.children[parentID]
	out := make([]DirEntry, 0, len(all))
	for _, c := range all {
		if after != "" && c.Name <= after {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeIndex) ListEntities() ([]Entity, error) {
	out := make([]Entity, 0, len(f.entities))
	for _, e := range f.entities {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeIndex) ForEachEntity(fn func(Entity) error) error {
	for _, e := range f.entities {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeIndex) Batch(fn func(Batch) error) error { return fn(nil) }

func (f *fakeIndex) Close() error { return nil }
