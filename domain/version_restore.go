package domain

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestoreOptions controls the RestoreVersion behaviour. The zero value
// means "in-place" restore (current bytes get snapshotted, then replaced
// by the version's bytes — file ID preserved).
type RestoreOptions struct {
	// AsNewFile, when true, places the restored bytes into a NEW file in
	// the same directory and leaves the source file untouched. The new
	// file gets a fresh FileID and an EventCreated event.
	AsNewFile bool
	// Name overrides the default `<base>-restored<ext>` naming for the
	// AsNewFile path. Empty = use default. Conflict-suffix `-N` is
	// applied automatically when the chosen name already exists.
	Name string
}

// RestoreVersion brings the bytes of a previously-captured version back
// into the live filesystem. Two modes:
//
//   - In-place (default): atomic; the current bytes are first snapshotted
//     as a new version (so a misclick is undoable), then replaced by the
//     target version's bytes. File ID, parent, and Name are preserved.
//   - AsNewFile=true: the version's bytes are placed into a fresh sibling
//     file (default name `<base>-restored<ext>`, with -N suffix on
//     conflict). The source file is not touched.
//
// Returns the resulting FileMeta plus a bool indicating whether a new
// file was created (AsNewFile path) or the source was modified in place.
//
// The in-place path holds the per-file mutation lock from snapshot
// through metadata-resync so a concurrent WriteContent can't slip
// between steps and lose data.
func (s *Service) RestoreVersion(fileID FileID, versionID VersionID, opts RestoreOptions) (*FileMeta, bool, error) {
	if !s.VersioningEnabled() {
		return nil, false, ErrUnsupportedFS
	}
	if opts.AsNewFile && opts.Name != "" {
		trimmed := strings.TrimSpace(opts.Name)
		if trimmed == "" || strings.Contains(trimmed, "/") {
			return nil, false, ErrInvalidArgument
		}
		opts.Name = trimmed
	}

	// Lock FIRST, then resolve. Same race argument as WriteContent/Delete:
	// a concurrent Delete that finishes between our resolve and our
	// lock acquire would leave us holding stale paths and risk
	// resurrecting the file with its old ID.
	mu := s.versionLocks.Acquire(fileID)
	mu.Lock()
	defer mu.Unlock()

	srcAbs, err := s.ResolveAbsPath(fileID)
	if err != nil {
		return nil, false, err
	}
	version, err := s.idx.GetVersion(fileID, versionID)
	if err != nil {
		return nil, false, err
	}
	_, blobPath, err := s.versionStoragePath(fileID, srcAbs, versionID)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(blobPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}

	if opts.AsNewFile {
		meta, err := s.restoreAsNewFile(fileID, version, srcAbs, blobPath, opts.Name)
		if err != nil {
			return nil, false, err
		}
		return meta, true, nil
	}
	meta, err := s.restoreInPlaceLocked(fileID, version, srcAbs, blobPath)
	if err != nil {
		return nil, false, err
	}
	return meta, false, nil
}

// restoreInPlaceLocked replaces the live file's bytes with version.
// Atomic via reflink-to-tmp + rename. Caller MUST already hold
// versionLocks.Acquire(fileID); this function does not lock so the
// resolve-then-restore sequence in RestoreVersion stays a single
// critical section.
func (s *Service) restoreInPlaceLocked(fileID FileID, version *VersionMeta, srcAbs string, blobPath string) (*FileMeta, error) {
	// Snapshot the current bytes BEFORE replacing them so a misclicked
	// restore is itself undoable. Best-effort: if the snapshot fails
	// (cooldown swallow doesn't apply here because captureBeforeOverwrite
	// honours cooldown — that's by design, the user's recent edits are
	// already represented in the most recent auto-version).
	s.captureBeforeOverwrite(fileID, srcAbs)

	parentDir := filepath.Dir(srcAbs)
	tmpPath := filepath.Join(parentDir, ".filegate-restore-"+version.VersionID.String())

	// Reflink the version blob into the source's directory so the
	// subsequent rename(2) is intra-fs and therefore atomic.
	if _, err := s.store.CloneFile(blobPath, tmpPath); err != nil {
		return nil, err
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	// Stamp the source's stable FileID onto the tmp inode so the rename
	// preserves the ID across the swap (rename(2) keeps the source
	// inode's xattrs).
	if err := s.store.SetID(tmpPath, fileID); err != nil {
		return nil, err
	}
	if version.Mode != 0 {
		if err := os.Chmod(tmpPath, os.FileMode(version.Mode)); err != nil {
			return nil, err
		}
	}
	// Durability: fsync the tmp file before rename so the post-rename
	// inode's contents survive a crash. Without this, the rename can
	// be visible while the file's data isn't yet on stable storage.
	if err := syncFilePath(tmpPath); err != nil {
		return nil, err
	}

	if err := s.store.Rename(tmpPath, srcAbs); err != nil {
		return nil, err
	}
	cleanupTmp = false
	// fsync the parent dir so the rename itself is durable.
	if err := syncDirPath(parentDir); err != nil {
		log.Printf("[filegate] versioning restore: dir fsync %s failed: %v", parentDir, err)
	}

	if err := s.syncSingle(srcAbs); err != nil {
		return nil, err
	}
	s.bus.Publish(Event{Type: EventUpdated, ID: fileID, Path: srcAbs, At: time.Now()})
	return s.GetFile(fileID)
}

// restoreAsNewFile places the version's bytes into a fresh sibling of
// the source file. Conflict-safe via the existing commitNoReplace
// hardlink-and-remove pattern: each candidate name is tried atomically;
// EEXIST advances to the next suffix without a TOCTOU window.
func (s *Service) restoreAsNewFile(fileID FileID, version *VersionMeta, srcAbs string, blobPath string, requestedName string) (*FileMeta, error) {
	parentDir := filepath.Dir(srcAbs)
	srcBase := filepath.Base(srcAbs)
	defaultBase := requestedName
	if defaultBase == "" {
		defaultBase = restoredDefaultName(srcBase)
	}

	tmpPath := filepath.Join(parentDir, ".filegate-restore-asnew-"+version.VersionID.String())
	if _, err := s.store.CloneFile(blobPath, tmpPath); err != nil {
		return nil, err
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	newID, err := newID()
	if err != nil {
		return nil, err
	}
	if err := s.store.SetID(tmpPath, newID); err != nil {
		return nil, err
	}
	if version.Mode != 0 {
		if err := os.Chmod(tmpPath, os.FileMode(version.Mode)); err != nil {
			return nil, err
		}
	}

	// Durability: fsync the tmp blob before placing it. commitNoReplace
	// only renames/links — neither flushes the file's data — so without
	// this a crash between commit and a later flush could leave an
	// empty inode at the user-visible path.
	if err := syncFilePath(tmpPath); err != nil {
		return nil, err
	}

	// Try the default name first (suffix=0), then -1, -2, ... commitNoReplace
	// returns ErrConflict when the candidate already exists; any other
	// error aborts.
	const maxSuffix = 999
	var finalAbs string
	for i := 0; i <= maxSuffix; i++ {
		candidate := filepath.Join(parentDir, restoreCandidateName(defaultBase, i))
		if err := commitNoReplace(tmpPath, candidate); err == nil {
			finalAbs = candidate
			cleanupTmp = false // commitNoReplace removed tmpPath
			break
		} else if !errors.Is(err, ErrConflict) {
			return nil, err
		}
	}
	if finalAbs == "" {
		return nil, ErrConflict
	}
	// fsync the parent dir so the new directory entry is durable
	// alongside the file's data.
	if err := syncDirPath(parentDir); err != nil {
		log.Printf("[filegate] versioning restore-as-new: dir fsync %s failed: %v", parentDir, err)
	}

	if err := s.syncSingle(finalAbs); err != nil {
		return nil, err
	}
	actualID, err := s.store.GetID(finalAbs)
	if err != nil {
		return nil, err
	}
	s.bus.Publish(Event{Type: EventCreated, ID: actualID, Path: finalAbs, At: time.Now()})
	return s.GetFile(actualID)
}

// restoredDefaultName produces the base name for a restored sibling. The
// last extension is preserved so `report.pdf` becomes
// `report-restored.pdf` (not `report.pdf-restored`). For multi-extension
// names like `archive.tar.gz`, only the trailing `.gz` is treated as
// the extension — matching the convention humans expect.
func restoredDefaultName(src string) string {
	ext := filepath.Ext(src)
	stem := strings.TrimSuffix(src, ext)
	if stem == "" {
		stem = src // dotfiles like ".env" have no stem, no ext-split
		ext = ""
	}
	return stem + "-restored" + ext
}

// restoreCandidateName produces the suffix-`-N` variant on conflict.
// suffix=0 returns the base name unchanged; suffix>0 inserts `-N`
// between stem and extension.
func restoreCandidateName(base string, suffix int) string {
	if suffix == 0 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s-%d%s", stem, suffix, ext)
}
