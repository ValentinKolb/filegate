package pebble

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble"

	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/fgbin"
)

const (
	familyEntity byte = 0x01
	familyChild  byte = 0x02
	// 0x03 is intentionally skipped — the legacy inode keyspace lived
	// there before its removal at format version 6. Reusing 0x03 would
	// risk collisions with stale bytes on indexes that didn't get a
	// clean rebuild between bumps.
	familyVersion byte = 0x04

	// currentIndexFormatVersion was bumped from 6 to 7 when the per-file
	// versioning subsystem (`familyVersion`) was added. The bump forces
	// a clean rebuild so old indexes don't surface partial data; the new
	// keyspace is empty after the rebuild and gets populated as files
	// are written or manually snapshotted.
	currentIndexFormatVersion uint16 = 7
)

// ErrUnsupportedIndexFormat is returned when the on-disk index version is incompatible.
var ErrUnsupportedIndexFormat = errors.New("unsupported index format version")

// ErrIndexClosed is returned when operations are attempted on a closed index.
var ErrIndexClosed = errors.New("index closed")

// ErrIndexUnavailable is returned when a fatal Pebble error has been recorded.
var ErrIndexUnavailable = errors.New("index unavailable")

// IsTerminalError reports whether err signals that the index is no longer
// usable and the caller's loop should stop. Recognises ErrIndexClosed,
// ErrIndexUnavailable, and the raw "pebble: closed" string returned by
// background pebble goroutines that bypass the index runOp wrapper.
func IsTerminalError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrIndexClosed) || errors.Is(err, ErrIndexUnavailable) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "pebble: closed")
}

var indexFormatVersionKey = []byte{0x00, 'f', 'g', ':', 'i', 'd', 'x', ':', 'f', 'm', 't'}

// Index is a thread-safe metadata store backed by a Pebble database.
type Index struct {
	mu       sync.RWMutex
	db       *pebble.DB
	closed   bool
	fatalErr error
}

// Open creates or opens a Pebble-backed index at the given path.
func Open(path string, blockCacheBytes int64) (*Index, error) {
	cache := pebble.NewCache(blockCacheBytes)
	pebbleLogger := &nonFatalPebbleLogger{}
	opts := &pebble.Options{
		Cache: cache,
		Levels: []pebble.LevelOptions{{
			Compression: pebble.ZstdCompression,
		}},
		Logger: pebbleLogger,
	}
	db, err := pebble.Open(path, opts)
	if err != nil {
		cache.Unref()
		return nil, err
	}
	idx := &Index{db: db}
	pebbleLogger.onFatal = idx.markFatal
	if err := ensureIndexFormatVersion(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return idx, nil
}

type nonFatalPebbleLogger struct {
	onFatal func(error)
}

func (l *nonFatalPebbleLogger) Infof(format string, args ...interface{}) {
	log.Printf(format, args...)
}

func (l *nonFatalPebbleLogger) Errorf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

// Fatalf is called by Pebble on unrecoverable internal errors. The Pebble
// Logger contract requires Fatalf to not return (like log.Fatalf). We mark the
// index as fatally failed via onFatal so subsequent operations return errors
// gracefully, then panic to satisfy the contract. The panic is recoverable by
// callers if needed, which is safer than os.Exit(1).
func (l *nonFatalPebbleLogger) Fatalf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	err := fmt.Errorf("%w: %s", ErrIndexUnavailable, msg)
	if l.onFatal != nil {
		l.onFatal(err)
	}
	panic(err)
}

func (i *Index) markFatal(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.fatalErr == nil {
		i.fatalErr = err
	}
}

func (i *Index) currentStateLocked() error {
	if i.closed {
		return ErrIndexClosed
	}
	if i.fatalErr != nil {
		return i.fatalErr
	}
	return nil
}

func normalizeIndexErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrIndexClosed) || errors.Is(err, ErrIndexUnavailable) {
		return err
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "pebble: closed") {
		return ErrIndexClosed
	}
	if strings.Contains(msg, "fatal commit error") {
		return fmt.Errorf("%w: %v", ErrIndexUnavailable, err)
	}
	return err
}

func encodeFormatVersion(v uint16) []byte {
	var out [2]byte
	binary.BigEndian.PutUint16(out[:], v)
	return out[:]
}

func decodeFormatVersion(v []byte) (uint16, error) {
	if len(v) != 2 {
		return 0, fmt.Errorf("invalid index format version payload")
	}
	return binary.BigEndian.Uint16(v), nil
}

func ensureIndexFormatVersion(db *pebble.DB) error {
	value, closer, err := db.Get(indexFormatVersionKey)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return db.Set(indexFormatVersionKey, encodeFormatVersion(currentIndexFormatVersion), pebble.Sync)
		}
		return err
	}
	defer closer.Close()
	found, err := decodeFormatVersion(value)
	if err != nil {
		return err
	}
	if found != currentIndexFormatVersion {
		return fmt.Errorf("%w: %d (expected %d)", ErrUnsupportedIndexFormat, found, currentIndexFormatVersion)
	}
	return nil
}

func entityKey(id domain.FileID) []byte {
	key := make([]byte, 1+16)
	key[0] = familyEntity
	copy(key[1:], id[:])
	return key
}

func childPrefix(parentID domain.FileID) []byte {
	prefix := make([]byte, 1+16)
	prefix[0] = familyChild
	copy(prefix[1:], parentID[:])
	return prefix
}

func childTypeByte(isDir bool) byte {
	if isDir {
		return 0
	}
	return 1
}

func childKey(parentID domain.FileID, isDir bool, name string) []byte {
	p := childPrefix(parentID)
	p = append(p, childTypeByte(isDir), 0)
	return append(p, []byte(name)...)
}


func prefixUpperBound(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	upper := append([]byte(nil), prefix...)
	for i := len(upper) - 1; i >= 0; i-- {
		if upper[i] != 0xFF {
			upper[i]++
			return upper[:i+1]
		}
	}
	return nil
}

func encodeEntity(e domain.Entity) ([]byte, error) {
	exifJSON, _ := json.Marshal(e.Exif)
	rec := fgbin.Entity{
		ID:       [16]byte(e.ID),
		ParentID: [16]byte(e.ParentID),
		IsDir:    e.IsDir,
		Size:     e.Size,
		MtimeNs:  e.Mtime * int64(1_000_000),
		UID:      e.UID,
		GID:      e.GID,
		Mode:     e.Mode,
		Device:   e.Device,
		Inode:    e.Inode,
		Nlink:    e.Nlink,
		Name:     e.Name,
		MimeType: e.MimeType,
	}
	if len(exifJSON) > 0 && string(exifJSON) != "{}" {
		rec.Extensions = append(rec.Extensions, fgbin.Extension{
			FieldID: fgbin.FieldEXIF,
			Value:   exifJSON,
		})
	}
	return fgbin.EncodeEntity(rec)
}

func decodeEntity(id domain.FileID, value []byte) (domain.Entity, error) {
	rec, err := fgbin.DecodeEntity(value)
	if err != nil {
		return domain.Entity{}, err
	}
	if domain.FileID(rec.ID) != id {
		return domain.Entity{}, fmt.Errorf("entity id mismatch")
	}
	var exif map[string]string
	if raw, ok := fgbin.ExtensionByID(rec.Extensions, fgbin.FieldEXIF); ok && len(raw) > 0 {
		exif = make(map[string]string)
		if err := json.Unmarshal(raw, &exif); err != nil {
			// Corrupt EXIF blob — keep the entity readable but surface the
			// fact that on-disk metadata is malformed so it can be repaired.
			log.Printf("[filegate] index: dropping malformed EXIF for %s: %v", id, err)
			exif = nil
		}
	}
	return domain.Entity{
		ID:       id,
		ParentID: domain.FileID(rec.ParentID),
		Name:     rec.Name,
		IsDir:    rec.IsDir,
		Size:     rec.Size,
		Mtime:    rec.MtimeNs / int64(1_000_000),
		UID:      rec.UID,
		GID:      rec.GID,
		Mode:     rec.Mode,
		Device:   rec.Device,
		Inode:    rec.Inode,
		Nlink:    rec.Nlink,
		MimeType: rec.MimeType,
		Exif:     exif,
	}, nil
}

func encodeChild(entry domain.DirEntry) ([]byte, error) {
	return fgbin.EncodeChild(fgbin.Child{
		ID:      [16]byte(entry.ID),
		IsDir:   entry.IsDir,
		Size:    entry.Size,
		MtimeNs: entry.Mtime * int64(1_000_000),
		Name:    entry.Name,
	})
}

func decodeChild(name string, value []byte) (domain.DirEntry, error) {
	rec, err := fgbin.DecodeChild(value)
	if err != nil {
		return domain.DirEntry{}, err
	}
	if rec.Name != name {
		return domain.DirEntry{}, fmt.Errorf("child name mismatch")
	}
	return domain.DirEntry{
		ID:    domain.FileID(rec.ID),
		Name:  rec.Name,
		IsDir: rec.IsDir,
		Size:  rec.Size,
		Mtime: rec.MtimeNs / int64(1_000_000),
	}, nil
}

// versionPrefix returns the key prefix for all versions of fileID. Used for
// "list all versions of file X" iterations.
func versionPrefix(fileID domain.FileID) []byte {
	prefix := make([]byte, 1+16)
	prefix[0] = familyVersion
	copy(prefix[1:], fileID[:])
	return prefix
}

// versionKey returns the full Pebble key for a single version. The
// VersionID is a UUIDv7, whose first 6 bytes are the BigEndian unix-ms
// timestamp — appending it to the file prefix gives natural chronological
// sort within a file with the random tail breaking ms collisions.
func versionKey(fileID domain.FileID, versionID domain.VersionID) []byte {
	key := make([]byte, 1+16+16)
	key[0] = familyVersion
	copy(key[1:17], fileID[:])
	copy(key[17:], versionID[:])
	return key
}

func encodeVersion(meta domain.VersionMeta) ([]byte, error) {
	return fgbin.EncodeVersion(fgbin.Version{
		VersionID: [16]byte(meta.VersionID),
		FileID:    [16]byte(meta.FileID),
		Timestamp: meta.Timestamp,
		Size:      meta.Size,
		Mode:      meta.Mode,
		DeletedAt: meta.DeletedAt,
		Pinned:    meta.Pinned,
		Label:     []byte(meta.Label),
	})
}

func decodeVersion(value []byte) (domain.VersionMeta, error) {
	rec, err := fgbin.DecodeVersion(value)
	if err != nil {
		return domain.VersionMeta{}, err
	}
	return domain.VersionMeta{
		VersionID: domain.VersionID(rec.VersionID),
		FileID:    domain.FileID(rec.FileID),
		Timestamp: rec.Timestamp,
		Size:      rec.Size,
		Mode:      rec.Mode,
		Pinned:    rec.Pinned,
		Label:     string(rec.Label),
		DeletedAt: rec.DeletedAt,
	}, nil
}

func (i *Index) GetEntity(id domain.FileID) (out *domain.Entity, err error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return nil, normalizeIndexErr(stateErr)
	}
	defer i.recoverIntoError(&err)
	v, closer, err := i.db.Get(entityKey(id))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, normalizeIndexErr(err)
	}
	e, err := decodeEntity(id, v)
	closeErr := closer.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, normalizeIndexErr(closeErr)
	}
	return &e, nil
}

func (i *Index) LookupChild(parentID domain.FileID, name string) (out *domain.DirEntry, err error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return nil, normalizeIndexErr(stateErr)
	}
	defer i.recoverIntoError(&err)
	for _, isDir := range []bool{true, false} {
		v, closer, err := i.db.Get(childKey(parentID, isDir, name))
		if err != nil {
			if errors.Is(err, pebble.ErrNotFound) {
				continue
			}
			return nil, normalizeIndexErr(err)
		}
		entry, err := decodeChild(name, v)
		closeErr := closer.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, normalizeIndexErr(closeErr)
		}
		return &entry, nil
	}
	return nil, domain.ErrNotFound
}

func (i *Index) ListChildren(parentID domain.FileID, after string, limit int) ([]domain.DirEntry, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return nil, normalizeIndexErr(stateErr)
	}
	var retErr error
	defer i.recoverIntoError(&retErr)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	prefix := childPrefix(parentID)
	start := prefix
	cursorType := byte(0)
	if after != "" {
		entry, err := i.LookupChild(parentID, after)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			cursorType = childTypeByte(entry.IsDir)
			start = childKey(parentID, entry.IsDir, after)
		}
	}

	iterOpts := &pebble.IterOptions{LowerBound: prefix}
	if upper := prefixUpperBound(prefix); upper != nil {
		iterOpts.UpperBound = upper
	}
	iter, err := i.db.NewIter(iterOpts)
	if err != nil {
		return nil, normalizeIndexErr(err)
	}
	defer iter.Close()

	entries := make([]domain.DirEntry, 0, limit)
	for ok := iter.SeekGE(start); ok && iter.Valid(); ok = iter.Next() {
		k := iter.Key()
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if len(k) <= len(prefix)+2 {
			continue
		}
		kind := k[len(prefix)]
		name := string(k[len(prefix)+2:])
		if after != "" && kind == cursorType && name <= after {
			continue
		}
		entry, err := decodeChild(name, iter.Value())
		if err != nil {
			continue
		}
		entries = append(entries, entry)
		if len(entries) >= limit {
			break
		}
	}
	return entries, retErr
}

func (i *Index) ListEntities() ([]domain.Entity, error) {
	out := make([]domain.Entity, 0, 1024)
	if err := i.ForEachEntity(func(e domain.Entity) error {
		out = append(out, e)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

func (i *Index) ForEachEntity(fn func(domain.Entity) error) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return normalizeIndexErr(stateErr)
	}
	var retErr error
	defer i.recoverIntoError(&retErr)
	prefix := []byte{familyEntity}
	iterOpts := &pebble.IterOptions{LowerBound: prefix}
	if upper := prefixUpperBound(prefix); upper != nil {
		iterOpts.UpperBound = upper
	}
	iter, err := i.db.NewIter(iterOpts)
	if err != nil {
		return normalizeIndexErr(err)
	}
	defer iter.Close()

	for ok := iter.SeekGE(prefix); ok && iter.Valid(); ok = iter.Next() {
		k := iter.Key()
		if len(k) == 0 || k[0] != familyEntity {
			break
		}
		if len(k) != 17 {
			continue
		}
		var id domain.FileID
		copy(id[:], k[1:17])
		e, err := decodeEntity(id, iter.Value())
		if err != nil {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return retErr
}

// GetVersion returns the metadata for a single version, or domain.ErrNotFound.
func (i *Index) GetVersion(fileID domain.FileID, versionID domain.VersionID) (out *domain.VersionMeta, err error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return nil, normalizeIndexErr(stateErr)
	}
	defer i.recoverIntoError(&err)
	v, closer, err := i.db.Get(versionKey(fileID, versionID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, normalizeIndexErr(err)
	}
	defer closer.Close()
	meta, err := decodeVersion(v)
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// ListVersions returns versions of fileID, ordered by Timestamp ascending
// (oldest first). The `after` cursor is the VersionID returned at the end
// of the previous page; pass the zero VersionID to start from the beginning.
// limit ≤ 0 defaults to 100, limit > 1000 caps to 1000.
func (i *Index) ListVersions(fileID domain.FileID, after domain.VersionID, limit int) ([]domain.VersionMeta, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return nil, normalizeIndexErr(stateErr)
	}
	var retErr error
	defer i.recoverIntoError(&retErr)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	prefix := versionPrefix(fileID)
	start := prefix
	if !after.IsZero() {
		// Strict-greater-than: skip past the cursor's exact key. SeekGE
		// to the cursor and skip it on the first iteration would also
		// work but adds a branch in the hot loop.
		startKey := versionKey(fileID, after)
		start = append(append([]byte(nil), startKey...), 0x00)
	}
	iterOpts := &pebble.IterOptions{LowerBound: prefix}
	if upper := prefixUpperBound(prefix); upper != nil {
		iterOpts.UpperBound = upper
	}
	iter, err := i.db.NewIter(iterOpts)
	if err != nil {
		return nil, normalizeIndexErr(err)
	}
	defer iter.Close()

	out := make([]domain.VersionMeta, 0, limit)
	for ok := iter.SeekGE(start); ok && iter.Valid() && len(out) < limit; ok = iter.Next() {
		k := iter.Key()
		if !bytes.HasPrefix(k, prefix) || len(k) != 1+16+16 {
			break
		}
		meta, err := decodeVersion(iter.Value())
		if err != nil {
			continue
		}
		out = append(out, meta)
	}
	return out, retErr
}

// LatestVersionTimestamp returns the Timestamp of the newest version of
// fileID, or 0 if no versions exist. Used by the cooldown check on the
// auto-capture path. The Pebble keys are sorted by VersionID ascending,
// which (because UUIDv7's leading bytes are BigEndian unix-ms) is the
// same as Timestamp ascending — the last key in the prefix is the newest.
func (i *Index) LatestVersionTimestamp(fileID domain.FileID) (ts int64, err error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return 0, normalizeIndexErr(stateErr)
	}
	defer i.recoverIntoError(&err)
	prefix := versionPrefix(fileID)
	iterOpts := &pebble.IterOptions{LowerBound: prefix}
	if upper := prefixUpperBound(prefix); upper != nil {
		iterOpts.UpperBound = upper
	}
	iter, ierr := i.db.NewIter(iterOpts)
	if ierr != nil {
		return 0, normalizeIndexErr(ierr)
	}
	defer iter.Close()
	if !iter.SeekLT(iterOpts.UpperBound) {
		return 0, nil
	}
	k := iter.Key()
	if !bytes.HasPrefix(k, prefix) || len(k) != 1+16+16 {
		return 0, nil
	}
	meta, derr := decodeVersion(iter.Value())
	if derr != nil {
		return 0, derr
	}
	return meta.Timestamp, nil
}

// ForEachFileVersions iterates the versions keyspace and groups
// adjacent rows by FileID. fn is invoked once per file with all its
// versions in ascending Timestamp order. Memory is bounded to one
// file's versions at a time, which makes the pass safe even on indexes
// with millions of versions across many files.
func (i *Index) ForEachFileVersions(fn func(fileID domain.FileID, versions []domain.VersionMeta) error) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return normalizeIndexErr(stateErr)
	}
	var retErr error
	defer i.recoverIntoError(&retErr)

	prefix := []byte{familyVersion}
	iterOpts := &pebble.IterOptions{LowerBound: prefix}
	if upper := prefixUpperBound(prefix); upper != nil {
		iterOpts.UpperBound = upper
	}
	iter, err := i.db.NewIter(iterOpts)
	if err != nil {
		return normalizeIndexErr(err)
	}
	defer iter.Close()

	var current domain.FileID
	var batch []domain.VersionMeta
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := fn(current, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for ok := iter.SeekGE(prefix); ok && iter.Valid(); ok = iter.Next() {
		k := iter.Key()
		if !bytes.HasPrefix(k, prefix) || len(k) != 1+16+16 {
			break
		}
		var fid domain.FileID
		copy(fid[:], k[1:17])
		if len(batch) == 0 {
			current = fid
		} else if fid != current {
			if err := flush(); err != nil {
				return err
			}
			current = fid
		}
		meta, derr := decodeVersion(iter.Value())
		if derr != nil {
			continue
		}
		batch = append(batch, meta)
	}
	if err := flush(); err != nil {
		return err
	}
	return retErr
}

// MarkVersionsDeleted sets DeletedAt = deletedAt on every version of
// fileID whose DeletedAt is currently zero. Idempotent: re-running with a
// later timestamp does not bump already-marked entries (the original
// transition time wins, so the grace period is measured from when the
// file actually went away, not from a re-mark). Returns the number of
// rows updated.
func (i *Index) MarkVersionsDeleted(fileID domain.FileID, deletedAt int64) (n int, err error) {
	if deletedAt <= 0 {
		return 0, domain.ErrInvalidArgument
	}
	versions, err := i.ListVersions(fileID, domain.VersionID{}, 0)
	if err != nil {
		return 0, err
	}
	pending := make([]domain.VersionMeta, 0, len(versions))
	for _, v := range versions {
		if v.DeletedAt == 0 {
			v.DeletedAt = deletedAt
			pending = append(pending, v)
		}
	}
	for len(versions) == 1000 {
		// Page through if the file has many versions. ListVersions caps
		// at 1000 per call.
		next, lerr := i.ListVersions(fileID, versions[len(versions)-1].VersionID, 0)
		if lerr != nil {
			return 0, lerr
		}
		versions = next
		for _, v := range versions {
			if v.DeletedAt == 0 {
				v.DeletedAt = deletedAt
				pending = append(pending, v)
			}
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}
	if err := i.Batch(func(b domain.Batch) error {
		for _, v := range pending {
			b.PutVersion(v)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return len(pending), nil
}

type batch struct {
	b   pebbleBatchReadWriter
	err error
}

// pebbleBatchReadWriter is the subset of *pebble.Batch we use during the
// batch lifecycle. Reads are needed so PutEntity can detect renames (old
// child entry to delete) and inode changes (mapping to update). Get reads
// from the batch's own pending writes plus the underlying DB, which is the
// behaviour we want for self-consistency within a single batch.
type pebbleBatchReadWriter interface {
	Get(key []byte) ([]byte, io.Closer, error)
	Set(key, value []byte, opts *pebble.WriteOptions) error
	Delete(key []byte, opts *pebble.WriteOptions) error
}

func (b *batch) setErr(err error) {
	if err == nil || b.err != nil {
		return
	}
	b.err = err
}

func (b *batch) PutEntity(entity domain.Entity) {
	if b.err != nil {
		return
	}
	// Read the previous entity so we can detect a same-id rename
	// (Name or ParentID changed for the same entity ID) and drop the
	// stale child entry under the old parent. This is the common case
	// fast path; ReconcileDirectory in the consumer is the safety net
	// for the cases this misses (e.g. when find-new doesn't emit at all
	// for an in-subvol directory rename).
	//
	// Skipped for hard-link siblings (nlink > 1) because they share an ID
	// across multiple (parent, name) pairs and "the previous Put was
	// stale" is not a valid inference. snapshot/cp-a duplicates can't
	// trigger this path because resolveOrReissueID upstream re-issues a
	// fresh UUID for them — by the time PutEntity sees them they have
	// their own ID with no prior entity record.
	prev, err := b.loadEntityForBatch(entity.ID)
	if err != nil {
		b.setErr(err)
		return
	}
	if prev != nil {
		nlinkSafe := prev.Nlink <= 1 && entity.Nlink <= 1
		if nlinkSafe && (prev.ParentID != entity.ParentID || prev.Name != entity.Name) {
			if err := b.b.Delete(childKey(prev.ParentID, true, prev.Name), nil); err != nil {
				b.setErr(err)
				return
			}
			if err := b.b.Delete(childKey(prev.ParentID, false, prev.Name), nil); err != nil {
				b.setErr(err)
				return
			}
		}
	}
	payload, err := encodeEntity(entity)
	if err != nil {
		b.setErr(err)
		return
	}
	b.setErr(b.b.Set(entityKey(entity.ID), payload, nil))
}

// loadEntityForBatch returns the entity stored under id, reading from the
// batch's pending writes plus the underlying DB. Returns (nil, nil) when no
// entity is stored.
func (b *batch) loadEntityForBatch(id domain.FileID) (*domain.Entity, error) {
	v, closer, err := b.b.Get(entityKey(id))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()
	e, err := decodeEntity(id, v)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (b *batch) PutChild(parentID domain.FileID, name string, entry domain.DirEntry) {
	if b.err != nil {
		return
	}
	payload, err := encodeChild(entry)
	if err != nil {
		b.setErr(err)
		return
	}
	b.setErr(b.b.Set(childKey(parentID, entry.IsDir, name), payload, nil))
}

func (b *batch) DelChild(parentID domain.FileID, name string) {
	if b.err != nil {
		return
	}
	if err := b.b.Delete(childKey(parentID, true, name), nil); err != nil {
		b.setErr(err)
		return
	}
	b.setErr(b.b.Delete(childKey(parentID, false, name), nil))
}

func (b *batch) DelEntity(id domain.FileID) {
	if b.err != nil {
		return
	}
	b.setErr(b.b.Delete(entityKey(id), nil))
}

func (b *batch) PutVersion(meta domain.VersionMeta) {
	if b.err != nil {
		return
	}
	if meta.FileID.IsZero() || meta.VersionID.IsZero() {
		b.setErr(domain.ErrInvalidArgument)
		return
	}
	payload, err := encodeVersion(meta)
	if err != nil {
		b.setErr(err)
		return
	}
	b.setErr(b.b.Set(versionKey(meta.FileID, meta.VersionID), payload, nil))
}

func (b *batch) DelVersion(fileID domain.FileID, versionID domain.VersionID) {
	if b.err != nil {
		return
	}
	b.setErr(b.b.Delete(versionKey(fileID, versionID), nil))
}

func (i *Index) Batch(fn func(domain.Batch) error) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return normalizeIndexErr(stateErr)
	}
	var retErr error
	defer i.recoverIntoError(&retErr)
	// Indexed batch is required because PutEntity reads the previous
	// entity to detect a same-id rename (so it can drop the stale child
	// entry under the old parent in the same atomic write). NewBatch
	// does not support Get inside the batch.
	b := i.db.NewIndexedBatch()
	defer b.Close()
	wrap := &batch{b: b}
	if err := fn(wrap); err != nil {
		return err
	}
	if wrap.err != nil {
		return wrap.err
	}
	commitErr := normalizeIndexErr(b.Commit(pebble.Sync))
	if commitErr != nil {
		return commitErr
	}
	return retErr
}

func (i *Index) recoverIntoError(target *error) {
	if rec := recover(); rec != nil {
		err := fmt.Errorf("%w: %v", ErrIndexUnavailable, rec)
		i.markFatal(err)
		*target = err
	}
}

func (i *Index) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	return normalizeIndexErr(i.db.Close())
}
