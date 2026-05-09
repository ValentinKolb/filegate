package domain

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CopyObjectArgs bundles the inputs for the S3 server-side copy
// op. All checks (source conditionals, destination conditionals,
// size limit) happen under the destination path-lock so a
// concurrent write can't squeeze in between the conditional and
// the install.
//
// MetadataDirective semantics (matches AWS spec):
//   - "COPY" (default): the dest inherits the source's ContentType,
//     ContentEncoding, ContentDisposition, and S3UserMetadata.
//     DestOpts is ignored on metadata fields.
//   - "REPLACE": DestOpts.ContentType/Encoding/Disposition/UserMetadata
//     are written to the dest entity verbatim. The source's
//     metadata is dropped.
//
// ETag rule (single-object copy, matches AWS spec): the
// destination's ETagMD5 is set to the source's ETagMD5 — the
// bytes are byte-identical, so the MD5 must be too. MultipartETag
// is ALWAYS cleared on the destination, because multipart-copy
// (which would preserve the composite ETag) is out of scope; a
// single-call CopyObject of a multipart-source presents itself as
// a fresh single-MD5 object.
type CopyObjectArgs struct {
	SourceVP string
	DestVP   string

	// Source preconditions (x-amz-copy-source-if-*). Empty values
	// disable the corresponding check. Times are zero-valued when
	// the header is absent.
	SourceIfMatch         string
	SourceIfNoneMatch     string
	SourceIfModifiedSince time.Time
	SourceIfUnmodifiedSince time.Time

	// Destination preconditions (If-Match, If-None-Match: *).
	DestIfMatch         string
	DestIfNoneMatchAny  bool

	// MetadataDirective: "COPY" (default) or "REPLACE".
	MetadataDirective string

	// DestOpts is consulted when MetadataDirective="REPLACE".
	// IfMatch/IfNoneMatchAny inside DestOpts are NOT honored —
	// use the explicit Dest fields above.
	DestOpts S3WriteOptions
}

// CopyObjectResult is what CopyObjectS3 returns. ETag is the
// destination's new ETag (== source's ETagMD5). LastModified is
// the new on-disk mtime of the destination.
type CopyObjectResult struct {
	Meta         *FileMeta
	ETag         string
	LastModified time.Time
	Created      bool // true when the dest didn't exist before
	Reflinked    bool // true when CloneFile took the reflink fast-path
}

// CopyObjectS3 implements S3 server-side single-object copy. Both
// the source and the destination must resolve to existing mounts;
// the source must be a file (S3 has no directory copy). When the
// two paths sit in the same mount, the byte-copy is delegated to
// CloneFile which uses btrfs reflinks where available. Across
// mounts a streaming io.Copy is used.
//
// Returns ErrConflict when any precondition fails (the adapter
// maps that to 412 PreconditionFailed). Returns
// ErrCopySourceTooLarge when the source exceeds the 5 GiB
// single-copy limit; multipart-copy lives in M3+1 (out of scope).
func (s *Service) CopyObjectS3(args CopyObjectArgs) (*CopyObjectResult, error) {
	srcVP, err := sanitizeVirtualPath(args.SourceVP)
	if err != nil {
		return nil, err
	}
	dstVP, err := sanitizeVirtualPath(args.DestVP)
	if err != nil {
		return nil, err
	}
	srcParts := strings.Split(srcVP, "/")
	dstParts := strings.Split(dstVP, "/")
	if len(srcParts) < 2 || len(dstParts) < 2 {
		return nil, ErrInvalidArgument
	}
	dstFileName := strings.TrimSpace(dstParts[len(dstParts)-1])
	if dstFileName == "" {
		return nil, ErrInvalidArgument
	}

	directive := strings.ToUpper(strings.TrimSpace(args.MetadataDirective))
	if directive == "" {
		directive = "COPY"
	}
	if directive != "COPY" && directive != "REPLACE" {
		return nil, ErrInvalidArgument
	}

	// Resolve dest mount + parent chain (creates parent dirs as
	// needed, mirroring WriteObjectS3 behaviour).
	s.mu.RLock()
	dstMountID, ok := s.mountIDByName[dstParts[0]]
	srcMountID, srcMountOK := s.mountIDByName[srcParts[0]]
	s.mu.RUnlock()
	if !ok || !srcMountOK {
		return nil, ErrNotFound
	}
	dstParentID := dstMountID
	if len(dstParts) > 2 {
		parentRel := strings.Join(dstParts[1:len(dstParts)-1], "/")
		parentMeta, perr := s.MkdirRelative(dstMountID, parentRel, true, nil, ConflictSkip)
		if perr != nil {
			return nil, perr
		}
		dstParentID = parentMeta.ID
	}

	// Compute the lock keys for both endpoints. Same-bucket-same-
	// key (a self-copy with REPLACE) collapses to one point lock,
	// otherwise we acquire a pair.
	dstParentVP, err := s.VirtualPath(dstParentID)
	if err != nil {
		return nil, err
	}
	dstMount, dstParentRel, vpOK := splitVirtualPath(dstParentVP)
	if !vpOK {
		return nil, ErrInvalidArgument
	}
	var dstLeafRel string
	if dstParentRel == "" {
		dstLeafRel = dstFileName
	} else {
		dstLeafRel = dstParentRel + "/" + dstFileName
	}
	dstKey := pathLockKey(dstMount, dstLeafRel)
	srcKey := pathLockKey(srcParts[0], strings.Join(srcParts[1:], "/"))

	var release func()
	if srcKey == dstKey {
		release = s.pathLocks.AcquirePoint(dstKey)
	} else {
		release = s.pathLocks.AcquirePointPair(srcKey, dstKey)
	}
	defer release()

	// Resolve source under lock and validate it.
	srcID, err := s.ResolvePath(srcVP)
	if err != nil {
		return nil, err
	}
	srcEntity, err := s.idx.GetEntity(srcID)
	if err != nil {
		return nil, err
	}
	if srcEntity == nil {
		return nil, ErrNotFound
	}
	if srcEntity.IsDir {
		return nil, ErrInvalidArgument
	}
	if srcEntity.Size > maxSingleCopyBytes {
		return nil, ErrCopySourceTooLarge
	}
	srcMtime := time.UnixMilli(srcEntity.Mtime)
	srcETag := effectiveS3ETag(srcEntity)

	// Source preconditions. "If-Match" and "If-None-Match" both
	// compare against the source's effective ETag (which honors
	// multipart_etag if set — the value an S3 client would have
	// retrieved from a prior GET/HEAD). Modified/Unmodified-Since
	// compare against the source's mtime.
	if args.SourceIfMatch != "" && !ifMatchSatisfied(args.SourceIfMatch, srcETag) {
		return nil, ErrConflict
	}
	if args.SourceIfNoneMatch != "" && ifMatchSatisfied(args.SourceIfNoneMatch, srcETag) {
		return nil, ErrConflict
	}
	if !args.SourceIfModifiedSince.IsZero() && !srcMtime.After(args.SourceIfModifiedSince) {
		return nil, ErrConflict
	}
	if !args.SourceIfUnmodifiedSince.IsZero() && srcMtime.After(args.SourceIfUnmodifiedSince) {
		return nil, ErrConflict
	}

	// Destination preconditions.
	dstID, dstLookupErr := s.ResolvePath(dstVP)
	dstExists := dstLookupErr == nil
	if dstLookupErr != nil && !errors.Is(dstLookupErr, ErrNotFound) {
		return nil, dstLookupErr
	}
	if dstExists && args.DestIfNoneMatchAny {
		return nil, ErrConflict
	}
	if args.DestIfMatch != "" {
		if !dstExists {
			return nil, ErrConflict
		}
		dstEntity, getErr := s.idx.GetEntity(dstID)
		if getErr != nil {
			return nil, getErr
		}
		if !ifMatchSatisfied(args.DestIfMatch, effectiveS3ETag(dstEntity)) {
			return nil, ErrConflict
		}
	}
	if dstExists {
		dstMeta, gErr := s.GetFile(dstID)
		if gErr != nil {
			return nil, gErr
		}
		if dstMeta.Type != "file" {
			return nil, ErrConflict
		}
	}

	// Resolve absolute paths for the byte copy.
	srcAbs, err := s.ResolveAbsPath(srcID)
	if err != nil {
		return nil, err
	}
	dstParentAbs, err := s.ResolveAbsPath(dstParentID)
	if err != nil {
		return nil, err
	}
	dstAbs := filepath.Join(dstParentAbs, dstFileName)

	// Determine the dest fileID — preserve on overwrite, mint on
	// create. captureBeforeOverwrite snapshots the prior content
	// so versioning sees the bytes that are about to be replaced.
	var preserveID FileID
	created := !dstExists
	if dstExists {
		preserveID = dstID
		s.captureBeforeOverwrite(dstID, dstAbs)
	} else {
		newID, idErr := newID()
		if idErr != nil {
			return nil, idErr
		}
		preserveID = newID
	}

	// Choose the copy strategy. Same-mount → CloneFile (reflink on
	// btrfs, byte-copy fallback elsewhere). Cross-mount must always
	// stream because reflinks can't span filesystems. Self-copy
	// (src and dest are the SAME file on disk) skips the byte copy
	// entirely — the only meaningful work is the metadata
	// REPLACE that follows. AWS clients use this to update
	// metadata in place ("the S3 metadata-update trick").
	reflinked := false
	selfCopy := srcAbs == dstAbs
	switch {
	case selfCopy:
		// No byte movement. The xattr ID is already correct, the
		// stat info doesn't change. Skip ahead to metadata update.
	case srcMountID == dstMountID:
		// Try CloneFile. Even on ext4 it returns false + does a
		// byte copy via the same code path, so we don't need a
		// fallback branch here.
		linked, copyErr := copyForS3(s, srcAbs, dstAbs)
		if copyErr != nil {
			return nil, fmt.Errorf("copy bytes: %w", copyErr)
		}
		reflinked = linked
	default:
		if err := streamCopy(srcAbs, dstAbs); err != nil {
			return nil, fmt.Errorf("stream copy: %w", err)
		}
	}

	// Stamp the dest with the chosen fileID via xattr so resolveOrReissueID
	// picks it up if anything else syncs the file later. Set perms
	// to 0o644 (S3 has no notion of POSIX modes, matches single-PUT).
	// Self-copy already has the right ID + mode; skip both syscalls
	// to avoid a redundant xattr write under the existing ID.
	if !selfCopy {
		if err := s.store.SetID(dstAbs, preserveID); err != nil {
			_ = os.Remove(dstAbs)
			return nil, fmt.Errorf("set dest ID: %w", err)
		}
		if err := os.Chmod(dstAbs, 0o644); err != nil {
			_ = os.Remove(dstAbs)
			return nil, fmt.Errorf("chmod dest: %w", err)
		}
	}

	// Build dest entity inline — single batch with entity + child +
	// flat-key. Mirrors the multipart Complete pattern: no
	// syncSingle (which would publish the entity in its own batch
	// and create a partial-state visibility window).
	info, err := os.Lstat(dstAbs)
	if err != nil {
		return nil, fmt.Errorf("stat dest: %w", err)
	}
	dstEntity := buildEntityMetadata(preserveID, dstParentID, dstFileName, dstAbs, info)
	dstEntity.ETagMD5 = srcETag                    // bytes-identical → MD5-identical
	dstEntity.MultipartETag = ""                   // single-copy never preserves composite

	switch directive {
	case "COPY":
		dstEntity.ContentType = srcEntity.ContentType
		dstEntity.ContentEncoding = srcEntity.ContentEncoding
		dstEntity.ContentDisposition = srcEntity.ContentDisposition
		if len(srcEntity.S3UserMetadata) > 0 {
			dstEntity.S3UserMetadata = append([]byte(nil), srcEntity.S3UserMetadata...)
		} else {
			dstEntity.S3UserMetadata = nil
		}
	case "REPLACE":
		dstEntity.ContentType = args.DestOpts.ContentType
		dstEntity.ContentEncoding = args.DestOpts.ContentEncoding
		dstEntity.ContentDisposition = args.DestOpts.ContentDisposition
		if len(args.DestOpts.UserMetadata) > 0 {
			dstEntity.S3UserMetadata = append([]byte(nil), args.DestOpts.UserMetadata...)
		} else {
			dstEntity.S3UserMetadata = nil
		}
	}

	dstDirEntry := DirEntry{
		ID:    preserveID,
		Name:  dstFileName,
		IsDir: false,
		Size:  info.Size(),
		Mtime: info.ModTime().UnixMilli(),
	}
	mountRel := strings.Join(dstParts[1:], "/")
	if err := s.idx.Batch(func(b Batch) error {
		b.PutEntity(dstEntity)
		b.PutChild(dstParentID, dstFileName, dstDirEntry)
		b.PutFlatKey(dstMount, mountRel, preserveID)
		return nil
	}); err != nil {
		return nil, err
	}

	// Cache invalidation + first-version capture (create path) +
	// event publish.
	s.invalidateCacheByID(preserveID)
	if parentVP, vpErr := s.VirtualPath(dstParentID); vpErr == nil {
		s.InvalidatePathCache(parentVP)
		s.InvalidatePathCache(parentVP + "/" + dstFileName)
	}
	if created {
		s.captureFirstVersion(preserveID, dstAbs)
		s.bus.Publish(Event{Type: EventCreated, ID: preserveID, Path: dstAbs, At: time.Now()})
	} else {
		s.bus.Publish(Event{Type: EventUpdated, ID: preserveID, Path: dstAbs, At: time.Now()})
	}

	finalMeta, err := s.GetFile(preserveID)
	if err != nil {
		return nil, err
	}
	return &CopyObjectResult{
		Meta:         finalMeta,
		ETag:         srcETag,
		LastModified: info.ModTime(),
		Created:      created,
		Reflinked:    reflinked,
	}, nil
}

// maxSingleCopyBytes is the 5 GiB limit AWS imposes on
// CopyObject. Above that, clients must use UploadPartCopy
// (multipart-copy), which we don't support yet — the source-too-
// large case surfaces as ErrCopySourceTooLarge so the adapter
// maps it to S3 EntityTooLarge.
const maxSingleCopyBytes = 5 * 1024 * 1024 * 1024

// ErrCopySourceTooLarge signals a CopyObject source exceeded the
// 5 GiB single-call ceiling. The S3 adapter maps this to the
// EntityTooLarge error code.
var ErrCopySourceTooLarge = errors.New("copy source exceeds 5 GiB single-copy limit")

// copyForS3 invokes the store's CloneFile (FICLONE on btrfs, byte
// copy elsewhere). Returns (reflinked, error). The dest must NOT
// already exist — CloneFile rejects an existing target. We delete
// any prior dest before calling.
func copyForS3(s *Service, srcAbs, dstAbs string) (bool, error) {
	if _, err := os.Lstat(dstAbs); err == nil {
		if err := os.Remove(dstAbs); err != nil {
			return false, fmt.Errorf("remove existing dest: %w", err)
		}
	}
	return s.store.CloneFile(srcAbs, dstAbs)
}

// streamCopy is the cross-mount fallback. Always reads through
// the regular file API; reflinks across filesystems aren't
// supported by the kernel.
func streamCopy(srcAbs, dstAbs string) error {
	in, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer in.Close()
	if _, err := os.Lstat(dstAbs); err == nil {
		if err := os.Remove(dstAbs); err != nil {
			return fmt.Errorf("remove existing dest: %w", err)
		}
	}
	out, err := os.OpenFile(dstAbs, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dstAbs)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dstAbs)
		return err
	}
	return out.Close()
}
