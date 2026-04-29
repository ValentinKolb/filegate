package pebble

import (
	"testing"

	"github.com/valentinkolb/filegate/domain"
)

func TestLookupByInodeReturnsEmptyForUnknown(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	ids, err := idx.LookupByInode(1, 100)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty, got %v", ids)
	}
}

func TestPutEntityRegistersInodeMapping(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	id := testID(1)
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id, Name: "a", Device: 7, Inode: 42, Nlink: 1})
		return nil
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}

	got, err := idx.LookupByInode(7, 42)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 1 || got[0] != id {
		t.Fatalf("LookupByInode = %v, want [%v]", got, id)
	}
}

func TestPutEntitySkipsZeroInodeMapping(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	id := testID(1)
	if err := idx.Batch(func(b domain.Batch) error {
		// Mount-root style entity with no stat info.
		b.PutEntity(domain.Entity{ID: id, Name: "root", IsDir: true})
		return nil
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}

	got, err := idx.LookupByInode(0, 0)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("zero inode must not be indexed, got %v", got)
	}
}

func TestPutEntityUpdatesInodeMappingOnInodeChange(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	id := testID(1)
	// Initial write at inode 42.
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id, Name: "x", Device: 7, Inode: 42, Nlink: 1})
		return nil
	}); err != nil {
		t.Fatalf("batch1: %v", err)
	}
	// Same ID, different inode (e.g. file replaced in-place externally).
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id, Name: "x", Device: 7, Inode: 99, Nlink: 1})
		return nil
	}); err != nil {
		t.Fatalf("batch2: %v", err)
	}

	if got, _ := idx.LookupByInode(7, 42); len(got) != 0 {
		t.Fatalf("old inode mapping must be cleared, got %v", got)
	}
	got, err := idx.LookupByInode(7, 99)
	if err != nil {
		t.Fatalf("lookup new: %v", err)
	}
	if len(got) != 1 || got[0] != id {
		t.Fatalf("LookupByInode(7,99) = %v, want [%v]", got, id)
	}
}

func TestPutEntityRenameDropsStaleChildEntry(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	parentA := testID(10)
	parentB := testID(11)
	id := testID(1)

	// Initial: id sits under parentA as "old.txt".
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id, ParentID: parentA, Name: "old.txt", Device: 7, Inode: 42, Nlink: 1})
		b.PutChild(parentA, "old.txt", domain.DirEntry{ID: id, Name: "old.txt"})
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got, _ := idx.LookupChild(parentA, "old.txt"); got == nil || got.ID != id {
		t.Fatalf("seed child not found")
	}

	// Reparent + rename: id moves to parentB as "new.txt". PutEntity must
	// detect the change and tear down the old (parentA, "old.txt") entry.
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id, ParentID: parentB, Name: "new.txt", Device: 7, Inode: 42, Nlink: 1})
		b.PutChild(parentB, "new.txt", domain.DirEntry{ID: id, Name: "new.txt"})
		return nil
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got, err := idx.LookupChild(parentA, "old.txt")
	if err != nil && got != nil {
		t.Fatalf("stale child lookup err: %v", err)
	}
	if got != nil {
		t.Fatalf("stale child entry under old parent must be gone, got %+v", got)
	}
	gotNew, err := idx.LookupChild(parentB, "new.txt")
	if err != nil {
		t.Fatalf("lookup new: %v", err)
	}
	if gotNew == nil || gotNew.ID != id {
		t.Fatalf("new child lookup = %+v, want id=%v", gotNew, id)
	}
}

func TestDelEntityRemovesInodeMapping(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	id := testID(1)
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id, Name: "x", Device: 7, Inode: 42, Nlink: 1})
		return nil
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := idx.Batch(func(b domain.Batch) error {
		b.DelEntity(id)
		return nil
	}); err != nil {
		t.Fatalf("del: %v", err)
	}

	if got, _ := idx.LookupByInode(7, 42); len(got) != 0 {
		t.Fatalf("inode mapping must be removed after DelEntity, got %v", got)
	}
}

func TestPutEntityCoalescesMultipleIDsAtSameInode(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	id1 := testID(1)
	id2 := testID(2)
	if err := idx.Batch(func(b domain.Batch) error {
		b.PutEntity(domain.Entity{ID: id1, Name: "a", Device: 7, Inode: 42, Nlink: 2})
		b.PutEntity(domain.Entity{ID: id2, Name: "b", Device: 7, Inode: 42, Nlink: 2})
		return nil
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := idx.LookupByInode(7, 42)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	seen := map[domain.FileID]bool{}
	for _, gid := range got {
		seen[gid] = true
	}
	if !seen[id1] || !seen[id2] || len(seen) != 2 {
		t.Fatalf("LookupByInode(7,42) = %v, want both %v and %v", got, id1, id2)
	}
}

func TestFormatVersionIsFive(t *testing.T) {
	// Compile-time guard: catch accidental rollback of the inode-tracking
	// schema bump. If this fails it's because someone changed the version
	// constant without thinking through the migration story.
	if currentIndexFormatVersion != 5 {
		t.Fatalf("currentIndexFormatVersion=%d, expected 5 (inode tracking schema)", currentIndexFormatVersion)
	}
}
