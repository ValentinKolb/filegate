package s3

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/valentinkolb/filegate/infra/filesystem"
)

// handleCreateMultipartUpload implements the POST /{bucket}/{key}?uploads
// flow. Generates a fresh uploadId, captures the per-PUT object
// metadata in the manifest, and returns the InitiateMultipartUpload
// XML response.
func (rt *router) handleCreateMultipartUpload(w http.ResponseWriter, r *http.Request, verified *sigV4Result, bucket, key string) {
	if err := validateObjectKey(key); err != nil {
		writeError(w, r, errInvalidArgument, err.Error(), withBucket(bucket), withKey(key))
		return
	}

	// Enforce the same 2 KiB user-metadata budget the single-PUT
	// path enforces. Without this, clients could smuggle arbitrarily
	// large x-amz-meta-* blobs through CreateMultipartUpload —
	// inconsistent with PutObject and an unbounded resource cost on
	// the manifest.
	userMeta := collectUserMetadata(r)
	if len(userMeta) > 0 {
		blob, jerr := json.Marshal(userMeta)
		if jerr != nil {
			writeError(w, r, errInvalidArgument, "could not encode x-amz-meta-* headers: "+jerr.Error(), withBucket(bucket), withKey(key))
			return
		}
		if len(blob) > userMetadataMaxBytes {
			writeError(w, r, errInvalidArgument, "x-amz-meta-* headers exceed 2 KiB user-metadata budget", withBucket(bucket), withKey(key))
			return
		}
	}

	uploadID, err := generateUploadID()
	if err != nil {
		writeError(w, r, errInternalError, "could not generate uploadId", withBucket(bucket), withKey(key))
		return
	}
	loc, err := rt.stageDirForBucket(bucket, uploadID)
	if err != nil {
		writeError(w, r, errNoSuchBucket, "bucket does not exist", withBucket(bucket), withKey(key))
		return
	}

	manifest := &multipartManifest{
		Format:             multipartManifestFormat,
		Kind:               multipartManifestKind,
		UploadID:           uploadID,
		Bucket:             bucket,
		Key:                key,
		Initiated:          time.Now().UnixMilli(),
		ContentType:        r.Header.Get("Content-Type"),
		ContentEncoding:    r.Header.Get("Content-Encoding"),
		ContentDisposition: r.Header.Get("Content-Disposition"),
		UserMetadata:       userMeta,
		Parts:              map[int]multipartPart{},
		Phase:              phaseInProgress,
	}
	if err := os.MkdirAll(filepath.Join(loc.StageDir, multipartPartsDirName), 0o755); err != nil {
		writeError(w, r, errInternalError, "could not prepare staging dir", withBucket(bucket), withKey(key))
		return
	}
	if err := writeManifest(loc.StageDir, manifest); err != nil {
		writeError(w, r, errInternalError, "could not write manifest", withBucket(bucket), withKey(key))
		return
	}

	res := initiateMultipartUploadResult{
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
	if rt.accessLog {
		rt.logAccess("CreateMultipartUpload", bucket, key, verified.AccessKeyID, "uploadId="+uploadID)
	}
}

// handleUploadPart implements PUT /{bucket}/{key}?partNumber=N&uploadId=X.
// Body is the raw part bytes; we tee through MD5 while writing,
// store the part-MD5 in the manifest, and return ETag.
//
// AWS spec rules:
//   - partNumber range 1-10000 (errInvalidArgument otherwise)
//   - duplicate UploadPart for the same partNumber overwrites
//   - 5 MiB minimum is enforced at Complete time, not here (since
//     UploadPart doesn't know yet whether this is the final part)
func (rt *router) handleUploadPart(w http.ResponseWriter, r *http.Request, verified *sigV4Result, bucket, key string) {
	q := r.URL.Query()
	partRaw := q.Get("partNumber")
	uploadID := q.Get("uploadId")
	if partRaw == "" || uploadID == "" {
		writeError(w, r, errInvalidArgument, "partNumber and uploadId required", withBucket(bucket), withKey(key))
		return
	}
	partNumber, err := strconv.Atoi(partRaw)
	if err != nil || partNumber < 1 || partNumber > multipartMaxPartCount {
		writeError(w, r, errInvalidArgument, fmt.Sprintf("partNumber must be 1-%d", multipartMaxPartCount), withBucket(bucket), withKey(key))
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
	if manifest.Phase != phaseInProgress {
		writeError(w, r, errInvalidRequest, "upload is not in_progress", withBucket(bucket), withKey(key))
		return
	}
	if manifest.Bucket != bucket || manifest.Key != key {
		// Defense against client confusion or replay across keys.
		writeError(w, r, errInvalidArgument, "uploadId belongs to a different bucket/key", withBucket(bucket), withKey(key))
		return
	}

	// Stream body to part file while computing MD5.
	partPath := partPathFor(loc.StageDir, partNumber)
	written, partETag, sigErrV := writePartTeeMD5(verified.BodyReader, partPath)
	if sigErrV != nil {
		writeError(w, r, sigErrV.Code, sigErrV.Message, withBucket(bucket), withKey(key))
		return
	}

	// Optional Content-MD5 verification.
	if v := strings.TrimSpace(r.Header.Get("Content-MD5")); v != "" {
		raw, decErr := base64.StdEncoding.DecodeString(v)
		if decErr != nil || len(raw) != 16 {
			_ = os.Remove(partPath)
			writeError(w, r, errInvalidDigest, "Content-MD5 must be a 16-byte base64-encoded value", withBucket(bucket), withKey(key))
			return
		}
		if hex.EncodeToString(raw) != partETag {
			_ = os.Remove(partPath)
			writeError(w, r, errBadDigest, "Content-MD5 does not match part body", withBucket(bucket), withKey(key))
			return
		}
	}

	// Update manifest: overwrite if duplicate.
	manifest.Parts[partNumber] = multipartPart{
		PartNumber: partNumber,
		Size:       written,
		ETag:       partETag,
		UpdatedAt:  time.Now().UnixMilli(),
	}
	if err := writeManifest(loc.StageDir, manifest); err != nil {
		writeError(w, r, errInternalError, "could not update manifest", withBucket(bucket), withKey(key))
		return
	}

	w.Header().Set("ETag", quoteETag(partETag))
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	if rt.accessLog {
		rt.logAccess("UploadPart", bucket, key, verified.AccessKeyID, fmt.Sprintf("uploadId=%s part=%d size=%d", uploadID, partNumber, written))
	}
}

// handleAbortMultipartUpload deletes the staging dir for the given
// uploadId. Idempotent: a missing upload returns 204.
func (rt *router) handleAbortMultipartUpload(w http.ResponseWriter, r *http.Request, verified *sigV4Result, bucket, key string) {
	uploadID := r.URL.Query().Get("uploadId")
	if uploadID == "" {
		writeError(w, r, errInvalidArgument, "uploadId required", withBucket(bucket), withKey(key))
		return
	}
	loc, err := rt.findStageDir(uploadID)
	if err != nil {
		// Idempotent: already gone.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := os.RemoveAll(loc.StageDir); err != nil {
		writeError(w, r, errInternalError, "could not remove staging dir", withBucket(bucket), withKey(key))
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if rt.accessLog {
		rt.logAccess("AbortMultipartUpload", bucket, key, verified.AccessKeyID, "uploadId="+uploadID)
	}
}

// handleListParts implements GET /{bucket}/{key}?uploadId=X.
// Returns the parts of an in-progress upload, sorted by partNumber.
func (rt *router) handleListParts(w http.ResponseWriter, r *http.Request, verified *sigV4Result, bucket, key string) {
	uploadID := r.URL.Query().Get("uploadId")
	if uploadID == "" {
		writeError(w, r, errInvalidArgument, "uploadId required", withBucket(bucket), withKey(key))
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

	parts := make([]int, 0, len(manifest.Parts))
	for n := range manifest.Parts {
		parts = append(parts, n)
	}
	sort.Ints(parts)
	res := listPartsResult{
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
	}
	for _, n := range parts {
		p := manifest.Parts[n]
		res.Parts = append(res.Parts, partXML{
			PartNumber:   p.PartNumber,
			LastModified: time.UnixMilli(p.UpdatedAt).UTC().Format("2006-01-02T15:04:05.000Z"),
			ETag:         quoteETag(p.ETag),
			Size:         p.Size,
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
	if rt.accessLog {
		rt.logAccess("ListParts", bucket, key, verified.AccessKeyID, fmt.Sprintf("uploadId=%s parts=%d", uploadID, len(parts)))
	}
}

// handleListMultipartUploads implements GET /{bucket}?uploads.
// Returns in-progress (and committing) uploads in the bucket.
func (rt *router) handleListMultipartUploads(w http.ResponseWriter, r *http.Request, verified *sigV4Result, bucket string) {
	loc, err := rt.stageDirForBucket(bucket, "" /* uploadId not relevant here */)
	if err != nil {
		writeError(w, r, errNoSuchBucket, "bucket does not exist", withBucket(bucket))
		return
	}
	manifests, err := listMultipartUploadsForBucket(loc.MountAbs)
	if err != nil {
		writeError(w, r, errInternalError, fmt.Sprintf("listing failed: %s", err), withBucket(bucket))
		return
	}
	res := listMultipartUploadsResult{
		Bucket: bucket,
	}
	for _, m := range manifests {
		res.Uploads = append(res.Uploads, uploadXML{
			Key:       m.Key,
			UploadID:  m.UploadID,
			Initiated: time.UnixMilli(m.Initiated).UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
	if rt.accessLog {
		rt.logAccess("ListMultipartUploads", bucket, "", verified.AccessKeyID, fmt.Sprintf("count=%d", len(manifests)))
	}
}

// generateUploadID returns a fresh 32-hex-char uploadId. We use 16
// bytes of crypto/rand which is plenty for collision avoidance
// (2^128 keyspace).
func generateUploadID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// writePartTeeMD5 writes body bytes to partPath (atomically via
// tmp+rename) while teeing through MD5. Returns (size, hex-md5,
// nil) on success.
func writePartTeeMD5(body io.Reader, partPath string) (int64, string, *sigV4VerifyError) {
	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		return 0, "", sigErr(errInternalError, "could not prepare parts dir: %s", err)
	}
	tmp := partPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, "", sigErr(errInternalError, "could not open part file: %s", err)
	}
	hasher := md5.New()
	tee := io.MultiWriter(f, hasher)
	written, err := io.Copy(tee, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, "", sigErr(errIncompleteBody, "part write: %s", err)
	}
	// fsync the data file so the rename'd part is durable.
	if err := filesystem.SyncDir(filepath.Dir(tmp)); err == nil {
		// ignore — best-effort dir-sync
	}
	if err := os.Rename(tmp, partPath); err != nil {
		_ = os.Remove(tmp)
		return 0, "", sigErr(errInternalError, "rename part: %s", err)
	}
	return written, hex.EncodeToString(hasher.Sum(nil)), nil
}

// collectUserMetadata extracts x-amz-meta-* headers from the
// request into a flat map. Same shape as buildS3WriteOptions but
// returns the map directly; we serialize to JSON only at Complete
// time (push 3) when we know the final entity slot.
func collectUserMetadata(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "x-amz-meta-") {
			continue
		}
		name := strings.TrimPrefix(lk, "x-amz-meta-")
		if name == "" {
			continue
		}
		out[name] = strings.Join(v, ",")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
