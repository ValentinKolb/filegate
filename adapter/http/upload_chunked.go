package httpadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/puzpuzpuz/xsync/v4"

	apiv1 "github.com/valentinkolb/filegate/api/v1"
	"github.com/valentinkolb/filegate/domain"
)

var checksumRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var uploadIDRE = regexp.MustCompile(`^[a-f0-9]{16}$`)

const uploadManifestVersion = 2
const uploadStagingDirName = ".fg-uploads"
const uploadManifestFileName = "manifest.json"

type chunkedUploadMeta struct {
	Version           int                 `json:"version"`
	UploadID          string              `json:"uploadId"`
	ParentID          domain.FileID       `json:"parentId"`
	Filename          string              `json:"filename"`
	Size              int64               `json:"size"`
	Checksum          string              `json:"checksum"`
	ChunkSize         int64               `json:"chunkSize"`
	TotalChunks       int                 `json:"totalChunks"`
	Ownership         *domain.Ownership   `json:"ownership,omitempty"`
	OnConflict        domain.ConflictMode `json:"onConflict,omitempty"`
	StageDir          string              `json:"stageDir"`
	PartPath          string              `json:"partPath"`
	UploadedBits      []byte              `json:"uploadedBits,omitempty"`
	UploadedCnt       int                 `json:"uploadedCnt"`
	Finalizing        bool                `json:"finalizing,omitempty"`
	Completed         bool                `json:"completed,omitempty"`
	CompletedChecksum string              `json:"completedChecksum,omitempty"`
	CompletedNodeID   string              `json:"completedNodeId,omitempty"`
	CreatedAt         int64               `json:"createdAt"`
	UpdatedAt         int64               `json:"updatedAt"`

	// v1 legacy compatibility (best effort during transition).
	Uploaded map[int]bool `json:"uploaded,omitempty"`
}

type chunkedManager struct {
	svc *domain.Service

	expiry                time.Duration
	cleanupInterval       time.Duration
	maxChunkBytes         int64
	maxUploadBytes        int64
	maxChunkedUploadBytes int64
	minFreeBytes          int64

	locks      *xsync.Map[string, *sync.Mutex]
	chunkLocks *xsync.Map[string, *sync.Mutex]
	uploadDirs *xsync.Map[string, string]
	chunkSlots chan struct{}

	cleanupStop chan struct{}
	cleanupDone chan struct{}
	cleanupOnce sync.Once
}

func newChunkedManager(
	svc *domain.Service,
	expiry, cleanupInterval time.Duration,
	maxChunkBytes, maxUploadBytes, maxChunkedUploadBytes int64,
	maxConcurrentChunkWrites int,
	minFreeBytes int64,
) *chunkedManager {
	if maxChunkedUploadBytes <= 0 {
		maxChunkedUploadBytes = maxUploadBytes
	}
	if maxChunkedUploadBytes <= 0 {
		maxChunkedUploadBytes = int64(50 * 1024 * 1024 * 1024)
	}
	if maxConcurrentChunkWrites <= 0 {
		maxConcurrentChunkWrites = runtime.NumCPU() * 8
		if maxConcurrentChunkWrites < 32 {
			maxConcurrentChunkWrites = 32
		}
		if maxConcurrentChunkWrites > 512 {
			maxConcurrentChunkWrites = 512
		}
	}
	if minFreeBytes < 0 {
		minFreeBytes = 0
	}
	m := &chunkedManager{
		svc:                   svc,
		expiry:                expiry,
		cleanupInterval:       cleanupInterval,
		maxChunkBytes:         maxChunkBytes,
		maxUploadBytes:        maxUploadBytes,
		maxChunkedUploadBytes: maxChunkedUploadBytes,
		minFreeBytes:          minFreeBytes,
		locks:                 xsync.NewMap[string, *sync.Mutex](),
		chunkLocks:            xsync.NewMap[string, *sync.Mutex](),
		uploadDirs:            xsync.NewMap[string, string](),
		chunkSlots:            make(chan struct{}, maxConcurrentChunkWrites),
		cleanupStop:           make(chan struct{}),
		cleanupDone:           make(chan struct{}),
	}
	if cleanupInterval > 0 {
		go m.cleanupLoop()
	} else {
		close(m.cleanupDone)
	}
	return m
}

func (m *chunkedManager) acquireChunkSlot(ctx context.Context) error {
	if m == nil || m.chunkSlots == nil {
		return nil
	}
	select {
	case m.chunkSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *chunkedManager) releaseChunkSlot() {
	if m == nil || m.chunkSlots == nil {
		return
	}
	select {
	case <-m.chunkSlots:
	default:
	}
}

func freeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func (m *chunkedManager) ensureSpaceForUpload(stageRoot string, bytesNeeded int64) error {
	if bytesNeeded <= 0 {
		return nil
	}
	free, err := freeBytes(stageRoot)
	if err != nil {
		return err
	}
	needed := uint64(bytesNeeded)
	if m.minFreeBytes > 0 {
		needed += uint64(m.minFreeBytes)
	}
	if free < needed {
		return domain.ErrInsufficientStorage
	}
	return nil
}

func (m *chunkedManager) cleanupLoop() {
	defer close(m.cleanupDone)
	ticker := time.NewTicker(m.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = m.cleanupExpired()
		case <-m.cleanupStop:
			return
		}
	}
}

func (m *chunkedManager) Close() {
	if m == nil {
		return
	}
	m.cleanupOnce.Do(func() {
		close(m.cleanupStop)
		<-m.cleanupDone
	})
}

func (m *chunkedManager) lock(uploadID string) *sync.Mutex {
	if l, ok := m.locks.Load(uploadID); ok {
		return l
	}
	l := &sync.Mutex{}
	actual, _ := m.locks.LoadOrStore(uploadID, l)
	return actual
}

func (m *chunkedManager) chunkLock(uploadID string, index int) *sync.Mutex {
	key := uploadID + ":" + strconv.Itoa(index)
	if l, ok := m.chunkLocks.Load(key); ok {
		return l
	}
	l := &sync.Mutex{}
	actual, _ := m.chunkLocks.LoadOrStore(key, l)
	return actual
}

func (m *chunkedManager) clearChunkLocks(uploadID string) {
	prefix := uploadID + ":"
	m.chunkLocks.Range(func(key string, _ *sync.Mutex) bool {
		if strings.HasPrefix(key, prefix) {
			m.chunkLocks.Delete(key)
		}
		return true
	})
}

func deterministicUploadID(parentID domain.FileID, filename, checksum string) string {
	sum := sha256.Sum256([]byte(parentID.String() + ":" + filename + ":" + checksum))
	return hex.EncodeToString(sum[:8])
}

func bitsetLen(totalChunks int) int {
	if totalChunks <= 0 {
		return 0
	}
	return (totalChunks + 7) / 8
}

func ensureBitsetSize(bits []byte, totalChunks int) []byte {
	want := bitsetLen(totalChunks)
	if want == 0 {
		return nil
	}
	if len(bits) >= want {
		return bits[:want]
	}
	out := make([]byte, want)
	copy(out, bits)
	return out
}

func hasBit(bits []byte, idx int) bool {
	if idx < 0 {
		return false
	}
	byteIdx := idx / 8
	if byteIdx >= len(bits) {
		return false
	}
	mask := byte(1 << (idx % 8))
	return bits[byteIdx]&mask != 0
}

func setBit(bits []byte, idx int) bool {
	if idx < 0 {
		return false
	}
	byteIdx := idx / 8
	if byteIdx >= len(bits) {
		return false
	}
	mask := byte(1 << (idx % 8))
	if bits[byteIdx]&mask != 0 {
		return false
	}
	bits[byteIdx] |= mask
	return true
}

func uploadedChunkList(meta *chunkedUploadMeta) []int {
	if meta == nil || meta.TotalChunks <= 0 {
		return nil
	}
	out := make([]int, 0, meta.UploadedCnt)
	for i := 0; i < meta.TotalChunks; i++ {
		if hasBit(meta.UploadedBits, i) {
			out = append(out, i)
		}
	}
	sort.Ints(out)
	return out
}

func (m *chunkedManager) mountRoots() []string {
	mounts := m.svc.ListRoot()
	roots := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		abs, err := m.svc.ResolveAbsPath(mount.ID)
		if err != nil {
			continue
		}
		roots = append(roots, abs)
	}
	return roots
}

func (m *chunkedManager) stagingRootForParent(parentID domain.FileID) (string, error) {
	parentAbs, err := m.svc.ResolveAbsPath(parentID)
	if err != nil {
		return "", err
	}
	roots := m.mountRoots()
	best := ""
	for _, root := range roots {
		if parentAbs == root || strings.HasPrefix(parentAbs, root+string(os.PathSeparator)) {
			if len(root) > len(best) {
				best = root
			}
		}
	}
	if best == "" {
		return "", domain.ErrNotFound
	}
	return filepath.Join(best, uploadStagingDirName), nil
}

func uploadDir(stageRoot, uploadID string) string {
	return filepath.Join(stageRoot, uploadID)
}

func manifestPath(uploadDir string) string {
	return filepath.Join(uploadDir, uploadManifestFileName)
}

func (m *chunkedManager) findUploadDir(uploadID string) (string, error) {
	if d, ok := m.uploadDirs.Load(uploadID); ok {
		if _, err := os.Stat(manifestPath(d)); err == nil {
			return d, nil
		}
		m.uploadDirs.Delete(uploadID)
	}

	for _, root := range m.mountRoots() {
		d := uploadDir(filepath.Join(root, uploadStagingDirName), uploadID)
		if _, err := os.Stat(manifestPath(d)); err == nil {
			m.uploadDirs.Store(uploadID, d)
			return d, nil
		}
	}
	return "", domain.ErrNotFound
}

func (m *chunkedManager) readMeta(uploadID string) (*chunkedUploadMeta, error) {
	uploadDir, err := m.findUploadDir(uploadID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(manifestPath(uploadDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var meta chunkedUploadMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.Version == 0 {
		meta.Version = 1
	}
	if meta.UploadID == "" {
		meta.UploadID = uploadID
	}
	if meta.StageDir == "" {
		meta.StageDir = uploadDir
	}
	if meta.PartPath == "" {
		meta.PartPath = filepath.Join(meta.StageDir, "data.part")
	}
	meta.UploadedBits = ensureBitsetSize(meta.UploadedBits, meta.TotalChunks)
	if len(meta.UploadedBits) == 0 && len(meta.Uploaded) > 0 {
		meta.UploadedBits = ensureBitsetSize(nil, meta.TotalChunks)
		meta.UploadedCnt = 0
		for idx, done := range meta.Uploaded {
			if done && setBit(meta.UploadedBits, idx) {
				meta.UploadedCnt++
			}
		}
		meta.Uploaded = nil
	}
	if meta.UpdatedAt == 0 {
		meta.UpdatedAt = meta.CreatedAt
	}
	m.uploadDirs.Store(meta.UploadID, meta.StageDir)
	return &meta, nil
}

func writeFileAtomic(path string, payload []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (m *chunkedManager) writeMeta(meta *chunkedUploadMeta) error {
	if err := os.MkdirAll(meta.StageDir, 0o755); err != nil {
		return err
	}
	meta.Version = uploadManifestVersion
	meta.UploadedBits = ensureBitsetSize(meta.UploadedBits, meta.TotalChunks)
	meta.Uploaded = nil
	payload, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(manifestPath(meta.StageDir), payload); err != nil {
		return err
	}
	m.uploadDirs.Store(meta.UploadID, meta.StageDir)
	return nil
}

func chunkExpectedSize(meta *chunkedUploadMeta, chunkIdx int) (int64, error) {
	if chunkIdx < 0 || chunkIdx >= meta.TotalChunks {
		return 0, domain.ErrInvalidArgument
	}
	if chunkIdx < meta.TotalChunks-1 {
		return meta.ChunkSize, nil
	}
	last := meta.Size - (int64(meta.TotalChunks-1) * meta.ChunkSize)
	if last <= 0 {
		return 0, domain.ErrInvalidArgument
	}
	return last, nil
}

func writeChunkAtPath(partPath string, offset, expectedSize, maxChunkBytes int64, src io.Reader) (string, error) {
	if expectedSize <= 0 || expectedSize > maxChunkBytes {
		return "", domain.ErrInvalidArgument
	}
	f, err := os.OpenFile(partPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	r := io.LimitReader(src, expectedSize+1)
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	written := int64(0)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, err := h.Write(chunk); err != nil {
				return "", err
			}
			if written+int64(n) > expectedSize {
				return "", fmt.Errorf("chunk too large")
			}
			if _, err := f.WriteAt(chunk, offset+written); err != nil {
				return "", err
			}
			written += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if written != expectedSize {
		return "", fmt.Errorf("chunk size mismatch")
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func hashChunkFromReader(expectedSize, maxChunkBytes int64, src io.Reader) (string, error) {
	if expectedSize <= 0 || expectedSize > maxChunkBytes {
		return "", domain.ErrInvalidArgument
	}
	h := sha256.New()
	r := io.LimitReader(src, expectedSize+1)
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	readN := int64(0)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, err := h.Write(chunk); err != nil {
				return "", err
			}
			readN += int64(n)
			if readN > expectedSize {
				return "", fmt.Errorf("chunk too large")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if readN != expectedSize {
		return "", fmt.Errorf("chunk size mismatch")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func hashFileRange(path string, offset, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sr := io.NewSectionReader(f, offset, size)
	h := sha256.New()
	if err := hashSection(h, sr, size); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func hashWholeFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	_, copyErr := io.CopyBuffer(h, f, buf)
	copyBufPool.Put(bufPtr)
	if copyErr != nil {
		return "", 0, copyErr
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

func hashSection(h hash.Hash, r io.Reader, expected int64) error {
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, wErr := h.Write(buf[:n]); wErr != nil {
				return wErr
			}
			total += int64(n)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if total != expected {
		return fmt.Errorf("short read")
	}
	return nil
}

func nodeFromMeta(m *chunkedUploadMeta, svc *domain.Service) (*apiv1.NodeWithChecksum, error) {
	if strings.TrimSpace(m.CompletedNodeID) == "" {
		return nil, domain.ErrNotFound
	}
	id, err := domain.ParseFileID(m.CompletedNodeID)
	if err != nil {
		return nil, err
	}
	meta, err := svc.GetFile(id)
	if err != nil {
		return nil, err
	}
	node := nodeResponse(meta)
	out := &apiv1.NodeWithChecksum{Node: node, Checksum: m.CompletedChecksum}
	return out, nil
}

func (m *chunkedManager) finalize(meta *chunkedUploadMeta) (*apiv1.NodeWithChecksum, error) {
	if meta.Completed {
		return nodeFromMeta(meta, m.svc)
	}
	meta.Finalizing = true
	meta.UpdatedAt = time.Now().UnixMilli()
	if err := m.writeMeta(meta); err != nil {
		return nil, err
	}

	actual, size, err := hashWholeFile(meta.PartPath)
	if err != nil {
		meta.Finalizing = false
		if wErr := m.writeMeta(meta); wErr != nil {
			log.Printf("[filegate] warning: failed to reset finalizing state for upload %s: %v", meta.UploadID, wErr)
		}
		return nil, err
	}
	if size != meta.Size {
		meta.Finalizing = false
		if wErr := m.writeMeta(meta); wErr != nil {
			log.Printf("[filegate] warning: failed to reset finalizing state for upload %s: %v", meta.UploadID, wErr)
		}
		return nil, fmt.Errorf("assembled size mismatch")
	}
	if actual != meta.Checksum {
		meta.Finalizing = false
		if wErr := m.writeMeta(meta); wErr != nil {
			log.Printf("[filegate] warning: failed to reset finalizing state for upload %s: %v", meta.UploadID, wErr)
		}
		return nil, fmt.Errorf("checksum mismatch")
	}

	finalizeMode := meta.OnConflict
	if finalizeMode == "" {
		finalizeMode = domain.ConflictError
	}
	fileMeta, err := m.svc.ReplaceFile(meta.ParentID, meta.Filename, meta.PartPath, meta.Ownership, finalizeMode)
	if err != nil {
		meta.Finalizing = false
		if wErr := m.writeMeta(meta); wErr != nil {
			log.Printf("[filegate] warning: failed to reset finalizing state for upload %s: %v", meta.UploadID, wErr)
		}
		return nil, err
	}

	meta.Finalizing = false
	meta.Completed = true
	meta.CompletedChecksum = actual
	meta.CompletedNodeID = fileMeta.ID.String()
	meta.UpdatedAt = time.Now().UnixMilli()
	if err := m.writeMeta(meta); err != nil {
		return nil, err
	}
	m.clearChunkLocks(meta.UploadID)
	node := nodeResponse(fileMeta)
	return &apiv1.NodeWithChecksum{Node: node, Checksum: actual}, nil
}

func (m *chunkedManager) handleStart(w http.ResponseWriter, r *http.Request) {
	var body apiv1.ChunkedStartRequest
	if ok := decodeJSONBody(w, r, &body); !ok {
		return
	}

	id, err := domain.ParseFileID(body.ParentID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid parentId")
		return
	}
	if strings.TrimSpace(body.Filename) == "" || strings.Contains(body.Filename, "/") {
		writeErr(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if body.Size <= 0 || body.Size > m.maxChunkedUploadBytes {
		writeErr(w, http.StatusBadRequest, "invalid size")
		return
	}
	if body.ChunkSize <= 0 || body.ChunkSize > m.maxChunkBytes {
		writeErr(w, http.StatusBadRequest, "invalid chunkSize")
		return
	}
	if !checksumRE.MatchString(body.Checksum) {
		writeErr(w, http.StatusBadRequest, "invalid checksum")
		return
	}

	parentMeta, err := m.svc.GetFile(id)
	if err != nil {
		statusFromErr(w, err)
		return
	}
	if parentMeta.Type != "directory" {
		writeErr(w, http.StatusBadRequest, "parent is not a directory")
		return
	}

	mode, err := domain.ParseConflictMode(body.OnConflict, domain.FileConflictModes)
	if err != nil {
		statusFromErr(w, err)
		return
	}

	// Optimistic conflict check: if the target name already exists and the
	// mode is "error", reject before the client wastes bandwidth on chunks.
	// "rename" deliberately skips this check at start because the unique
	// name is computed at finalize against the live filesystem state to
	// avoid reserving a name that may then collide.
	if mode == domain.ConflictError {
		if existingID, existingPath, hit := lookupChildOfParent(m.svc, parentMeta, body.Filename); hit {
			writeConflict(w, "filename already exists in parent", existingID, existingPath)
			return
		}
	}

	uploadID := deterministicUploadID(id, body.Filename, body.Checksum)
	lock := m.lock(uploadID)
	lock.Lock()
	defer lock.Unlock()

	meta, err := m.readMeta(uploadID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, "failed to load upload")
		return
	}

	if meta == nil {
		total := int((body.Size + body.ChunkSize - 1) / body.ChunkSize)
		stageRoot, err := m.stagingRootForParent(id)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		if err := os.MkdirAll(stageRoot, 0o755); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to prepare upload root")
			return
		}
		if err := m.ensureSpaceForUpload(stageRoot, body.Size); err != nil {
			statusFromErr(w, err)
			return
		}
		stageDir := uploadDir(stageRoot, uploadID)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to create upload dir")
			return
		}
		partPath := filepath.Join(stageDir, "data.part")
		f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to create upload file")
			return
		}
		if err := f.Truncate(body.Size); err != nil {
			_ = f.Close()
			writeErr(w, http.StatusInternalServerError, "failed to prepare upload file")
			return
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			writeErr(w, http.StatusInternalServerError, "failed to sync upload file")
			return
		}
		if err := f.Close(); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to finalize upload file")
			return
		}
		now := time.Now().UnixMilli()
		meta = &chunkedUploadMeta{
			Version:      uploadManifestVersion,
			UploadID:     uploadID,
			ParentID:     id,
			Filename:     body.Filename,
			Size:         body.Size,
			Checksum:     body.Checksum,
			ChunkSize:    body.ChunkSize,
			TotalChunks:  total,
			Ownership:    ownershipToDomain(body.Ownership),
			OnConflict:   mode,
			StageDir:     stageDir,
			PartPath:     partPath,
			UploadedBits: ensureBitsetSize(nil, total),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := m.writeMeta(meta); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to create upload")
			return
		}
	} else {
		if meta.ParentID != id || meta.Filename != body.Filename || meta.Size != body.Size || meta.Checksum != body.Checksum || meta.ChunkSize != body.ChunkSize {
			writeErr(w, http.StatusConflict, "upload metadata mismatch for existing uploadId")
			return
		}
		// Resume: if the caller supplied a non-empty conflict mode, honor
		// it even if it differs from the persisted one. This lets a client
		// retry with "overwrite" or "rename" after an initial "error" was
		// rejected at finalize, without needing to abandon the upload.
		if mode != domain.ConflictError || strings.TrimSpace(body.OnConflict) != "" {
			meta.OnConflict = mode
		}
		meta.UpdatedAt = time.Now().UnixMilli()
		if err := m.writeMeta(meta); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to update upload")
			return
		}
	}

	writeJSON(w, http.StatusOK, apiv1.ChunkedStatusResponse{
		UploadID:       uploadID,
		ChunkSize:      meta.ChunkSize,
		TotalChunks:    meta.TotalChunks,
		UploadedChunks: uploadedChunkList(meta),
		Completed:      meta.Completed,
	})
}

func (m *chunkedManager) handleStatus(w http.ResponseWriter, r *http.Request) {
	uploadID := strings.TrimSpace(r.PathValue("uploadId"))
	if !uploadIDRE.MatchString(uploadID) {
		writeErr(w, http.StatusBadRequest, "invalid uploadId")
		return
	}
	meta, err := m.readMeta(uploadID)
	if err != nil {
		statusFromErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiv1.ChunkedStatusResponse{
		UploadID:       uploadID,
		ChunkSize:      meta.ChunkSize,
		TotalChunks:    meta.TotalChunks,
		UploadedChunks: uploadedChunkList(meta),
		Completed:      meta.Completed,
	})
}

func (m *chunkedManager) handleChunk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, m.maxChunkBytes+1024)

	uploadID := strings.TrimSpace(r.PathValue("uploadId"))
	if !uploadIDRE.MatchString(uploadID) {
		writeErr(w, http.StatusBadRequest, "invalid uploadId")
		return
	}
	chunkIdx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || chunkIdx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chunk index")
		return
	}
	headerChecksum := strings.TrimSpace(r.Header.Get("X-Chunk-Checksum"))
	if headerChecksum != "" && !checksumRE.MatchString(headerChecksum) {
		writeErr(w, http.StatusBadRequest, "invalid chunk checksum")
		return
	}
	if err := m.acquireChunkSlot(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "server busy")
		return
	}
	defer m.releaseChunkSlot()

	chunkLock := m.chunkLock(uploadID, chunkIdx)
	chunkLock.Lock()
	defer chunkLock.Unlock()

	lock := m.lock(uploadID)
	lock.Lock()
	meta, err := m.readMeta(uploadID)
	if err != nil {
		lock.Unlock()
		statusFromErr(w, err)
		return
	}
	if chunkIdx >= meta.TotalChunks {
		lock.Unlock()
		writeErr(w, http.StatusBadRequest, "chunk index out of range")
		return
	}
	if meta.Completed {
		lock.Unlock()
		node, err := nodeFromMeta(meta, m.svc)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, apiv1.ChunkedCompleteResponse{Completed: true, File: *node})
		return
	}
	if meta.Finalizing {
		progress := apiv1.ChunkedProgressResponse{
			ChunkIndex:     chunkIdx,
			UploadedChunks: uploadedChunkList(meta),
			Completed:      false,
		}
		lock.Unlock()
		writeJSON(w, http.StatusOK, progress)
		return
	}

	expectedSize, err := chunkExpectedSize(meta, chunkIdx)
	if err != nil {
		lock.Unlock()
		writeErr(w, http.StatusBadRequest, "invalid chunk index")
		return
	}
	offset := int64(chunkIdx) * meta.ChunkSize
	partPath := meta.PartPath
	chunkWasUploaded := hasBit(meta.UploadedBits, chunkIdx)
	lock.Unlock()

	incomingChecksum := ""
	if chunkWasUploaded {
		incomingChecksum, err = hashChunkFromReader(expectedSize, m.maxChunkBytes, r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid chunk data")
			return
		}
		existingChecksum, err := hashFileRange(partPath, offset, expectedSize)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to verify existing chunk")
			return
		}
		if existingChecksum != incomingChecksum {
			writeErr(w, http.StatusConflict, "chunk already exists with different content")
			return
		}
	} else {
		incomingChecksum, err = writeChunkAtPath(partPath, offset, expectedSize, m.maxChunkBytes, r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid chunk data")
			return
		}
		if headerChecksum != "" && incomingChecksum != headerChecksum {
			writeErr(w, http.StatusBadRequest, "chunk checksum mismatch")
			return
		}
	}

	lock.Lock()
	meta, err = m.readMeta(uploadID)
	if err != nil {
		lock.Unlock()
		statusFromErr(w, err)
		return
	}
	if chunkIdx >= meta.TotalChunks {
		lock.Unlock()
		writeErr(w, http.StatusBadRequest, "chunk index out of range")
		return
	}
	if meta.Completed {
		node, err := nodeFromMeta(meta, m.svc)
		lock.Unlock()
		if err != nil {
			statusFromErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, apiv1.ChunkedCompleteResponse{Completed: true, File: *node})
		return
	}
	if meta.Finalizing {
		progress := apiv1.ChunkedProgressResponse{
			ChunkIndex:     chunkIdx,
			UploadedChunks: uploadedChunkList(meta),
			Completed:      false,
		}
		lock.Unlock()
		writeJSON(w, http.StatusOK, progress)
		return
	}
	expectedSize, err = chunkExpectedSize(meta, chunkIdx)
	if err != nil {
		lock.Unlock()
		writeErr(w, http.StatusBadRequest, "invalid chunk index")
		return
	}
	offset = int64(chunkIdx) * meta.ChunkSize
	if hasBit(meta.UploadedBits, chunkIdx) {
		existingChecksum, err := hashFileRange(meta.PartPath, offset, expectedSize)
		if err != nil {
			lock.Unlock()
			writeErr(w, http.StatusInternalServerError, "failed to verify existing chunk")
			return
		}
		if existingChecksum != incomingChecksum {
			lock.Unlock()
			writeErr(w, http.StatusConflict, "chunk already exists with different content")
			return
		}
	} else {
		if setBit(meta.UploadedBits, chunkIdx) {
			meta.UploadedCnt++
		}
	}
	meta.UpdatedAt = time.Now().UnixMilli()
	if err := m.writeMeta(meta); err != nil {
		lock.Unlock()
		writeErr(w, http.StatusInternalServerError, "failed to persist upload progress")
		return
	}

	if meta.UploadedCnt < meta.TotalChunks {
		progress := apiv1.ChunkedProgressResponse{
			ChunkIndex:     chunkIdx,
			UploadedChunks: uploadedChunkList(meta),
			Completed:      false,
		}
		lock.Unlock()
		writeJSON(w, http.StatusOK, progress)
		return
	}
	meta.Finalizing = true
	meta.UpdatedAt = time.Now().UnixMilli()
	if err := m.writeMeta(meta); err != nil {
		lock.Unlock()
		writeErr(w, http.StatusInternalServerError, "failed to mark upload as finalizing")
		return
	}
	finalizeMeta := *meta
	lock.Unlock()

	fileNode, err := m.finalize(&finalizeMeta)
	if err != nil {
		// Authoritative conflict check at finalize: a concurrent writer
		// may have created the target between start and finalize. Surface
		// it as 409 with diagnostic fields so the client can retry the
		// /start with a different onConflict mode.
		if errors.Is(err, domain.ErrConflict) {
			parentMeta, _ := m.svc.GetFile(finalizeMeta.ParentID)
			id, path := "", ""
			if parentMeta != nil {
				id, path, _ = lookupChildOfParent(m.svc, parentMeta, finalizeMeta.Filename)
			}
			writeConflict(w, "filename already exists in parent at finalize", id, path)
			return
		}
		msg := "failed to finalize file"
		if strings.Contains(err.Error(), "checksum mismatch") {
			msg = "checksum mismatch"
		}
		writeErr(w, http.StatusInternalServerError, msg)
		return
	}
	writeJSON(w, http.StatusOK, apiv1.ChunkedCompleteResponse{Completed: true, File: *fileNode})
}

func (m *chunkedManager) cleanupExpired() error {
	if m.expiry <= 0 {
		return nil
	}
	now := time.Now()
	for _, root := range m.mountRoots() {
		stageRoot := filepath.Join(root, uploadStagingDirName)
		dirs, err := os.ReadDir(stageRoot)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			uploadID := d.Name()
			lock := m.lock(uploadID)
			lock.Lock()
			uploadDir := filepath.Join(stageRoot, uploadID)
			payload, err := os.ReadFile(manifestPath(uploadDir))
			if err != nil {
				if rmErr := os.RemoveAll(uploadDir); rmErr != nil {
					log.Printf("[filegate] warning: failed to remove orphaned upload dir %s: %v", uploadDir, rmErr)
				}
				lock.Unlock()
				m.uploadDirs.Delete(uploadID)
				m.locks.Delete(uploadID)
				m.clearChunkLocks(uploadID)
				continue
			}
			var meta chunkedUploadMeta
			if err := json.Unmarshal(payload, &meta); err != nil {
				if rmErr := os.RemoveAll(uploadDir); rmErr != nil {
					log.Printf("[filegate] warning: failed to remove corrupt upload dir %s: %v", uploadDir, rmErr)
				}
				lock.Unlock()
				m.uploadDirs.Delete(uploadID)
				m.locks.Delete(uploadID)
				m.clearChunkLocks(uploadID)
				continue
			}
			ts := meta.UpdatedAt
			if ts == 0 {
				ts = meta.CreatedAt
			}
			if ts == 0 {
				ts = now.UnixMilli()
			}
			if now.Sub(time.UnixMilli(ts)) <= m.expiry {
				lock.Unlock()
				continue
			}
			if rmErr := os.RemoveAll(uploadDir); rmErr != nil {
				log.Printf("[filegate] warning: failed to remove expired upload dir %s: %v", uploadDir, rmErr)
			}
			lock.Unlock()
			m.uploadDirs.Delete(uploadID)
			m.locks.Delete(uploadID)
			m.clearChunkLocks(uploadID)
		}
	}
	return nil
}
