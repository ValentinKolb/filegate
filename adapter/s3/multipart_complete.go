package s3

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/valentinkolb/filegate/domain"
)

// handleCompleteMultipartUpload finalizes a multipart upload using
// the 2-phase commit protocol described in domain/service_s3_multipart.go:
//
//  1. Look up the upload's staging dir + manifest.
//  2. Validate the client-supplied parts list (XML body) against the
//     manifest: every PartNumber present, ETags match, ascending
//     order, ≥5 MiB on every non-final part.
//  3. Concat parts in order into <stage>/complete.tmp while computing
//     the composite ETag — hex(MD5(concat-of-part-MD5-bytes)) + "-N".
//  4. Mark manifest phase=committing (so a crash mid-install can be
//     reconciled by the recovery sweep).
//  5. Call domain.CompleteMultipartUpload, which atomically renames
//     complete.tmp into place and writes the durable uploadId record
//     in the same Pebble batch. The domain layer owns the path-lock
//     + idempotency check.
//  6. Mark manifest phase=done with the result snapshot. The cleanup
//     loop GCs done manifests on a retention timer.
//
// Idempotency: the durable record (Pebble) is the source of truth;
// the manifest is purely on-disk recovery state. A retried Complete
// that finds an existing record returns the historical result.
func (rt *router) handleCompleteMultipartUpload(w http.ResponseWriter, r *http.Request, verified *sigV4Result, bucket, key string) {
	uploadID := r.URL.Query().Get("uploadId")
	if uploadID == "" {
		writeError(w, r, errInvalidArgument, "uploadId required", withBucket(bucket), withKey(key))
		return
	}
	if err := validateObjectKey(key); err != nil {
		writeError(w, r, errInvalidArgument, err.Error(), withBucket(bucket), withKey(key))
		return
	}

	loc, err := rt.findStageDir(uploadID)
	if err != nil {
		writeError(w, r, errNoSuchUpload, "upload not found", withBucket(bucket), withKey(key))
		return
	}
	manifest, err := readManifest(loc.StageDir)
	if err != nil {
		writeError(w, r, errNoSuchUpload, "upload not found", withBucket(bucket), withKey(key))
		return
	}
	if manifest.Bucket != bucket || manifest.Key != key {
		writeError(w, r, errInvalidArgument, "uploadId belongs to a different bucket/key", withBucket(bucket), withKey(key))
		return
	}

	// Decode the 32-hex-char on-disk uploadId into the 16-byte form
	// the durable record expects. We pin this here so a malformed
	// dirname (shouldn't happen — we generate them) surfaces as
	// InternalError rather than corrupting the keyspace.
	var uploadIDBytes [16]byte
	if raw, decErr := hex.DecodeString(uploadID); decErr != nil || len(raw) != 16 {
		writeError(w, r, errInternalError, "uploadId is not 16-byte hex", withBucket(bucket), withKey(key))
		return
	} else {
		copy(uploadIDBytes[:], raw)
	}

	// Phase=done means the original Complete already succeeded. The
	// domain layer's idempotency check covers crash-after-batch
	// (where we never updated the manifest); for the happy-path
	// retry we can short-circuit here without re-reading parts.
	if manifest.Phase == phaseDone {
		writeCompleteResultFromManifest(w, r, bucket, key, manifest)
		return
	}
	if manifest.Phase == phaseAborted {
		writeError(w, r, errNoSuchUpload, "upload was aborted", withBucket(bucket), withKey(key))
		return
	}
	// phaseInProgress and phaseCommitting both proceed. The latter
	// happens when an earlier Complete crashed between manifest.update
	// and the domain commit — the domain idempotency check decides
	// whether work needs to be redone or replayed.

	// Parse the request body XML.
	bodyRaw, readErr := io.ReadAll(verified.BodyReader)
	if readErr != nil {
		writeError(w, r, errIncompleteBody, "could not read request body", withBucket(bucket), withKey(key))
		return
	}
	var req completeMultipartUploadRequest
	if err := xml.Unmarshal(bodyRaw, &req); err != nil {
		writeError(w, r, errMalformedXML, fmt.Sprintf("body must be a CompleteMultipartUpload XML document: %s", err), withBucket(bucket), withKey(key))
		return
	}
	if len(req.Parts) == 0 {
		writeError(w, r, errMalformedXML, "CompleteMultipartUpload requires at least one part", withBucket(bucket), withKey(key))
		return
	}

	// Mark phase=committing FIRST — before validation and concat.
	// handleUploadPart rejects parts when phase != in_progress, so
	// flipping the phase here closes the race where a concurrent
	// (or retried) UploadPart could overwrite a part file between
	// our validation and the concat-stream open. Persisting this
	// before doing any expensive work also gives the recovery sweep
	// something to find if we crash mid-validate.
	if manifest.Phase == phaseInProgress {
		manifest.Phase = phaseCommitting
		if err := writeManifest(loc.StageDir, manifest); err != nil {
			writeError(w, r, errInternalError, "could not write committing manifest", withBucket(bucket), withKey(key))
			return
		}
	}

	// Validate the parts list against the manifest.
	validatedParts, validateErr := validateCompleteParts(req.Parts, manifest)
	if validateErr != nil {
		writeError(w, r, validateErr.Code, validateErr.Message, withBucket(bucket), withKey(key))
		return
	}

	// Concat parts → complete.tmp while computing the composite ETag.
	// This is the adapter-measured phase of the trace-substitute
	// histogram; the domain returns the other three (lock/hash/batch).
	completeTmp := filepath.Join(loc.StageDir, multipartCompleteTmp)
	concatStart := time.Now()
	composite, concatErr := concatPartsAndComputeCompositeETag(loc.StageDir, validatedParts, completeTmp)
	concatDur := time.Since(concatStart)
	if concatErr != nil {
		_ = os.Remove(completeTmp)
		writeError(w, r, errInternalError, fmt.Sprintf("could not assemble parts: %s", concatErr), withBucket(bucket), withKey(key))
		return
	}

	// Persist the composite ETag now so a crash before the durable
	// commit lets recovery verify (or surface) what would have been
	// installed.
	manifest.CompositeETag = composite
	if err := writeManifest(loc.StageDir, manifest); err != nil {
		_ = os.Remove(completeTmp)
		writeError(w, r, errInternalError, "could not write committing manifest", withBucket(bucket), withKey(key))
		return
	}

	// Build the S3 write options from the captured manifest fields.
	// Note: IfMatch / IfNoneMatchAny are NOT honored on Complete by
	// AWS — the domain layer ignores them on the multipart path.
	var userMeta []byte
	if len(manifest.UserMetadata) > 0 {
		blob, jerr := json.Marshal(manifest.UserMetadata)
		if jerr != nil {
			writeError(w, r, errInternalError, "could not encode user metadata", withBucket(bucket), withKey(key))
			return
		}
		userMeta = blob
	}
	opts := domain.S3WriteOptions{
		ContentType:        manifest.ContentType,
		ContentEncoding:    manifest.ContentEncoding,
		ContentDisposition: manifest.ContentDisposition,
		UserMetadata:       userMeta,
	}

	// Hand off to the domain layer. It owns the path-lock, the
	// rename, and the durable Pebble batch.
	result, domErr := rt.svc.CompleteMultipartUpload(domain.MultipartCompleteArgs{
		VirtualPath:   virtualPathFor(bucket, key),
		SrcPath:       completeTmp,
		UploadID:      uploadIDBytes,
		CompositeETag: composite,
		Opts:          opts,
	})
	if domErr != nil {
		// complete.tmp is left behind on failure — the recovery sweep
		// (or an Abort) will clean the staging dir. Don't remove it
		// inline: a crash between os.Remove and the manifest update
		// could lose data we'd otherwise recover.
		mapDomainError(w, r, domErr, bucket, key)
		return
	}

	// Observe the four Complete sub-phases. Skipped on the Replayed
	// fast path (no install happened, timings are zero). nil-safe.
	if !result.Replayed {
		rt.metrics.ObserveCompletePhase("concat", concatDur.Seconds())
		rt.metrics.ObserveCompletePhase("lock_wait", result.Timings.LockWait.Seconds())
		rt.metrics.ObserveCompletePhase("hash", result.Timings.Hash.Seconds())
		rt.metrics.ObserveCompletePhase("pebble_batch", result.Timings.PebbleBatch.Seconds())
	}

	// Mark manifest phase=done with the result snapshot. Best-effort
	// — the Pebble record is what matters for client semantics. A
	// failure to update the on-disk manifest after the durable
	// commit is logged but doesn't fail the response.
	manifest.Phase = phaseDone
	manifest.WholeBodyMD5 = ""
	if result.Meta != nil {
		manifest.WholeBodyMD5 = result.Meta.ETag
		manifest.CompletedFileID = result.Meta.ID.String()
	}
	manifest.CompletedAt = time.Now().UnixMilli()
	if err := writeManifest(loc.StageDir, manifest); err != nil {
		// Don't fail the request — Pebble already has the durable
		// record. Log and move on; recovery will reconcile.
		fmt.Printf("[filegate-s3] CompleteMultipartUpload: post-commit manifest write failed: %s\n", err)
	}

	// Drop the parts/ directory and the assembled complete.tmp now
	// that the durable commit has landed. The manifest stays so a
	// retried Complete (until retention sweeps the manifest) can
	// short-circuit via the phaseDone branch — the Pebble record is
	// authoritative; the manifest just lets ListMultipartUploads /
	// retry-Complete answer without a Pebble lookup.
	cleanupCompletedStaging(loc.StageDir)

	// Build the response.
	res := completeMultipartUploadResult{
		Location: locationFor(r, bucket, key),
		Bucket:   bucket,
		Key:      key,
		ETag:     quoteETag(composite),
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)

	if rt.accessLog {
		extra := fmt.Sprintf("uploadId=%s parts=%d etag=%s replayed=%t", uploadID, len(validatedParts), composite, result.Replayed)
		rt.logAccess("CompleteMultipartUpload", bucket, key, verified.AccessKeyID, extra)
	}
}

// validateCompleteParts checks the client-supplied parts list against
// the manifest. AWS rules (errors mirror real S3):
//
//   - Each PartNumber must exist in the manifest (InvalidPart).
//   - Per-part ETag must match the stored MD5 (InvalidPart).
//   - The list must be in strictly ascending PartNumber order
//     (InvalidPartOrder). Duplicates are also InvalidPartOrder.
//   - Every part except the LAST must be ≥ 5 MiB (EntityTooSmall).
//
// On success returns the list as resolved manifestParts in order.
func validateCompleteParts(reqParts []completeRequestPart, manifest *multipartManifest) ([]multipartPart, *sigV4VerifyError) {
	out := make([]multipartPart, 0, len(reqParts))
	prev := 0
	for _, rp := range reqParts {
		if rp.PartNumber < 1 || rp.PartNumber > multipartMaxPartCount {
			return nil, sigErr(errInvalidPart, "partNumber %d out of range 1-%d", rp.PartNumber, multipartMaxPartCount)
		}
		if rp.PartNumber <= prev {
			return nil, sigErr(errInvalidPartOrder, "parts must appear in strictly ascending PartNumber order")
		}
		prev = rp.PartNumber
		stored, ok := manifest.Parts[rp.PartNumber]
		if !ok {
			return nil, sigErr(errInvalidPart, "partNumber %d was not uploaded", rp.PartNumber)
		}
		// Strip surrounding quotes — clients quote ETags on the wire,
		// but rclone and a few SDKs send unquoted. Be liberal.
		clientETag := strings.Trim(strings.TrimSpace(rp.ETag), `"`)
		if !strings.EqualFold(clientETag, stored.ETag) {
			return nil, sigErr(errInvalidPart, "partNumber %d ETag does not match upload (expected %q, got %q)", rp.PartNumber, stored.ETag, clientETag)
		}
		out = append(out, stored)
	}
	// 5 MiB minimum on every part except the last. The last part can
	// be any size (including very small) — that's how S3 handles
	// small final chunks.
	for i := 0; i < len(out)-1; i++ {
		if out[i].Size < multipartMinPartSize {
			return nil, sigErr(errEntityTooSmall, "partNumber %d is %d bytes; minimum is %d on non-final parts", out[i].PartNumber, out[i].Size, multipartMinPartSize)
		}
	}
	return out, nil
}

// concatPartsAndComputeCompositeETag streams every part's bytes
// into completeTmp (atomically via tmp+rename) while accumulating
// the per-part MD5s into the composite digest:
//
//	composite = hex(MD5(md5_part1_bytes ‖ md5_part2_bytes ‖ ...)) + "-N"
//
// The per-part MD5 bytes are the RAW 16-byte digests, NOT the hex
// strings — this is what AWS specifies and what every S3 client
// computes locally to verify.
func concatPartsAndComputeCompositeETag(stageDir string, parts []multipartPart, completeTmp string) (string, error) {
	tmp := completeTmp + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("open complete.tmp: %w", err)
	}
	composite := md5.New()
	defer func() {
		if out != nil {
			_ = out.Close()
		}
		_ = os.Remove(tmp)
	}()

	// Sort defensively — validateCompleteParts already returns in
	// ascending order, but the cost is negligible.
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })

	for _, p := range parts {
		raw, decErr := hex.DecodeString(p.ETag)
		if decErr != nil || len(raw) != 16 {
			return "", fmt.Errorf("part %d has malformed stored ETag %q", p.PartNumber, p.ETag)
		}
		composite.Write(raw)

		partPath := partPathFor(stageDir, p.PartNumber)
		f, openErr := os.Open(partPath)
		if openErr != nil {
			return "", fmt.Errorf("open part %d: %w", p.PartNumber, openErr)
		}
		_, copyErr := io.Copy(out, f)
		_ = f.Close()
		if copyErr != nil {
			return "", fmt.Errorf("copy part %d: %w", p.PartNumber, copyErr)
		}
	}

	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("sync complete.tmp: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close complete.tmp: %w", err)
	}
	out = nil
	if err := os.Rename(tmp, completeTmp); err != nil {
		return "", fmt.Errorf("rename complete.tmp: %w", err)
	}

	digest := hex.EncodeToString(composite.Sum(nil))
	return fmt.Sprintf("%s-%d", digest, len(parts)), nil
}

// writeCompleteResultFromManifest is the phaseDone short-circuit.
// We can synthesize the response without touching the durable
// record because manifest fields (CompositeETag, CompletedFileID)
// were captured by the original Complete.
func writeCompleteResultFromManifest(w http.ResponseWriter, r *http.Request, bucket, key string, manifest *multipartManifest) {
	res := completeMultipartUploadResult{
		Location: locationFor(r, bucket, key),
		Bucket:   bucket,
		Key:      key,
		ETag:     quoteETag(manifest.CompositeETag),
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
}

// cleanupCompletedStaging removes the bulky staging artifacts of a
// finished multipart upload (parts/ and complete.tmp) while leaving
// the manifest in place. The manifest is small and serves the
// idempotent-retry short-circuit; the multipart cleanup loop deletes
// done manifests + the durable Pebble record after the retention
// window. Errors are logged but don't fail the request — the bytes
// are already durable in Pebble.
func cleanupCompletedStaging(stageDir string) {
	partsDir := filepath.Join(stageDir, multipartPartsDirName)
	if err := os.RemoveAll(partsDir); err != nil {
		fmt.Printf("[filegate-s3] CompleteMultipartUpload: remove parts dir %s: %s\n", partsDir, err)
	}
	completeTmp := filepath.Join(stageDir, multipartCompleteTmp)
	if err := os.Remove(completeTmp); err != nil && !os.IsNotExist(err) {
		fmt.Printf("[filegate-s3] CompleteMultipartUpload: remove complete.tmp %s: %s\n", completeTmp, err)
	}
}

// locationFor synthesizes the Location header value AWS clients
// echo back. It's best-effort — clients overwhelmingly read Bucket
// + Key and ignore Location, so a slightly off scheme doesn't break
// anything. We pick https when the request was TLS-terminated,
// otherwise http.
func locationFor(r *http.Request, bucket, key string) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "filegate"
	}
	return fmt.Sprintf("%s://%s/%s/%s", scheme, host, bucket, key)
}
