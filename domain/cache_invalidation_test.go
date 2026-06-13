package domain

import (
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
)

// newCacheTestService builds a Service around fakeIndex with a small
// tree: /data (mount root) -> a (dir) -> f.txt, plus /data/other.txt.
// Returns the service and the ids involved.
func newCacheTestService(t *testing.T) (svc *Service, dirID, fileID, otherID FileID) {
	t.Helper()
	rootID := FileID{0x01}
	dirID = FileID{0x02}
	fileID = FileID{0x03}
	otherID = FileID{0x04}

	idx := newFakeIndex()
	idx.entities[rootID] = Entity{ID: rootID, Name: "data", IsDir: true}
	idx.entities[dirID] = Entity{ID: dirID, ParentID: rootID, Name: "a", IsDir: true}
	idx.entities[fileID] = Entity{ID: fileID, ParentID: dirID, Name: "f.txt"}
	idx.entities[otherID] = Entity{ID: otherID, ParentID: rootID, Name: "other.txt"}

	cache, err := lru.New[string, pathCacheEntry](64)
	if err != nil {
		t.Fatalf("new path cache: %v", err)
	}
	idPathCache, err := lru.New[FileID, string](64)
	if err != nil {
		t.Fatalf("new id path cache: %v", err)
	}
	svc = &Service{idx: idx, cache: cache, idPathCache: idPathCache}
	return svc, dirID, fileID, otherID
}

// TestInvalidateCachePrefixIsTargeted pins the targeted invalidation:
// dropping a subtree prefix removes exactly the id→path entries under
// it and leaves unrelated hot entries alone (the old implementation
// purged the whole id→path cache on every mutation).
func TestInvalidateCachePrefixIsTargeted(t *testing.T) {
	svc, dirID, fileID, otherID := newCacheTestService(t)

	for _, id := range []FileID{dirID, fileID, otherID} {
		if _, err := svc.VirtualPath(id); err != nil {
			t.Fatalf("warm VirtualPath(%s): %v", id, err)
		}
	}
	svc.cache.Add("data/a/f.txt", pathCacheEntry{ID: fileID})
	svc.cache.Add("data/other.txt", pathCacheEntry{ID: otherID})

	svc.invalidateCachePrefix("/data/a")

	if _, ok := svc.idPathCache.Peek(fileID); ok {
		t.Fatal("descendant id→path entry survived prefix invalidation")
	}
	if _, ok := svc.idPathCache.Peek(dirID); ok {
		t.Fatal("subtree-root id→path entry survived prefix invalidation")
	}
	if _, ok := svc.idPathCache.Peek(otherID); !ok {
		t.Fatal("unrelated id→path entry was dropped — invalidation is not targeted")
	}
	if _, ok := svc.cache.Peek("data/a/f.txt"); ok {
		t.Fatal("descendant path→id entry survived prefix invalidation")
	}
	if _, ok := svc.cache.Peek("data/other.txt"); !ok {
		t.Fatal("unrelated path→id entry was dropped")
	}
}

// TestInvalidatePathCacheKeepsIDPathEntries pins that the exact-path
// invalidation no longer nukes the id→path cache: it only removes the
// one path→id mapping.
func TestInvalidatePathCacheKeepsIDPathEntries(t *testing.T) {
	svc, _, fileID, otherID := newCacheTestService(t)

	if _, err := svc.VirtualPath(otherID); err != nil {
		t.Fatalf("warm VirtualPath: %v", err)
	}
	svc.cache.Add("data/a/f.txt", pathCacheEntry{ID: fileID})

	svc.InvalidatePathCache("/data/a/f.txt")

	if _, ok := svc.cache.Peek("data/a/f.txt"); ok {
		t.Fatal("exact path→id entry survived invalidation")
	}
	if _, ok := svc.idPathCache.Peek(otherID); !ok {
		t.Fatal("id→path cache was purged by exact-path invalidation")
	}
}
