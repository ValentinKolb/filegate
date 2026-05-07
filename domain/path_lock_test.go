package domain

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPointLockExclusiveOnSamePath: two acquire-point on the same
// path serialize. The second waits until the first releases.
func TestPointLockExclusiveOnSamePath(t *testing.T) {
	m := newPathLockManager()

	rel1 := m.AcquirePoint("data/foo.txt")
	acquired := make(chan struct{})
	go func() {
		rel2 := m.AcquirePoint("data/foo.txt")
		close(acquired)
		rel2()
	}()

	// Second acquire must NOT have completed yet.
	select {
	case <-acquired:
		t.Fatalf("second AcquirePoint completed while first holds the lock")
	case <-time.After(50 * time.Millisecond):
	}
	rel1()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatalf("second AcquirePoint did not unblock after release")
	}
}

// TestPointLockDisjointPathsParallel: locks on different paths don't
// block each other. Used for high-throughput parallel writes.
func TestPointLockDisjointPathsParallel(t *testing.T) {
	m := newPathLockManager()

	rel1 := m.AcquirePoint("data/a.txt")
	defer rel1()
	// Should not block: different path.
	done := make(chan struct{})
	go func() {
		rel2 := m.AcquirePoint("data/b.txt")
		rel2()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("AcquirePoint on disjoint path blocked")
	}
}

// TestSubtreeLockBlocksDescendantPoint: a subtree lock on /data/a/
// blocks point-locks on /data/a/leaf, /data/a/sub/deep, etc.
func TestSubtreeLockBlocksDescendantPoint(t *testing.T) {
	m := newPathLockManager()

	rel := m.AcquireSubtree("data/a")

	for _, descendant := range []string{"data/a", "data/a/leaf", "data/a/sub/deep"} {
		blocked := make(chan struct{})
		go func(p string) {
			r := m.AcquirePoint(p)
			r()
			close(blocked)
		}(descendant)
		select {
		case <-blocked:
			t.Fatalf("AcquirePoint(%q) was not blocked by subtree on data/a", descendant)
		case <-time.After(20 * time.Millisecond):
		}
	}
	rel()
}

// TestPointBlocksSubtreeOnAncestor: a point lock on /data/a/leaf
// blocks subtree-lock on /data/a (the subtree contains the point).
func TestPointBlocksSubtreeOnAncestor(t *testing.T) {
	m := newPathLockManager()

	rel := m.AcquirePoint("data/a/leaf")
	blocked := make(chan struct{})
	go func() {
		r := m.AcquireSubtree("data/a")
		r()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatalf("AcquireSubtree on ancestor was not blocked by descendant point lock")
	case <-time.After(50 * time.Millisecond):
	}
	rel()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatalf("subtree did not unblock after point release")
	}
}

// TestSubtreeLocksDisjointPrefixesParallel: subtree on /a doesn't
// block subtree on /b.
func TestSubtreeLocksDisjointPrefixesParallel(t *testing.T) {
	m := newPathLockManager()

	rel1 := m.AcquireSubtree("data/a")
	defer rel1()

	done := make(chan struct{})
	go func() {
		rel2 := m.AcquireSubtree("data/b")
		rel2()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("disjoint subtree locks blocked each other")
	}
}

// TestSubtreeLocksOverlappingSerialize: subtree on /a/b conflicts
// with subtree on /a (the wider lock contains the narrower).
func TestSubtreeLocksOverlappingSerialize(t *testing.T) {
	m := newPathLockManager()

	rel1 := m.AcquireSubtree("data/a")
	blocked := make(chan struct{})
	go func() {
		r := m.AcquireSubtree("data/a/b")
		r()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatalf("inner subtree acquired while outer holds")
	case <-time.After(50 * time.Millisecond):
	}
	rel1()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatalf("inner subtree did not unblock after outer release")
	}
}

// TestPointPairLocksTwoDistinct: AcquirePointPair locks both paths,
// release frees both.
func TestPointPairLocksTwoDistinct(t *testing.T) {
	m := newPathLockManager()
	rel := m.AcquirePointPair("data/a", "data/b")

	// Both paths should be observably locked: a third party trying
	// to lock either of them blocks.
	for _, p := range []string{"data/a", "data/b"} {
		blocked := make(chan struct{})
		go func(p string) {
			r := m.AcquirePoint(p)
			r()
			close(blocked)
		}(p)
		select {
		case <-blocked:
			t.Fatalf("AcquirePoint(%q) was not blocked by pair", p)
		case <-time.After(20 * time.Millisecond):
		}
	}
	rel()
	// After release both should be acquirable immediately.
	r1 := m.AcquirePoint("data/a")
	r1()
	r2 := m.AcquirePoint("data/b")
	r2()
}

// TestPointPairSamePathSingleLock: when both args of PointPair are
// equal, only one logical lock is taken (so we don't deadlock on
// re-entrance).
func TestPointPairSamePathSingleLock(t *testing.T) {
	m := newPathLockManager()
	rel := m.AcquirePointPair("data/x", "data/x")
	rel()
	// Should be re-acquirable.
	rel2 := m.AcquirePoint("data/x")
	rel2()
}

// TestSubtreePairOverlappingTakesWider: when one subtree contains
// the other, AcquireSubtreePair takes only the wider lock.
func TestSubtreePairOverlappingTakesWider(t *testing.T) {
	m := newPathLockManager()
	rel := m.AcquireSubtreePair("data/parent", "data/parent/child")

	// Adventurous third party trying point on a sibling of "child"
	// must be blocked (the wider parent lock covers it).
	blocked := make(chan struct{})
	go func() {
		r := m.AcquirePoint("data/parent/sibling")
		r()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatalf("point on sibling of inner subtree not blocked by wider lock")
	case <-time.After(20 * time.Millisecond):
	}
	rel()
}

// TestStressNoLockLeak: 100 goroutines × 100 random ops, after all
// done the holders map must be empty. Catches release-without-acquire
// or acquire-without-release bugs.
func TestStressNoLockLeak(t *testing.T) {
	m := newPathLockManager()

	const goroutines = 100
	const opsEach = 100
	paths := []string{"data/a", "data/b", "data/c", "data/a/x", "data/a/y", "data/b/x"}

	var wg sync.WaitGroup
	var done atomic.Int64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsEach; i++ {
				p := paths[(id*opsEach+i)%len(paths)]
				switch (id + i) % 3 {
				case 0:
					rel := m.AcquirePoint(p)
					rel()
				case 1:
					rel := m.AcquireSubtree(p)
					rel()
				case 2:
					rel := m.AcquirePointPair(p, paths[(id+1)%len(paths)])
					rel()
				}
				done.Add(1)
			}
		}(g)
	}
	wg.Wait()
	if got := done.Load(); got != int64(goroutines*opsEach) {
		t.Fatalf("ops completed=%d, want %d", got, goroutines*opsEach)
	}

	m.mu.Lock()
	holders := len(m.holders)
	m.mu.Unlock()
	if holders != 0 {
		t.Fatalf("lock holders map leaked: %d remaining entries", holders)
	}
}
