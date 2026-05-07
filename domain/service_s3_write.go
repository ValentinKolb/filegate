package domain

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// S3WriteOptions carries the per-object metadata that an S3 PutObject
// adapter passes through to the domain layer. Every field is taken
// AT FACE VALUE on each write — an empty ContentType means "no
// explicit content type", which clears any prior value; the S3
// adapter is expected to forward exactly what it received from the
// client (and absent headers translate to empty strings here).
//
// IfNoneMatchAny enables PutObject's "If-None-Match: *" semantic:
// the write is rejected with ErrConflict when the target already
// exists.
//
// REST callers continue to use WriteContent / WriteContentByVirtualPath
// — those entry-points clear all S3-only metadata fields (the
// "non-S3 write means non-S3 file" rule from plan §7).
type S3WriteOptions struct {
	ContentType        string
	ContentEncoding    string
	ContentDisposition string
	UserMetadata       []byte // serialized x-amz-meta-* blob
	IfNoneMatchAny     bool   // S3 If-None-Match: *
}

// WriteObjectS3 is the S3-style write entry-point used by the (M1+)
// S3 adapter. Differences from WriteContentByVirtualPath:
//
//   - Default conflict mode is Overwrite, matching S3 PutObject's
//     silent-overwrite semantic. ConflictError is only used when the
//     caller explicitly sets IfNoneMatchAny.
//   - The entity's S3-only metadata fields (ContentType,
//     ContentEncoding, ContentDisposition, S3UserMetadata) are SET
//     from opts after the write, instead of being cleared as
//     syncSingleAfterLocalWrite does for REST callers.
//   - multipart_etag is cleared (this entry-point handles single-part
//     PutObject; CompleteMultipartUpload uses its own dedicated path
//     in M2 because of the composite-ETag and idempotency machinery).
//
// Returns the resulting FileMeta, a bool indicating whether a new
// object was created (false on overwrite of existing), and any error.
// On IfNoneMatchAny rejection the error is ErrConflict.
func (s *Service) WriteObjectS3(virtualPath string, body io.Reader, opts S3WriteOptions) (*FileMeta, bool, error) {
	vp, err := sanitizeVirtualPath(virtualPath)
	if err != nil {
		return nil, false, err
	}
	parts := strings.Split(vp, "/")
	if len(parts) < 2 {
		return nil, false, ErrInvalidArgument
	}
	fileName := strings.TrimSpace(parts[len(parts)-1])
	if fileName == "" {
		return nil, false, ErrInvalidArgument
	}

	s.mu.RLock()
	mountID, ok := s.mountIDByName[parts[0]]
	s.mu.RUnlock()
	if !ok {
		return nil, false, ErrNotFound
	}

	// Parent chain via MkdirRelative (idempotent skip-on-existing).
	// MkdirRelative now path-locks each segment internally — we don't
	// need to lock the parent chain here.
	parentID := mountID
	if len(parts) > 2 {
		parentPath := strings.Join(parts[1:len(parts)-1], "/")
		parentMeta, err := s.MkdirRelative(mountID, parentPath, true, nil, ConflictSkip)
		if err != nil {
			return nil, false, err
		}
		parentID = parentMeta.ID
	}

	// Acquire the leaf path-lock for the entire write. The
	// existence-check + conditional-rejection + create/overwrite
	// must be one critical section.
	parentVP, err := s.VirtualPath(parentID)
	if err != nil {
		return nil, false, err
	}
	pmount, prel, vpOK := splitVirtualPath(parentVP)
	if !vpOK {
		return nil, false, ErrInvalidArgument
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

	// Resolve target under lock.
	targetID, lookupErr := s.ResolvePath(vp)
	exists := lookupErr == nil
	if lookupErr != nil && !errors.Is(lookupErr, ErrNotFound) {
		return nil, false, lookupErr
	}

	// Conditional rejection (If-None-Match: *).
	if exists && opts.IfNoneMatchAny {
		return nil, false, ErrConflict
	}

	// Refuse to clobber a directory: S3 has no concept of
	// "directory" objects in the path-style we expose, so a PUT
	// that lands at a directory path is a semantic error.
	if exists {
		targetMeta, getErr := s.GetFile(targetID)
		if getErr != nil {
			return nil, false, getErr
		}
		if targetMeta.Type != "file" {
			return nil, false, ErrConflict
		}
	}

	// Resolve parent abs path and the target on disk.
	parentAbs, err := s.ResolveAbsPath(parentID)
	if err != nil {
		return nil, false, err
	}
	targetAbs := filepath.Join(parentAbs, fileName)

	if exists {
		// Overwrite path. Acquire the file-id lock too — the
		// versioning subsystem coordinates Snapshot/Pin/Restore via
		// versionLocks, and a parallel S3 PutObject racing one of
		// those would corrupt version state without this. Path-lock
		// alone serializes path-mutators against each other; the
		// id-lock serializes against versioning ops on the same id.
		fileMu := s.versionLocks.Acquire(targetID)
		fileMu.Lock()
		defer fileMu.Unlock()

		// Capture pre-overwrite version for the versioning
		// subsystem (it'll honour the per-bucket s3_auto_version
		// setting added in M2; for now it behaves like REST
		// overwrite).
		s.captureBeforeOverwrite(targetID, targetAbs)
		// Resolve current entity to keep its file mode / ownership
		// stable across the rewrite.
		curMeta, err := s.GetFile(targetID)
		if err != nil {
			return nil, false, err
		}
		preserveID := targetID
		md5Hex, err := s.writeFileAtomic(targetAbs, body, os.FileMode(curMeta.Mode), ownershipFromFileMeta(curMeta), &preserveID, false)
		if err != nil {
			return nil, false, err
		}
		if err := s.syncSingleAfterS3Write(targetAbs, md5Hex, opts); err != nil {
			return nil, false, err
		}
		s.bus.Publish(Event{Type: EventUpdated, ID: targetID, Path: targetAbs, At: time.Now()})
		updated, err := s.GetFile(targetID)
		if err != nil {
			return nil, false, err
		}
		return updated, false, nil
	}

	// Create path. New ID, mustNotExist=true so the filesystem
	// rejects any race that snuck a file in between our existence
	// check and the create.
	newID, err := newID()
	if err != nil {
		return nil, false, err
	}
	effectiveOwnership, err := s.effectiveOwnership(parentID, nil)
	if err != nil {
		return nil, false, err
	}
	md5Hex, err := s.writeFileAtomic(targetAbs, body, 0o644, effectiveOwnership, &newID, true)
	if err != nil {
		return nil, false, err
	}
	if err := s.syncSingleAfterS3Write(targetAbs, md5Hex, opts); err != nil {
		return nil, false, err
	}
	id, err := s.store.GetID(targetAbs)
	if err != nil {
		return nil, false, err
	}
	s.bus.Publish(Event{Type: EventCreated, ID: id, Path: targetAbs, At: time.Now()})
	s.captureFirstVersion(id, targetAbs)
	created, err := s.GetFile(id)
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// syncSingleAfterS3Write is the S3-write counterpart of
// syncSingleAfterLocalWrite: same atomic syncSingle, but the
// follow-up PutEntity SETS the S3-only metadata fields from opts
// instead of clearing them. multipart_etag is always cleared
// because single-PUT writes are not multipart-uploaded; M2's
// CompleteMultipartUpload sets multipart_etag through a different
// path that doesn't go through this helper.
func (s *Service) syncSingleAfterS3Write(absPath string, md5Hex string, opts S3WriteOptions) error {
	if err := s.syncSingle(absPath); err != nil {
		return err
	}
	id, err := s.store.GetID(absPath)
	if err != nil {
		return err
	}
	return s.idx.Batch(func(b Batch) error {
		entity, err := s.idx.GetEntity(id)
		if err != nil {
			return err
		}
		if entity == nil {
			return nil
		}
		entity.ETagMD5 = md5Hex
		entity.MultipartETag = ""
		entity.ContentType = opts.ContentType
		entity.ContentEncoding = opts.ContentEncoding
		entity.ContentDisposition = opts.ContentDisposition
		if len(opts.UserMetadata) > 0 {
			entity.S3UserMetadata = append([]byte(nil), opts.UserMetadata...)
		} else {
			entity.S3UserMetadata = nil
		}
		b.PutEntity(*entity)
		return nil
	})
}
