package domain

import (
	"strings"
	"sync"
)

// pathLockManager serializes namespace-mutating operations by path,
// covering the gap that file-id locks (versionLocks) leave open: a
// "create iff not exists" can't lock by id when the id doesn't exist
// yet, and a same-path race between two writers (one creating, one
// already operating on a temp id) needs to serialize on the path.
//
// The manager supports two lock flavors:
//
//   - **point** lock: covers exactly one path. Used for file-level
//     ops (WriteContent, single-file Delete, mkdir, file rename).
//     Two writers wanting a point-lock on the same exact path
//     serialize.
//
//   - **subtree** lock: covers a path AND every descendant. Used for
//     subtree-touching ops (recursive Delete, directory rename,
//     subtree Transfer). A subtree-lock on /a/b excludes any point
//     or subtree lock on /a/b, /a/b/x, /a/b/x/y, etc.
//
// Conflict rules (checked under m.mu):
//
//   - AcquirePoint(p) blocks while: any ancestor of p (including p)
//     holds a subtree lock; OR p holds a point lock.
//   - AcquireSubtree(p) blocks while: any ancestor of p holds a
//     subtree lock; OR any descendant of p (including p) holds a
//     point or subtree lock.
//
// Path keys are canonical "{mount}/{relPath}" strings (or just
// "{mount}" for the mount root). Cross-mount operations acquire two
// locks; callers must order them lexicographically to avoid
// inversion-deadlock with concurrent same-pair operations.
//
// Wait mechanism: one sync.Cond per manager. On Release we Broadcast
// — every waiter re-checks its condition. For typical filegate
// workloads (low path-lock contention, mostly disjoint paths) the
// thundering-herd cost is irrelevant; if profiling later shows
// contention, per-path conds can be added without API changes.
type pathLockManager struct {
	mu      sync.Mutex
	cond    *sync.Cond
	holders map[string]*pathLockState
}

type pathLockState struct {
	pointCount   int // 0 or 1 — point lock is exclusive on the exact path
	subtreeCount int // 0 or 1 — subtree lock is exclusive
}

func newPathLockManager() *pathLockManager {
	m := &pathLockManager{
		holders: make(map[string]*pathLockState, 64),
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// pathLockKey renders a (mount, relPath) tuple into the canonical
// lock-map key. Empty relPath returns the mount alone — that's the
// mount root, an unusual lock target but valid (e.g. wiping the
// entire mount via rescan rebuild).
func pathLockKey(mount, relPath string) string {
	if relPath == "" {
		return mount
	}
	return mount + "/" + relPath
}

// AcquirePoint blocks until no conflicting lock is held on path,
// then claims an exclusive point lock. Returns a release function
// the caller MUST call (typically via defer) to free the lock.
func (m *pathLockManager) AcquirePoint(path string) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for !m.canAcquirePointLocked(path) {
		m.cond.Wait()
	}
	h := m.getOrCreateLocked(path)
	h.pointCount++
	return func() { m.releasePoint(path) }
}

// AcquireSubtree blocks until no conflicting lock is held on path,
// any of its ancestors, or any of its descendants, then claims an
// exclusive subtree lock. Returns a release function.
func (m *pathLockManager) AcquireSubtree(path string) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for !m.canAcquireSubtreeLocked(path) {
		m.cond.Wait()
	}
	h := m.getOrCreateLocked(path)
	h.subtreeCount++
	return func() { m.releaseSubtree(path) }
}

// AcquirePointPair locks two distinct point paths in lexical order to
// avoid inversion-deadlock with a concurrent caller acquiring the
// same pair. Used by single-file Transfer (point-on-src + point-on-
// dest). The two release functions return locks in reverse order; the
// returned combined-release does both.
func (m *pathLockManager) AcquirePointPair(a, b string) func() {
	if a == b {
		// Same path → single lock.
		return m.AcquirePoint(a)
	}
	first, second := a, b
	if first > second {
		first, second = second, first
	}
	rel1 := m.AcquirePoint(first)
	rel2 := m.AcquirePoint(second)
	return func() {
		rel2()
		rel1()
	}
}

// AcquireSubtreePair locks two subtree paths in lexical order.
// Used by subtree Transfer. If the two paths overlap (one is an
// ancestor of the other), only the wider lock is taken — the
// narrower would conflict with itself.
func (m *pathLockManager) AcquireSubtreePair(a, b string) func() {
	if a == b || isAncestorOrEqual(a, b) || isAncestorOrEqual(b, a) {
		wider := a
		if isAncestorOrEqual(b, a) {
			wider = b
		}
		return m.AcquireSubtree(wider)
	}
	first, second := a, b
	if first > second {
		first, second = second, first
	}
	rel1 := m.AcquireSubtree(first)
	rel2 := m.AcquireSubtree(second)
	return func() {
		rel2()
		rel1()
	}
}

func (m *pathLockManager) canAcquirePointLocked(path string) bool {
	// Walk ancestors (including path itself) for subtree locks. Any
	// subtree lock at or above us blocks our point.
	for p := path; p != ""; p = parentLockKey(p) {
		if h := m.holders[p]; h != nil && h.subtreeCount > 0 {
			return false
		}
		if p == "" {
			break
		}
	}
	if h := m.holders[path]; h != nil && h.pointCount > 0 {
		return false
	}
	return true
}

func (m *pathLockManager) canAcquireSubtreeLocked(path string) bool {
	// Walk ancestors for subtree locks.
	for p := path; p != ""; p = parentLockKey(p) {
		if h := m.holders[p]; h != nil && h.subtreeCount > 0 && p != path {
			return false
		}
		if p == "" {
			break
		}
	}
	// Reject if path itself has any holder.
	if h := m.holders[path]; h != nil && (h.pointCount > 0 || h.subtreeCount > 0) {
		return false
	}
	// Walk every active holder; reject if any is a descendant of path.
	prefix := path + "/"
	for p, h := range m.holders {
		if p == path {
			continue
		}
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		if h.pointCount > 0 || h.subtreeCount > 0 {
			return false
		}
	}
	return true
}

func (m *pathLockManager) releasePoint(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.holders[path]
	if h == nil || h.pointCount == 0 {
		// Defensive: should never happen unless caller mishandled
		// release. Keep silent rather than panic to avoid taking
		// down the daemon for a lock-discipline bug.
		return
	}
	h.pointCount--
	if h.pointCount == 0 && h.subtreeCount == 0 {
		delete(m.holders, path)
	}
	m.cond.Broadcast()
}

func (m *pathLockManager) releaseSubtree(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.holders[path]
	if h == nil || h.subtreeCount == 0 {
		return
	}
	h.subtreeCount--
	if h.pointCount == 0 && h.subtreeCount == 0 {
		delete(m.holders, path)
	}
	m.cond.Broadcast()
}

func (m *pathLockManager) getOrCreateLocked(path string) *pathLockState {
	if h, ok := m.holders[path]; ok {
		return h
	}
	h := &pathLockState{}
	m.holders[path] = h
	return h
}

// parentLockKey returns the parent-path of a lock key, or "" when
// path is at the root (no further ancestors to walk).
func parentLockKey(path string) string {
	idx := strings.LastIndexByte(path, '/')
	if idx < 0 {
		return ""
	}
	return path[:idx]
}

// pathLockKeyForID derives the canonical path-lock key for an entity
// by ID. Returns ("", false) only when path resolution fails (root
// "/" path or VirtualPath error). Mount roots themselves resolve to a
// valid key (just the mount name with empty relPath); callers that
// want to reject mount-root mutations check entity.ParentID.IsZero
// separately rather than relying on this returning false.
func (s *Service) pathLockKeyForID(id FileID) (string, bool) {
	vp, err := s.VirtualPath(id)
	if err != nil {
		return "", false
	}
	mount, rel, ok := splitVirtualPath(vp)
	if !ok {
		return "", false
	}
	return pathLockKey(mount, rel), true
}

// withFilePointLock runs fn with both the path-point-lock and the
// file-id-lock held for id, in the documented acquire order
// (path-lock first, file-id-lock second). The lock-then-revalidate
// check returns ErrNotFound if the file's path or existence changed
// between path resolution and path-lock acquisition — a sign that a
// concurrent rename or delete completed before we managed to lock.
//
// Use this from any ID-based mutating Service method whose effect
// scope is a single file (write content, single-file rename, single-
// file delete, in-place restore). Subtree-touching methods use
// withSubtreeLockByID instead.
func (s *Service) withFilePointLock(id FileID, fn func() error) error {
	initialKey, ok := s.pathLockKeyForID(id)
	if !ok {
		return ErrForbidden
	}
	pathRel := s.pathLocks.AcquirePoint(initialKey)
	defer pathRel()

	fileMu := s.versionLocks.Acquire(id)
	fileMu.Lock()
	defer fileMu.Unlock()

	currentKey, ok := s.pathLockKeyForID(id)
	if !ok || currentKey != initialKey {
		// Path changed under us between read and lock; safest to
		// surface as not-found so callers can retry on the new id.
		return ErrNotFound
	}
	return fn()
}

// withSubtreeLockByID is the subtree variant of withFilePointLock.
// Used by recursive Delete and directory rename/move operations
// whose effect spans the directory and every descendant. The
// file-id-lock is held alongside (covering the directory entity
// itself); descendants don't get individual file-id-locks — the
// subtree-lock excludes any other path-mutating op on them.
func (s *Service) withSubtreeLockByID(id FileID, fn func() error) error {
	initialKey, ok := s.pathLockKeyForID(id)
	if !ok {
		return ErrForbidden
	}
	pathRel := s.pathLocks.AcquireSubtree(initialKey)
	defer pathRel()

	fileMu := s.versionLocks.Acquire(id)
	fileMu.Lock()
	defer fileMu.Unlock()

	currentKey, ok := s.pathLockKeyForID(id)
	if !ok || currentKey != initialKey {
		return ErrNotFound
	}
	return fn()
}

// isAncestorOrEqual reports whether a is an ancestor of b (a == b
// counts). Used by AcquireSubtreePair to detect overlapping pairs.
func isAncestorOrEqual(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(b, a+"/")
}
