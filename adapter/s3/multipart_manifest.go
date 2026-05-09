package s3

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/valentinkolb/filegate/infra/filesystem"
)

// Multipart upload manifest layout.
//
// On disk under each mount we use the existing .fg-uploads staging
// dir; multipart uploads live under their own subprefix so the
// existing chunked-upload cleanup loop and the new multipart
// cleanup loop don't trip over each other:
//
//   <mountAbs>/.fg-uploads/s3-<uploadId>/manifest.json
//   <mountAbs>/.fg-uploads/s3-<uploadId>/parts/00001.bin
//   <mountAbs>/.fg-uploads/s3-<uploadId>/parts/00002.bin
//   ...
//   <mountAbs>/.fg-uploads/s3-<uploadId>/complete.tmp   (only during Complete)
//
// uploadId is 32 lowercase-hex chars (16 random bytes). We pick our
// own format rather than emit AWS-shape uploadIds (long opaque
// base64) because clients treat them as opaque anyway and this
// keeps the on-disk dirname easy to grep/inspect.
const (
	multipartDirPrefix      = "s3-"
	multipartManifestFile   = "manifest.json"
	multipartPartsDirName   = "parts"
	multipartCompleteTmp    = "complete.tmp"
	multipartManifestKind   = "s3-multipart"
	multipartManifestFormat = 1

	// AWS S3 limits.
	multipartMinPartSize  = 5 * 1024 * 1024 // 5 MiB on non-final parts
	multipartMaxPartCount = 10000
)

// Multipart upload phases. The set is ordered by progression: a
// crash mid-upload is recoverable as long as the manifest tells us
// where we were.
type multipartPhase string

const (
	phaseInProgress multipartPhase = "in_progress" // accepting UploadPart
	phaseCommitting multipartPhase = "committing"  // Complete in flight; recovery needs work
	phaseDone       multipartPhase = "done"        // Complete succeeded; manifest kept for retention
	phaseAborted    multipartPhase = "aborted"     // AbortMultipartUpload called (or recovery decided abort)
)

// multipartManifest is what we serialize to manifest.json. Format
// version is bumped if the schema changes incompatibly.
type multipartManifest struct {
	Format    int            `json:"format"`
	Kind      string         `json:"kind"` // "s3-multipart" — distinguishes from chunked-upload manifests
	UploadID  string         `json:"upload_id"`
	Bucket    string         `json:"bucket"`
	Key       string         `json:"key"`
	Initiated int64          `json:"initiated_unix_ms"`

	// Per-PUT user-supplied object metadata that would normally go
	// on the resulting object. Captured at CreateMultipartUpload
	// time and applied by Complete.
	ContentType        string            `json:"content_type,omitempty"`
	ContentEncoding    string            `json:"content_encoding,omitempty"`
	ContentDisposition string            `json:"content_disposition,omitempty"`
	UserMetadata       map[string]string `json:"user_metadata,omitempty"`

	// Per-part state. Keyed by partNumber. Entry exists once the
	// part has been UploadPart'd.
	Parts map[int]multipartPart `json:"parts"`

	Phase multipartPhase `json:"phase"`

	// Push 3 fields — the 2-phase Complete protocol fills these
	// in to support crash recovery. Empty on a fresh upload.
	CompositeETag    string `json:"composite_etag,omitempty"`
	WholeBodyMD5     string `json:"whole_body_md5,omitempty"`
	PreInstallExists bool   `json:"pre_install_exists,omitempty"`
	PreInstallRaw   []byte  `json:"pre_install_raw,omitempty"` // base64 bytes of pre-install fgbin entity record
	CompletedFileID string  `json:"completed_file_id,omitempty"`
	CompletedAt     int64   `json:"completed_at_unix_ms,omitempty"`
}

// multipartPart is one part's recorded state.
type multipartPart struct {
	PartNumber int    `json:"part_number"`
	Size       int64  `json:"size"`
	ETag       string `json:"etag"`           // hex MD5 of part bytes
	UpdatedAt  int64  `json:"updated_unix_ms"`
}

// multipartLocator names where a given upload's staging dir lives.
type multipartLocator struct {
	MountAbs string // absolute path of the bucket's mount root
	StageDir string // <mountAbs>/.fg-uploads/s3-<uploadId>
	UploadID string
}

// stageDirFor returns the staging directory path for a given mount
// + upload ID. Caller is responsible for ensuring the mount path is
// the real one (we don't validate here).
func stageDirFor(mountAbs, uploadID string) string {
	return filepath.Join(mountAbs, ".fg-uploads", multipartDirPrefix+uploadID)
}

// manifestPathFor returns the manifest.json path for a given
// staging dir.
func manifestPathFor(stageDir string) string {
	return filepath.Join(stageDir, multipartManifestFile)
}

// partPathFor returns the per-part file path for partNumber N.
// Padded so directory listing sorts correctly.
func partPathFor(stageDir string, partNumber int) string {
	return filepath.Join(stageDir, multipartPartsDirName, fmt.Sprintf("%05d.bin", partNumber))
}

// readManifest loads + decodes the manifest at the staging dir.
// Returns ErrNotFound when the dir or manifest is gone (the caller
// surfaces NoSuchUpload to the client).
func readManifest(stageDir string) (*multipartManifest, error) {
	raw, err := os.ReadFile(manifestPathFor(stageDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errMultipartNotFound
		}
		return nil, err
	}
	var m multipartManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest decode: %w", err)
	}
	if m.Kind != multipartManifestKind {
		// Foreign manifest in our subprefix — defensive only;
		// shouldn't happen since we use a unique prefix.
		return nil, fmt.Errorf("manifest kind %q is not %q", m.Kind, multipartManifestKind)
	}
	return &m, nil
}

// writeManifest atomically replaces the manifest at the staging
// dir. Uses the shared infra/filesystem WriteFileAtomic helper so
// crash semantics match the rest of filegate's small-blob writes.
func writeManifest(stageDir string, m *multipartManifest) error {
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return filesystem.WriteFileAtomic(manifestPathFor(stageDir), raw)
}

// errMultipartNotFound is returned when an uploadId doesn't have a
// staging dir / manifest. The router maps this to NoSuchUpload.
var errMultipartNotFound = errors.New("s3: multipart upload not found")

// findStageDir walks every mount looking for the staging dir for
// uploadId. Returns ErrNotFound when no mount has it. Used to
// resolve uploadId to a (mountAbs, stageDir) pair without the
// adapter needing to know which mount the upload was created on.
func (rt *router) findStageDir(uploadID string) (multipartLocator, error) {
	if uploadID == "" {
		return multipartLocator{}, errMultipartNotFound
	}
	for _, root := range rt.svc.ListRoot() {
		mountAbs, err := rt.svc.ResolveAbsPath(root.ID)
		if err != nil {
			continue
		}
		dir := stageDirFor(mountAbs, uploadID)
		if _, err := os.Stat(dir); err == nil {
			return multipartLocator{MountAbs: mountAbs, StageDir: dir, UploadID: uploadID}, nil
		}
	}
	return multipartLocator{}, errMultipartNotFound
}

// stageDirForBucket: the inverse — given a bucket name, find its
// mount-abs and synthesize the staging dir for a NEW uploadId.
// Used by CreateMultipartUpload.
func (rt *router) stageDirForBucket(bucket, uploadID string) (multipartLocator, error) {
	for _, root := range rt.svc.ListRoot() {
		if root.Name != bucket {
			continue
		}
		mountAbs, err := rt.svc.ResolveAbsPath(root.ID)
		if err != nil {
			return multipartLocator{}, err
		}
		return multipartLocator{
			MountAbs: mountAbs,
			StageDir: stageDirFor(mountAbs, uploadID),
			UploadID: uploadID,
		}, nil
	}
	return multipartLocator{}, errMultipartNotFound
}

// listMultipartUploadsForBucket scans the .fg-uploads/s3-* dirs in
// a bucket and returns their manifests sorted by Initiated time
// (oldest first, matching AWS behaviour). Manifests in phase=done
// or phase=aborted are skipped — the cleanup loop GC's them on a
// retention timer; clients shouldn't see them as in-progress.
func listMultipartUploadsForBucket(mountAbs string) ([]multipartManifest, error) {
	stageRoot := filepath.Join(mountAbs, ".fg-uploads")
	entries, err := os.ReadDir(stageRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []multipartManifest
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), multipartDirPrefix) {
			continue
		}
		m, err := readManifest(filepath.Join(stageRoot, e.Name()))
		if err != nil {
			continue
		}
		if m.Phase != phaseInProgress && m.Phase != phaseCommitting {
			continue
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Initiated < out[j].Initiated })
	return out, nil
}

// secondsAgo returns time.Duration since the given unix ms. Returns
// the zero duration if t is in the future or zero.
func secondsAgo(unixMs int64) time.Duration {
	if unixMs == 0 {
		return 0
	}
	t := time.UnixMilli(unixMs)
	if t.After(time.Now()) {
		return 0
	}
	return time.Since(t)
}
