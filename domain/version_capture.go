package domain

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// versionsDirName is the hidden subdirectory inside each watched mount
// that holds version blob payloads. Layout:
//
//	<mount>/.fg-versions/<file-id-hex>/<version-id>.bin
//
// Reflinks must stay within a filesystem; placing versions inside the
// owning mount is what makes the per-mount FICLONE call cheap on btrfs.
const versionsDirName = ".fg-versions"

// EnableVersioning wires the versioning subsystem from operator config.
// Called by cli.serve after NewService. Until invoked the capture helpers
// short-circuit and the public API surface returns "feature disabled" —
// existing tests and embeddings keep working without any changes.
//
// Calling with enabled=false is also valid and acts as a kill switch.
func (s *Service) EnableVersioning(cfg VersioningConfig, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versioningCfg = cfg
	s.versioningEnabled = enabled
}

// VersioningEnabled reports whether the subsystem is active.
func (s *Service) VersioningEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versioningEnabled
}

// captureOptions controls the auto-capture behaviour.
type captureOptions struct {
	// ignoreCooldown bypasses the cooldown check. Manual snapshots and
	// the first-version-on-create path use it; routine writes do not.
	ignoreCooldown bool
	// honourMinSizeFloor skips capture for files smaller than
	// VersioningConfig.MinSizeForAutoV1. Auto-capture on overwrite and
	// first-version-on-create both pay this courtesy; manual snapshots
	// (via /snapshot) ignore the floor and capture anything.
	honourMinSizeFloor bool
	// pinned marks the captured version as immune to bucket pruning.
	pinned bool
	// label is opaque metadata, capped at MaxLabelBytes by the caller.
	label string
}

// captureCurrentBytes snapshots the on-disk bytes at srcAbs into the
// versions tree for fileID. Returns (nil, nil) when the cooldown or size
// floor short-circuited capture; returns (meta, nil) on a successful new
// version; returns (nil, err) on a hard failure.
//
// This function is best-effort from the caller's POV: WriteContent /
// ReplaceFile etc. should log a returned error and continue with the
// actual write. A failed snapshot must never block a user's mutation.
func (s *Service) captureCurrentBytes(fileID FileID, srcAbs string, opts captureOptions) (*VersionMeta, error) {
	if !s.VersioningEnabled() || fileID.IsZero() {
		return nil, nil
	}

	cfg := s.versioningSnapshot()

	if !opts.ignoreCooldown && cfg.Cooldown > 0 {
		ts, err := s.idx.LatestVersionTimestamp(fileID)
		if err != nil {
			return nil, err
		}
		if ts > 0 {
			sinceLast := time.Now().UnixMilli() - ts
			if sinceLast < cfg.Cooldown.Milliseconds() {
				return nil, nil
			}
		}
	}

	info, err := os.Stat(srcAbs)
	if err != nil {
		// ENOENT race — the file we're trying to snapshot vanished. Not
		// fatal: the caller is about to write fresh bytes anyway.
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	if opts.honourMinSizeFloor && cfg.MinSizeForAutoV1 > 0 && info.Size() < cfg.MinSizeForAutoV1 {
		return nil, nil
	}

	versionID, err := newVersionID()
	if err != nil {
		return nil, err
	}

	dstDir, dstPath, err := s.versionStoragePath(fileID, srcAbs, versionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return nil, err
	}

	// Reflink (or fallback copy on non-CoW filesystems). Failure here is
	// the non-trivial case: the byte payload is missing so persisting
	// metadata would point at nothing. Bail without writing the index.
	if _, err := s.store.CloneFile(srcAbs, dstPath); err != nil {
		return nil, err
	}

	meta := VersionMeta{
		VersionID: versionID,
		FileID:    fileID,
		Timestamp: time.Now().UnixMilli(),
		Size:      info.Size(),
		Mode:      uint32(info.Mode().Perm()),
		Pinned:    opts.pinned,
		Label:     opts.label,
	}
	if err := s.idx.Batch(func(b Batch) error {
		b.PutVersion(meta)
		return nil
	}); err != nil {
		// Roll back the dangling blob so disk usage stays consistent
		// with the index. Best effort — log only.
		if rmErr := os.Remove(dstPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			log.Printf("[filegate] versioning: blob cleanup after index error failed: %v", rmErr)
		}
		return nil, err
	}
	return &meta, nil
}

// captureBeforeOverwrite is the convenience helper public mutation methods
// call before clobbering an existing file's bytes. Errors are logged and
// swallowed — the user-visible mutation must succeed regardless.
func (s *Service) captureBeforeOverwrite(fileID FileID, srcAbs string) {
	if _, err := s.captureCurrentBytes(fileID, srcAbs, captureOptions{
		honourMinSizeFloor: false,
	}); err != nil {
		log.Printf("[filegate] versioning: pre-overwrite capture failed for %s: %v", fileID, err)
	}
}

// captureFirstVersion is the convenience helper called right after a new
// file is created to lay down V1 (subject to the size floor for auto
// captures). Errors are logged and swallowed.
func (s *Service) captureFirstVersion(fileID FileID, srcAbs string) {
	if _, err := s.captureCurrentBytes(fileID, srcAbs, captureOptions{
		honourMinSizeFloor: true,
	}); err != nil {
		log.Printf("[filegate] versioning: V1 capture failed for %s: %v", fileID, err)
	}
}

// versionStoragePath resolves the storage location for a version blob.
// The blob lives inside the same mount as the source file so reflinks
// work; the mount is determined by walking up from srcAbs.
func (s *Service) versionStoragePath(fileID FileID, srcAbs string, vid VersionID) (dir, full string, err error) {
	mountName, _, ok := s.mountForAbsPath(srcAbs)
	if !ok {
		return "", "", errors.New("versioning: source path is outside any watched mount")
	}
	s.mu.RLock()
	mountAbs := s.mountByName[mountName]
	s.mu.RUnlock()
	if mountAbs == "" {
		return "", "", errors.New("versioning: mount has no abs path mapping")
	}
	dir = filepath.Join(mountAbs, versionsDirName, fileID.String())
	full = filepath.Join(dir, vid.String()+".bin")
	return dir, full, nil
}

// versioningSnapshot returns a copy of the current versioning config
// under read lock. Cheap because the struct is value-typed.
func (s *Service) versioningSnapshot() VersioningConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versioningCfg
}

// newVersionID mints a UUIDv7 wrapped as a VersionID.
func newVersionID() (VersionID, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return VersionID{}, err
	}
	return VersionID(u), nil
}

// String renders a VersionID as a UUID string. We piggy-back on
// FileID's existing formatting so blob filenames are human-readable.
func (v VersionID) String() string { return FileID(v).String() }

// ParseVersionID parses a UUID-formatted string (with or without
// dashes) into a VersionID. Returns ErrInvalidID on bad input —
// canonical with ParseFileID since both share the underlying UUID
// alphabet.
func ParseVersionID(s string) (VersionID, error) {
	id, err := ParseFileID(s)
	if err != nil {
		return VersionID{}, err
	}
	return VersionID(id), nil
}

// ListedVersions wraps a page of versions plus the cursor needed to
// fetch the next page. NextCursor is the zero VersionID when the page
// is final.
type ListedVersions struct {
	Items      []VersionMeta
	NextCursor VersionID
}

// ListVersions returns versions of fileID in ascending Timestamp order
// (oldest first). cursor=zero starts at the beginning. limit≤0 defaults
// to 100; the index caps at 1000 per call. Returns ErrNotFound when the
// underlying file does not exist (versions for orphans are surfaced via
// a separate API in Phase 6).
func (s *Service) ListVersions(fileID FileID, cursor VersionID, limit int) (*ListedVersions, error) {
	if !s.VersioningEnabled() {
		return nil, ErrUnsupportedFS
	}
	if _, err := s.idx.GetEntity(fileID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	// Fetch one extra to detect "more available" without an explicit
	// count query. This mirrors ListNodeChildren's pattern.
	items, err := s.idx.ListVersions(fileID, cursor, limit+1)
	if err != nil {
		return nil, err
	}
	out := &ListedVersions{}
	if len(items) > limit {
		out.Items = items[:limit]
		out.NextCursor = out.Items[len(out.Items)-1].VersionID
	} else {
		out.Items = items
	}
	return out, nil
}

// SnapshotVersion captures the current bytes of fileID as a pinned
// version regardless of cooldown or size floor. The optional label is
// stored opaquely on the version. Returns ErrUnsupportedFS when
// versioning is disabled, ErrNotFound when the file doesn't exist,
// ErrInvalidArgument when the label exceeds MaxLabelBytes, and
// ErrConflict when the per-file pinned cap is already reached.
func (s *Service) SnapshotVersion(fileID FileID, label string) (*VersionMeta, error) {
	if !s.VersioningEnabled() {
		return nil, ErrUnsupportedFS
	}
	cfg := s.versioningSnapshot()
	if cfg.MaxLabelBytes > 0 && len(label) > cfg.MaxLabelBytes {
		return nil, ErrInvalidArgument
	}
	srcAbs, err := s.ResolveAbsPath(fileID)
	if err != nil {
		return nil, err
	}
	if err := s.enforcePinnedCap(fileID, cfg, VersionID{}); err != nil {
		return nil, err
	}
	return s.captureCurrentBytes(fileID, srcAbs, captureOptions{
		ignoreCooldown:     true,
		honourMinSizeFloor: false,
		pinned:             true,
		label:              label,
	})
}

// PinVersion marks an existing version as pinned and optionally updates
// its label. Idempotent: re-pinning an already-pinned version succeeds
// and overwrites the label only when label != nil.
//
// Returns ErrUnsupportedFS when versioning is disabled, ErrNotFound when
// the file or version doesn't exist, ErrInvalidArgument when the label
// is too long, ErrConflict when pinning would exceed the per-file
// pinned cap (re-pinning an already-pinned version is allowed even at
// the cap because it doesn't add a new pin).
func (s *Service) PinVersion(fileID FileID, versionID VersionID, label *string) (*VersionMeta, error) {
	if !s.VersioningEnabled() {
		return nil, ErrUnsupportedFS
	}
	cfg := s.versioningSnapshot()
	if label != nil && cfg.MaxLabelBytes > 0 && len(*label) > cfg.MaxLabelBytes {
		return nil, ErrInvalidArgument
	}

	mu := s.versionLocks.Acquire(fileID)
	mu.Lock()
	defer mu.Unlock()

	current, err := s.idx.GetVersion(fileID, versionID)
	if err != nil {
		return nil, err
	}
	// If we'd be transitioning unpinned -> pinned, check the cap. The
	// already-pinned re-pin is exempt because the count doesn't change.
	if !current.Pinned {
		if err := s.enforcePinnedCap(fileID, cfg, versionID); err != nil {
			return nil, err
		}
	}
	updated := *current
	updated.Pinned = true
	if label != nil {
		updated.Label = *label
	}
	if err := s.idx.Batch(func(b Batch) error {
		b.PutVersion(updated)
		return nil
	}); err != nil {
		return nil, err
	}
	return &updated, nil
}

// UnpinVersion clears the pinned flag, allowing the bucket pruner to
// reclaim the version per the retention policy. Idempotent: calling on
// an already-unpinned version is a no-op success.
func (s *Service) UnpinVersion(fileID FileID, versionID VersionID) (*VersionMeta, error) {
	if !s.VersioningEnabled() {
		return nil, ErrUnsupportedFS
	}
	mu := s.versionLocks.Acquire(fileID)
	mu.Lock()
	defer mu.Unlock()

	current, err := s.idx.GetVersion(fileID, versionID)
	if err != nil {
		return nil, err
	}
	if !current.Pinned {
		return current, nil
	}
	updated := *current
	updated.Pinned = false
	if err := s.idx.Batch(func(b Batch) error {
		b.PutVersion(updated)
		return nil
	}); err != nil {
		return nil, err
	}
	return &updated, nil
}

// enforcePinnedCap returns ErrConflict when adding a new pinned version
// to fileID would exceed the configured cap. exceptID is excluded from
// the count so re-pinning an already-pinned version isn't blocked.
//
// MaxPinnedPerFile <= 0 disables the cap (unbounded; operator opt-in).
func (s *Service) enforcePinnedCap(fileID FileID, cfg VersioningConfig, exceptID VersionID) error {
	if cfg.MaxPinnedPerFile <= 0 {
		return nil
	}
	count := 0
	cursor := VersionID{}
	for {
		page, err := s.idx.ListVersions(fileID, cursor, 1000)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, v := range page {
			if v.Pinned && v.VersionID != exceptID {
				count++
				if count >= cfg.MaxPinnedPerFile {
					return ErrConflict
				}
			}
		}
		if len(page) < 1000 {
			break
		}
		cursor = page[len(page)-1].VersionID
	}
	return nil
}

// OpenVersionContent opens the byte payload for a specific version.
// Returns ErrNotFound if the file or version doesn't exist. The returned
// reader MUST be closed by the caller.
func (s *Service) OpenVersionContent(fileID FileID, versionID VersionID) (rc *os.File, meta *VersionMeta, err error) {
	if !s.VersioningEnabled() {
		return nil, nil, ErrUnsupportedFS
	}
	srcAbs, err := s.ResolveAbsPath(fileID)
	if err != nil {
		return nil, nil, err
	}
	meta, err = s.idx.GetVersion(fileID, versionID)
	if err != nil {
		return nil, nil, err
	}
	_, fullPath, err := s.versionStoragePath(fileID, srcAbs, versionID)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	return f, meta, nil
}
