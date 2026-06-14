package domain

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MultipartCompleteArgs bundles the per-call inputs for
// CompleteMultipartUpload. Returned via CompleteMultipartUploadResult
// so the caller (S3 adapter) can build the response XML.
type MultipartCompleteArgs struct {
	// VirtualPath is the destination ("/bucket/key").
	VirtualPath string
	// SrcPath is the on-disk path to the assembled body that the
	// caller has already concat'd from its parts. The file is
	// renamed into the destination (same mount required); after a
	// successful commit it does not exist at SrcPath anymore.
	SrcPath string
	// UploadID is the 16-byte multipart upload identifier. Stored
	// in the durable uploadId-record so retries return idempotently.
	UploadID [16]byte
	// CompositeETag is the AWS-shape multipart ETag
	// "<hex(MD5(concat-of-part-MD5s))>-<N>". Stored on the entity
	// as MultipartETag and returned as ETag to GET/HEAD.
	CompositeETag string
	// Opts carries the S3-specific object metadata (Content-Type,
	// Content-Encoding, Content-Disposition, x-amz-meta-* blob).
	// IfMatch and IfNoneMatchAny are ignored — Complete uses its
	// own idempotency model via the uploadId record.
	Opts S3WriteOptions
}

// MultipartCompleteResult is what CompleteMultipartUpload returns.
type MultipartCompleteResult struct {
	Meta     *FileMeta
	Created  bool // true when the destination didn't exist before
	Replayed bool // true when the uploadId record already existed
	// and we returned its stored result without re-doing
	// the install
	// Timings reports per-phase durations of the domain-side install
	// (zero on the Replayed fast path, which does no install). The
	// adapter observes these into the complete-phase histogram —
	// the trace-substitute that tells operators whether a slow
	// Complete is lock contention, the whole-body re-hash, or the
	// Pebble commit. The concat phase is measured adapter-side.
	Timings MultipartCompleteTimings
}

// MultipartCompleteTimings holds the domain-side sub-phase durations
// of a multipart Complete install.
type MultipartCompleteTimings struct {
	LockWait    time.Duration // time spent acquiring the destination path-lock
	Hash        time.Duration // whole-body MD5 of the installed object
	PebbleBatch time.Duration // the atomic entity + record commit
}

// CompleteMultipartUpload commits a multipart upload atomically:
// installs the assembled body at the destination, updates the
// entity record with the composite ETag, and writes the durable
// uploadId record in the same Pebble batch.
//
// The 2-phase commit + recovery protocol is documented in the S3
// plan dex y0zjz8bi. This implementation does:
//
//  1. Acquire destination path-lock.
//  2. Idempotency check — if the uploadId record exists, return
//     its stored result without re-doing the install.
//  3. Atomic rename of SrcPath → destAbs (must be on the same fs).
//  4. Pebble batch: update entity (multipart_etag, S3 metadata) +
//     insert uploadId record. Both committed atomically.
//
// A crash between step 3 and step 4 leaves the destination with the
// new bytes but no uploadId record. A retry redoes step 3 (rename
// from a now-missing SrcPath fails with ENOENT — caller surfaces
// NoSuchUpload). Cleaning up that case is handled by the caller's
// recovery logic.
func (s *Service) CompleteMultipartUpload(args MultipartCompleteArgs) (MultipartCompleteResult, error) {
	if args.VirtualPath == "" {
		return MultipartCompleteResult{}, ErrInvalidArgument
	}
	vp, err := sanitizeVirtualPath(args.VirtualPath)
	if err != nil {
		return MultipartCompleteResult{}, err
	}
	parts := strings.Split(vp, "/")
	if len(parts) < 2 {
		return MultipartCompleteResult{}, ErrInvalidArgument
	}
	mountName := parts[0]
	fileName := strings.TrimSpace(parts[len(parts)-1])
	if fileName == "" {
		return MultipartCompleteResult{}, ErrInvalidArgument
	}

	s.mu.RLock()
	mountID, ok := s.mountIDByName[mountName]
	s.mu.RUnlock()
	if !ok {
		return MultipartCompleteResult{}, ErrNotFound
	}

	// Idempotency check FIRST — before path-lock acquisition. A
	// successful Complete left the uploadId record committed; a
	// retry returns its stored result without contention. This
	// also short-circuits before we touch any filesystem state.
	if record, err := s.idx.LookupMultipartUploadRecord(args.UploadID); err == nil && record != nil {
		// The record's stored entity may have been deleted or
		// overwritten since the original Complete. Per S3 spec we
		// return the historical commit's result regardless.
		meta, _ := s.GetFile(record.FileID)
		return MultipartCompleteResult{
			Meta:     meta,
			Replayed: true,
		}, nil
	}

	// Ensure parent dirs exist (mirrors WriteObjectS3 behavior).
	parentID := mountID
	if len(parts) > 2 {
		parentPath := strings.Join(parts[1:len(parts)-1], "/")
		parentMeta, perr := s.MkdirRelative(mountID, parentPath, true, nil, ConflictSkip)
		if perr != nil {
			return MultipartCompleteResult{}, perr
		}
		parentID = parentMeta.ID
	}

	// Compute leaf path-lock key and acquire.
	parentVP, err := s.VirtualPath(parentID)
	if err != nil {
		return MultipartCompleteResult{}, err
	}
	pmount, prel, vpOK := splitVirtualPath(parentVP)
	if !vpOK {
		return MultipartCompleteResult{}, ErrInvalidArgument
	}
	var leafRel string
	if prel == "" {
		leafRel = fileName
	} else {
		leafRel = prel + "/" + fileName
	}
	leafKey := pathLockKey(pmount, leafRel)
	var timings MultipartCompleteTimings
	lockStart := time.Now()
	release := s.pathLocks.AcquirePoint(leafKey)
	timings.LockWait = time.Since(lockStart)
	defer release()

	// Re-check uploadId record under the lock (a concurrent Complete
	// could have committed between our pre-lock check and now).
	if record, err := s.idx.LookupMultipartUploadRecord(args.UploadID); err == nil && record != nil {
		meta, _ := s.GetFile(record.FileID)
		return MultipartCompleteResult{
			Meta:     meta,
			Replayed: true,
		}, nil
	}

	// Resolve target.
	parentAbs, err := s.ResolveAbsPath(parentID)
	if err != nil {
		return MultipartCompleteResult{}, err
	}
	destAbs := filepath.Join(parentAbs, fileName)

	// Determine create-or-overwrite.
	created := false
	var preserveID *FileID
	if existingID, lookupErr := s.ResolvePath(vp); lookupErr == nil {
		// Overwrite — preserve existing fileID. Hold the file-id lock
		// through the install so versioning ops (Snapshot/Pin/Restore)
		// on the same id can't interleave; mirrors WriteObjectS3's
		// overwrite path. Acquired after the path-lock per the
		// documented lock order.
		fileMu := s.versionLocks.Acquire(existingID)
		fileMu.Lock()
		defer fileMu.Unlock()
		existingMeta, gErr := s.GetFile(existingID)
		if gErr != nil {
			return MultipartCompleteResult{}, gErr
		}
		if existingMeta.Type != "file" {
			return MultipartCompleteResult{}, ErrConflict
		}
		s.captureBeforeOverwrite(existingID, destAbs)
		preserveID = &existingID
	} else if errors.Is(lookupErr, ErrNotFound) {
		// Create — generate a fresh fileID.
		newFileID, idErr := newID()
		if idErr != nil {
			return MultipartCompleteResult{}, idErr
		}
		preserveID = &newFileID
		created = true
	} else {
		return MultipartCompleteResult{}, lookupErr
	}

	// Stamp the source with the destination's fileID via xattr so
	// resolveOrReissueID picks it up after the rename. Set perms
	// to 0o644 (S3 has no notion of POSIX modes; this matches the
	// single-PUT default).
	if err := s.store.SetID(args.SrcPath, *preserveID); err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("set source ID: %w", err)
	}
	if err := os.Chmod(args.SrcPath, 0o644); err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("chmod source: %w", err)
	}

	// Atomic rename (same-fs assumption). For cross-fs we'd need a
	// copy fallback — multipart staging always lives under the
	// same mount as the destination, so the assumption holds.
	if err := s.store.Rename(args.SrcPath, destAbs); err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("install: %w", err)
	}

	// Stat the freshly-renamed destination so we can build the
	// entity record inline. We deliberately do NOT call
	// s.syncSingle here — syncSingle commits the entity in its own
	// Pebble batch, which would create a window where the object
	// is visible without the durable uploadId record. Folding the
	// entity write and the record insertion into the SAME batch
	// below is what makes the install + commit atomic.
	info, err := os.Lstat(destAbs)
	if err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("stat after rename: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return MultipartCompleteResult{}, ErrForbidden
	}
	id, err := s.store.GetID(destAbs)
	if err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("get installed ID: %w", err)
	}

	// Hash the destination on disk so etag_md5 is correct (whole-
	// body MD5; distinct from the composite multipart ETag). The
	// rename is atomic and we hold the path-lock — no other writer
	// can mutate the file between rename and hash.
	hashStart := time.Now()
	wholeBodyHashes, hashErr := s.hashLocalFileHashes(destAbs)
	timings.Hash = time.Since(hashStart)
	if hashErr != nil {
		// Best-effort — leave etag_md5 empty; the rescan will
		// populate it later.
		wholeBodyHashes = ContentHashes{}
	}

	// Build the entity record from the rebuild stat + composite ETag
	// + S3 metadata. This is the same shape syncSingle would write
	// via buildEntityMetadata, plus the S3-specific fields the
	// adapter passed in.
	entity := buildEntityMetadata(id, parentID, fileName, destAbs, info)
	entity.ETagMD5 = wholeBodyHashes.MD5Hex
	entity.SHA256 = wholeBodyHashes.SHA256
	entity.MultipartETag = args.CompositeETag
	entity.ContentType = args.Opts.ContentType
	entity.ContentEncoding = args.Opts.ContentEncoding
	entity.ContentDisposition = args.Opts.ContentDisposition
	if len(args.Opts.UserMetadata) > 0 {
		entity.S3UserMetadata = append([]byte(nil), args.Opts.UserMetadata...)
	} else {
		entity.S3UserMetadata = nil
	}
	dirEntry := DirEntry{
		ID:    id,
		Name:  fileName,
		IsDir: false,
		Size:  info.Size(),
		Mtime: info.ModTime().UnixMilli(),
	}

	// Atomic batch: entity row + parent child-edge + flat-key + the
	// durable uploadId record. A crash before this commits leaves
	// the destination with new bytes on disk but NO entity row,
	// which the recovery sweep can detect (entity-by-id lookup
	// returns nil) and re-drive safely. A crash AFTER this commits
	// makes the upload durable; a retry sees the record and
	// short-circuits idempotently.
	now := time.Now().UnixMilli()
	mountRel := strings.Join(parts[1:], "/")
	batchStart := time.Now()
	if err := s.idx.Batch(func(b Batch) error {
		b.PutEntity(entity)
		b.PutChild(parentID, fileName, dirEntry)
		b.PutFlatKey(mountName, mountRel, id)
		b.PutMultipartUploadRecord(args.UploadID, MultipartUploadRecord{
			FileID:        id,
			CompositeETag: args.CompositeETag,
			Bucket:        mountName,
			Key:           mountRel,
			CompletedAt:   now,
		})
		return nil
	}); err != nil {
		return MultipartCompleteResult{}, err
	}
	timings.PebbleBatch = time.Since(batchStart)

	// Cache invalidation mirrors what syncSingle does — without it
	// stale path-cache entries can keep handing out the prior fileID
	// after an overwrite that minted a new one (we don't here, but
	// be defensive).
	s.invalidateCacheByID(id)
	if parentVP, vpErr := s.VirtualPath(parentID); vpErr == nil {
		s.InvalidatePathCache(parentVP)
		s.InvalidatePathCache(parentVP + "/" + fileName)
	}

	// Auto V1 + EventCreated mirroring the single-PUT path.
	if created {
		s.captureFirstVersion(id, destAbs)
		s.bus.Publish(Event{Type: EventCreated, ID: id, Path: destAbs, At: time.Now()})
	} else {
		s.bus.Publish(Event{Type: EventUpdated, ID: id, Path: destAbs, At: time.Now()})
	}

	finalMeta, err := s.GetFile(id)
	if err != nil {
		return MultipartCompleteResult{}, err
	}
	return MultipartCompleteResult{
		Meta:    finalMeta,
		Created: created,
		Timings: timings,
	}, nil
}

// LookupMultipartUploadRecord exposes the durable uploadId record
// via the service. The S3 adapter uses this for the startup
// recovery sweep — committing manifests with a present record can
// be promoted to done without re-driving the install.
func (s *Service) LookupMultipartUploadRecord(uploadID [16]byte) (*MultipartUploadRecord, error) {
	return s.idx.LookupMultipartUploadRecord(uploadID)
}

// DeleteMultipartUploadRecord removes the durable uploadId record
// from Pebble. Used by the cleanup loop after the retention
// window — once the record is gone, retried Complete calls for
// that upload will get a fresh redrive (or, if the staging dir
// is also gone, NoSuchUpload). Idempotent: deleting a missing
// record is not an error.
func (s *Service) DeleteMultipartUploadRecord(uploadID [16]byte) error {
	return s.idx.Batch(func(b Batch) error {
		b.DelMultipartUploadRecord(uploadID)
		return nil
	})
}

func (s *Service) CreateActiveMultipartUpload(upload ActiveMultipartUpload) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutActiveMultipartUpload(upload)
		return nil
	})
}

func (s *Service) LookupActiveMultipartUpload(uploadID string) (*ActiveMultipartUpload, error) {
	return s.idx.LookupActiveMultipartUpload(uploadID)
}

func (s *Service) ListActiveMultipartUploads(bucket string) ([]ActiveMultipartUpload, error) {
	return s.idx.ListActiveMultipartUploads(bucket)
}

func (s *Service) PutActiveMultipartPart(part ActiveMultipartPart) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutActiveMultipartPart(part)
		return nil
	})
}

func (s *Service) ListActiveMultipartParts(uploadID string) ([]ActiveMultipartPart, error) {
	return s.idx.ListActiveMultipartParts(uploadID)
}

func (s *Service) UpdateActiveMultipartUpload(upload ActiveMultipartUpload) error {
	return s.idx.Batch(func(b Batch) error {
		b.PutActiveMultipartUpload(upload)
		return nil
	})
}

func (s *Service) DeleteActiveMultipartUpload(uploadID string) error {
	return s.idx.Batch(func(b Batch) error {
		b.DelActiveMultipartParts(uploadID)
		b.DelActiveMultipartUpload(uploadID)
		return nil
	})
}

// hashLocalFileHashes fingerprints a local file in one read pass.
func (s *Service) hashLocalFileHashes(absPath string) (ContentHashes, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return ContentHashes{}, err
	}
	defer f.Close()
	md5Hash := md5.New()
	shaHash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(md5Hash, shaHash), f); err != nil {
		return ContentHashes{}, err
	}
	return ContentHashes{
		MD5Hex: hex.EncodeToString(md5Hash.Sum(nil)),
		SHA256: "sha256:" + hex.EncodeToString(shaHash.Sum(nil)),
	}, nil
}

func (s *Service) hashLocalFile(absPath string) (string, error) {
	hashes, err := s.hashLocalFileHashes(absPath)
	return hashes.MD5Hex, err
}
