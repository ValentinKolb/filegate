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
// IfMatch (when non-empty) is the unquoted ETag value the client
// supplied in If-Match. The write succeeds only if the existing
// object's effective ETag (multipart_etag if set, else etag_md5)
// matches one of the candidates. Compare-and-swap semantics —
// the check happens UNDER the path-lock, so there is no TOCTOU
// race against concurrent writers.
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
	IfMatch            string // S3 If-Match: "<etag>" (or comma-separated list, unquoted)
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
	// Conditional rejection (If-Match: "<etag>"). When the client
	// sets If-Match and the target doesn't exist, the precondition
	// fails (you can't compare-and-swap against a non-existent
	// object). When it does exist, the current effective ETag must
	// match one of the supplied candidates.
	if opts.IfMatch != "" {
		if !exists {
			return nil, false, ErrConflict
		}
		targetEntity, getErr := s.idx.GetEntity(targetID)
		if getErr != nil {
			return nil, false, getErr
		}
		current := effectiveS3ETag(targetEntity)
		if !ifMatchSatisfied(opts.IfMatch, current) {
			return nil, false, ErrConflict
		}
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

// S3MetadataView is the S3-only metadata an S3 adapter needs to
// render GET/HEAD responses. Returned by GetS3Metadata. Empty
// strings / nil slices mean "field wasn't set" — the adapter
// translates those to "header omitted".
type S3MetadataView struct {
	ContentType        string
	ContentEncoding    string
	ContentDisposition string
	UserMetadata       []byte // serialized x-amz-meta-* blob
	MultipartETag      string // composite ETag for multipart-uploaded files
}

// GetS3Metadata returns the S3-only extension fields stored on the
// entity. Returns ErrNotFound when the entity doesn't exist. The
// REST API doesn't surface these fields; the S3 adapter is the only
// expected caller. ETag (single-MD5) is on FileMeta — use GetFile
// for that. ETag selection rule (multipart vs single) is the
// caller's job: prefer MultipartETag when set, fall back to
// FileMeta.ETag.
func (s *Service) GetS3Metadata(id FileID) (*S3MetadataView, error) {
	entity, err := s.idx.GetEntity(id)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, ErrNotFound
	}
	return &S3MetadataView{
		ContentType:        entity.ContentType,
		ContentEncoding:    entity.ContentEncoding,
		ContentDisposition: entity.ContentDisposition,
		UserMetadata:       entity.S3UserMetadata,
		MultipartETag:      entity.MultipartETag,
	}, nil
}

// effectiveS3ETag returns the ETag value an S3 client sees on the
// wire for entity. Multipart-uploaded files present their composite
// ETag; everything else presents the single-MD5. Used by the
// conditional-write path-locked check inside WriteObjectS3 and
// DeleteIfMatch.
func effectiveS3ETag(entity *Entity) string {
	if entity == nil {
		return ""
	}
	if entity.MultipartETag != "" {
		return entity.MultipartETag
	}
	return entity.ETagMD5
}

// ifMatchSatisfied reports whether condition (an unquoted ETag or
// comma-separated list of unquoted ETags, possibly with W/ weak
// validators) matches current. Strong-only — weak validators are
// rejected per RFC 7232 §3.1 since If-Match is the strong-mode
// header.
func ifMatchSatisfied(condition, current string) bool {
	condition = strings.TrimSpace(condition)
	if condition == "*" {
		return current != ""
	}
	current = strings.Trim(current, `"`)
	for _, candidate := range strings.Split(condition, ",") {
		candidate = strings.TrimSpace(candidate)
		// Reject weak validators on If-Match.
		if strings.HasPrefix(candidate, "W/") {
			continue
		}
		candidate = strings.Trim(candidate, `"`)
		if candidate == current {
			return true
		}
	}
	return false
}

// DeleteIfMatch is Delete + an S3-style If-Match precondition. The
// ETag check happens INSIDE the same path-lock + file-id-lock
// critical section as the actual delete (via deleteCoreLocked),
// so a concurrent PUT that changes the object between our peek
// and the delete cannot win — its lock-acquire blocks behind ours.
//
// Pass an empty ifMatch to fall through to plain Delete.
//
// Returns ErrConflict when the precondition fails (S3 maps this to
// 412 PreconditionFailed at the adapter layer).
func (s *Service) DeleteIfMatch(id FileID, ifMatch string) error {
	if ifMatch == "" {
		return s.Delete(id)
	}
	entity, err := s.idx.GetEntity(id)
	if err != nil {
		return err
	}
	if entity.ParentID.IsZero() {
		return ErrForbidden
	}
	fn := func() error {
		// Re-fetch INSIDE the lock so concurrent writers that
		// committed between our pre-lock peek and the lock
		// acquisition are reflected in the ETag check.
		current, err := s.idx.GetEntity(id)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrNotFound
		}
		if !ifMatchSatisfied(ifMatch, effectiveS3ETag(current)) {
			return ErrConflict
		}
		return s.deleteCoreLocked(id)
	}
	if entity.IsDir {
		return s.withSubtreeLockByID(id, fn)
	}
	return s.withFilePointLock(id, fn)
}

// IterateFlatKeysForS3 is the Service-layer entry-point for the S3
// adapter's ListObjectsV2 path. Forwards to the index iterator with
// the same semantics — relPath order, prefix-bounded scan, after as
// strict-greater bound, limit caps the calls (zero = unlimited).
// The S3 adapter is the only expected caller; the wrapper exists
// so the adapter doesn't have to know about the underlying Pebble
// keyspace shape.
func (s *Service) IterateFlatKeysForS3(mountName, prefix, after string, limit int, fn func(relPath string, id FileID) (bool, error)) error {
	return s.idx.IterateFlatKeys(mountName, prefix, after, limit, fn)
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
