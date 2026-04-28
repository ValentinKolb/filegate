package pebble

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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

	currentIndexFormatVersion uint16 = 4
)

// ErrUnsupportedIndexFormat is returned when the on-disk index version is incompatible.
var ErrUnsupportedIndexFormat = errors.New("unsupported index format version")

// ErrIndexClosed is returned when operations are attempted on a closed index.
var ErrIndexClosed = errors.New("index closed")

// ErrIndexUnavailable is returned when a fatal Pebble error has been recorded.
var ErrIndexUnavailable = errors.New("index unavailable")

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

type batch struct {
	b   pebbleBatchWriter
	err error
}

type pebbleBatchWriter interface {
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
	payload, err := encodeEntity(entity)
	if err != nil {
		b.setErr(err)
		return
	}
	b.setErr(b.b.Set(entityKey(entity.ID), payload, nil))
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

func (i *Index) Batch(fn func(domain.Batch) error) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if stateErr := i.currentStateLocked(); stateErr != nil {
		return normalizeIndexErr(stateErr)
	}
	var retErr error
	defer i.recoverIntoError(&retErr)
	b := i.db.NewBatch()
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
