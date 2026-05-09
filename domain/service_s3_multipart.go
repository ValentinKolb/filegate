package domain

import (
	"crypto/md5"
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
	Meta    *FileMeta
	Created bool // true when the destination didn't exist before
	Replayed bool // true when the uploadId record already existed
                  // and we returned its stored result without re-doing
                  // the install
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
	release := s.pathLocks.AcquirePoint(leafKey)
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
		// Overwrite — preserve existing fileID.
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

	// syncSingle to populate the entity row from the new file's
	// stat info — this fills in size, mtime, ownership, ETag MD5
	// (computed inline by the rescan-aware path? no, syncSingle
	// uses buildEntityMetadata which doesn't hash). We compute the
	// ETag explicitly below and write it via a follow-up batch.
	if err := s.syncSingle(destAbs); err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("sync after rename: %w", err)
	}

	// Hash the destination on disk so etag_md5 is correct (whole-
	// body MD5; distinct from the composite multipart ETag). This
	// is the same calculation rescan does for legacy files.
	wholeBodyMD5, hashErr := s.hashLocalFile(destAbs)
	if hashErr != nil {
		// Best-effort — leave etag_md5 empty; the rescan will
		// populate it later.
		wholeBodyMD5 = ""
	}

	// Atomic batch: entity update (with composite multipart_etag,
	// content-type, etc) + uploadId record insertion. This is the
	// single Pebble commit that makes the multipart Complete
	// durable.
	id, err := s.store.GetID(destAbs)
	if err != nil {
		return MultipartCompleteResult{}, fmt.Errorf("get installed ID: %w", err)
	}
	now := time.Now().UnixMilli()
	if err := s.idx.Batch(func(b Batch) error {
		entity, gErr := s.idx.GetEntity(id)
		if gErr != nil {
			return gErr
		}
		if entity == nil {
			return ErrNotFound
		}
		entity.ETagMD5 = wholeBodyMD5
		entity.MultipartETag = args.CompositeETag
		entity.ContentType = args.Opts.ContentType
		entity.ContentEncoding = args.Opts.ContentEncoding
		entity.ContentDisposition = args.Opts.ContentDisposition
		if len(args.Opts.UserMetadata) > 0 {
			entity.S3UserMetadata = append([]byte(nil), args.Opts.UserMetadata...)
		} else {
			entity.S3UserMetadata = nil
		}
		b.PutEntity(*entity)
		b.PutMultipartUploadRecord(args.UploadID, MultipartUploadRecord{
			FileID:        id,
			CompositeETag: args.CompositeETag,
			Bucket:        mountName,
			Key:           strings.Join(parts[1:], "/"),
			CompletedAt:   now,
		})
		return nil
	}); err != nil {
		return MultipartCompleteResult{}, err
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
	}, nil
}

// LookupMultipartUploadRecord exposes the durable uploadId record
// via the service. The S3 adapter uses this for the startup
// recovery sweep — committing manifests with a present record can
// be promoted to done without re-driving the install.
func (s *Service) LookupMultipartUploadRecord(uploadID [16]byte) (*MultipartUploadRecord, error) {
	return s.idx.LookupMultipartUploadRecord(uploadID)
}

// hashLocalFile is a tiny helper to whole-body-MD5-hash a file on
// disk. Matches the format used elsewhere (lowercase hex).
func (s *Service) hashLocalFile(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
