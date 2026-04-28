package httpadapter

import (
	"archive/tar"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	apiv1 "github.com/valentinkolb/filegate/api/v1"
	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/jobs"
)

// RouterOptions configures the HTTP router including authentication, upload limits,
// thumbnail generation, and background job worker pools.
type RouterOptions struct {
	BearerToken              string
	AccessLogEnabled         bool
	IndexPath                string
	JobWorkers               int
	JobQueueSize             int
	ThumbnailJobWorkers      int
	ThumbnailJobQueueSize    int
	UploadExpiry             time.Duration
	UploadCleanupInterval    time.Duration
	MaxChunkBytes            int64
	MaxUploadBytes           int64
	MaxChunkedUploadBytes    int64
	MaxConcurrentChunkWrites int
	UploadMinFreeBytes       int64
	ThumbnailLRUCacheSize    int
	ThumbnailMaxSourceBytes  int64
	ThumbnailMaxPixels       int64
	Rescan                   func() error
}

type closeableHandler struct {
	handler   http.Handler
	closeFn   func()
	closeOnce sync.Once
}

type middlewareFunc func(http.Handler) http.Handler

type requestIDKey struct{}

var reqIDCounter uint64

var copyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 128*1024)
		return &buf
	},
}

var mountInfoCache struct {
	mu      sync.Mutex
	loaded  time.Time
	entries []mountInfo
}

func (h *closeableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func (h *closeableHandler) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.closeFn != nil {
			h.closeFn()
		}
	})
	return nil
}

// NewRouter constructs the HTTP handler tree with all routes, middleware, and background workers.
func NewRouter(svc *domain.Service, opts RouterOptions) http.Handler {
	root := http.NewServeMux()

	thumbnailWorkers := resolveThumbnailJobWorkers(opts)
	thumbnailQueueSize := resolveThumbnailQueueSize(opts)

	thumbnailScheduler := jobs.New(thumbnailWorkers, thumbnailQueueSize)
	chunked := newChunkedManager(
		svc,
		opts.UploadExpiry,
		opts.UploadCleanupInterval,
		opts.MaxChunkBytes,
		opts.MaxUploadBytes,
		opts.MaxChunkedUploadBytes,
		opts.MaxConcurrentChunkWrites,
		opts.UploadMinFreeBytes,
	)
	thumbs := newThumbnailer(
		svc,
		opts.ThumbnailLRUCacheSize,
		opts.ThumbnailMaxSourceBytes,
		opts.ThumbnailMaxPixels,
		thumbnailScheduler,
	)

	root.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	auth := authMiddleware(opts.BearerToken)
	handleV1 := func(pattern string, handler http.HandlerFunc) {
		root.Handle(pattern, auth(http.HandlerFunc(handler)))
	}

	handleV1("GET /v1/stats", func(w http.ResponseWriter, _ *http.Request) {
		stats, err := svc.Stats()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to collect stats")
			return
		}

		indexDBSizeBytes := int64(0)
		if strings.TrimSpace(opts.IndexPath) != "" {
			if sz, err := dirSizeBytes(opts.IndexPath); err == nil {
				indexDBSizeBytes = sz
			}
		}

		mounts := make([]apiv1.StatsMount, 0, len(stats.Mounts))
		for _, m := range stats.Mounts {
			mounts = append(mounts, apiv1.StatsMount{
				ID:    m.ID.String(),
				Name:  m.Name,
				Path:  m.Path,
				Files: m.Files,
				Dirs:  m.Dirs,
			})
		}

		writeJSON(w, http.StatusOK, apiv1.StatsResponse{
			GeneratedAt: stats.GeneratedAt,
			Index: apiv1.StatsIndex{
				TotalEntities: stats.TotalEntities,
				TotalFiles:    stats.TotalFiles,
				TotalDirs:     stats.TotalDirs,
				DBSizeBytes:   indexDBSizeBytes,
			},
			Cache: apiv1.StatsCache{
				PathEntries:   stats.PathCacheEntries,
				PathCapacity:  stats.PathCacheCapacity,
				PathUtilRatio: stats.PathCacheUtilRatio,
			},
			Mounts: mounts,
			Disks:  collectDiskUsage(stats.Mounts, svc),
		})
	})

	handleV1("GET /v1/paths/{$}", func(w http.ResponseWriter, _ *http.Request) {
		mounts := svc.ListRoot()
		items := make([]apiv1.Node, 0, len(mounts))
		for _, m := range mounts {
			meta, err := svc.GetFile(m.ID)
			if err != nil {
				continue
			}
			items = append(items, nodeResponse(meta))
		}
		writeJSON(w, http.StatusOK, apiv1.NodeListResponse{Items: items, Total: len(items)})
	})

	handleV1("GET /v1/paths/{path...}", func(w http.ResponseWriter, r *http.Request) {
		vp := strings.TrimPrefix(r.PathValue("path"), "/")
		if strings.TrimSpace(vp) == "" {
			writeErr(w, http.StatusBadRequest, "path required")
			return
		}
		meta, err := svc.GetFileByVirtualPath(vp)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		respondMetaWithOptionalChildren(w, r, svc, meta)
	})

	handleV1("PUT /v1/paths/{path...}", func(w http.ResponseWriter, r *http.Request) {
		vp := strings.TrimPrefix(r.PathValue("path"), "/")
		if strings.TrimSpace(vp) == "" {
			writeErr(w, http.StatusBadRequest, "path required")
			return
		}
		mode, err := domain.ParseConflictMode(r.URL.Query().Get("onConflict"), domain.FileConflictModes)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, opts.MaxUploadBytes)
		meta, created, err := svc.WriteContentByVirtualPath(vp, r.Body, mode)
		if err != nil {
			if errors.Is(err, domain.ErrConflict) {
				existingID, existingPath := lookupExistingByPath(svc, vp)
				writeConflict(w, "path already exists", existingID, existingPath)
				return
			}
			statusFromErr(w, err)
			return
		}
		w.Header().Set("X-Node-Id", meta.ID.String())
		if created {
			w.Header().Set("X-Created-Id", meta.ID.String())
			writeJSON(w, http.StatusCreated, nodeResponse(meta))
			return
		}
		writeJSON(w, http.StatusOK, nodeResponse(meta))
	})

	handleV1("GET /v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r.PathValue("id"))
		if !ok {
			return
		}
		respondNodeWithOptionalChildren(w, r, svc, id)
	})

	handleV1("GET /v1/nodes/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r.PathValue("id"))
		if !ok {
			return
		}
		meta, err := svc.GetFile(id)
		if err != nil {
			statusFromErr(w, err)
			return
		}

		if meta.Type == "directory" {
			abs, err := svc.ResolveAbsPath(id)
			if err != nil {
				statusFromErr(w, err)
				return
			}
			if err := preflightDirectoryTar(abs); err != nil {
				if errors.Is(err, os.ErrPermission) {
					writeErr(w, http.StatusForbidden, "directory content is not readable")
					return
				}
				writeErr(w, http.StatusInternalServerError, "failed to prepare directory download")
				return
			}
			tarName := meta.Name + ".tar"
			w.Header().Set("Content-Type", "application/x-tar")
			setContentDisposition(w, "attachment", tarName)
			if err := writeDirectoryTar(w, abs, meta.Name); err != nil {
				log.Printf("[filegate] tar stream failed for node=%s path=%s: %v", id.String(), abs, err)
			}
			return
		}

		reader, size, _, err := svc.OpenContent(id)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		defer reader.Close()

		contentType := mime.TypeByExtension(filepath.Ext(meta.Name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if r.URL.Query().Get("inline") == "true" {
			setContentDisposition(w, "inline", meta.Name)
		} else {
			setContentDisposition(w, "attachment", meta.Name)
		}
		bufPtr := copyBufPool.Get().(*[]byte)
		buf := *bufPtr
		_, _ = io.CopyBuffer(w, reader, buf)
		copyBufPool.Put(bufPtr)
	})

	handleV1("GET /v1/nodes/{id}/thumbnail", thumbs.handleGet)

	handleV1("POST /v1/nodes/{id}/mkdir", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r.PathValue("id"))
		if !ok {
			return
		}
		var body apiv1.MkdirRequest
		if ok := decodeJSONBody(w, r, &body); !ok {
			return
		}
		recursive := true
		if body.Recursive != nil {
			recursive = *body.Recursive
		}
		mode, err := domain.ParseConflictMode(body.OnConflict, domain.MkdirConflictModes)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		created, err := svc.MkdirRelative(id, body.Path, recursive, ownershipToDomain(body.Ownership), mode)
		if err != nil {
			if errors.Is(err, domain.ErrConflict) {
				existingID, existingPath := lookupExistingUnderParent(svc, id, body.Path)
				writeConflict(w, "path already exists", existingID, existingPath)
				return
			}
			statusFromErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, nodeResponse(created))
	})

	handleV1("PUT /v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r.PathValue("id"))
		if !ok {
			return
		}
		meta, err := svc.GetFile(id)
		if err != nil {
			statusFromErr(w, err)
			return
		}

		if meta.Type != "file" {
			writeErr(w, http.StatusBadRequest, "content writes are only allowed on file nodes")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, opts.MaxUploadBytes)
		if err := svc.WriteContent(id, r.Body); err != nil {
			statusFromErr(w, err)
			return
		}
		updated, err := svc.GetFile(id)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nodeResponse(updated))
	})

	handleV1("PATCH /v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r.PathValue("id"))
		if !ok {
			return
		}
		recursiveOwnership := parseBoolDefault(r.URL.Query().Get("recursiveOwnership"), true)
		var body apiv1.UpdateNodeRequest
		if ok := decodeJSONBody(w, r, &body); !ok {
			return
		}
		if body.Name == nil && body.Ownership == nil {
			writeErr(w, http.StatusBadRequest, "name or ownership required")
			return
		}
		updated, err := svc.UpdateNode(id, body.Name, ownershipToDomain(body.Ownership), recursiveOwnership)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nodeResponse(updated))
	})

	handleV1("DELETE /v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r.PathValue("id"))
		if !ok {
			return
		}
		if err := svc.Delete(id); err != nil {
			statusFromErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	handleV1("POST /v1/transfers", func(w http.ResponseWriter, r *http.Request) {
		recursiveOwnership := parseBoolDefault(r.URL.Query().Get("recursiveOwnership"), true)
		var body apiv1.TransferRequest
		if ok := decodeJSONBody(w, r, &body); !ok {
			return
		}
		sourceID, err := domain.ParseFileID(body.SourceID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid sourceId")
			return
		}
		targetParentID, err := domain.ParseFileID(body.TargetParentID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid targetParentId")
			return
		}
		mode, err := domain.ParseConflictMode(body.OnConflict, domain.FileConflictModes)
		if err != nil {
			statusFromErr(w, err)
			return
		}

		out, err := svc.Transfer(domain.TransferRequest{
			Op:                 body.Op,
			SourceID:           sourceID,
			TargetParentID:     targetParentID,
			TargetName:         body.TargetName,
			OnConflict:         mode,
			Ownership:          ownershipToDomain(body.Ownership),
			RecursiveOwnership: &recursiveOwnership,
		})
		if err != nil {
			if errors.Is(err, domain.ErrConflict) {
				existingID, existingPath := lookupExistingChildOfID(svc, targetParentID, body.TargetName)
				writeConflict(w, "target name already exists in parent", existingID, existingPath)
				return
			}
			statusFromErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, apiv1.TransferResponse{
			Node: nodeResponse(out),
			Op:   strings.ToLower(strings.TrimSpace(body.Op)),
		})
	})

	handleV1("GET /v1/search/glob", func(w http.ResponseWriter, r *http.Request) {
		pattern := strings.TrimSpace(r.URL.Query().Get("pattern"))
		if pattern == "" {
			writeErr(w, http.StatusBadRequest, "pattern required")
			return
		}
		if len(pattern) > 500 {
			writeErr(w, http.StatusBadRequest, "pattern too long")
			return
		}
		if strings.Count(pattern, "**") > 10 {
			writeErr(w, http.StatusBadRequest, "too many recursive wildcards")
			return
		}

		limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
		showHidden := parseBoolDefault(r.URL.Query().Get("showHidden"), false)
		includeFiles := parseBoolDefault(r.URL.Query().Get("files"), true)
		includeDirs := parseBoolDefault(r.URL.Query().Get("directories"), false)
		paths := parseList(r.URL.Query().Get("paths"))

		out, err := svc.SearchGlob(domain.GlobSearchRequest{
			Pattern:      pattern,
			Paths:        paths,
			Limit:        limit,
			ShowHidden:   showHidden,
			IncludeFiles: includeFiles,
			IncludeDirs:  includeDirs,
		})
		if err != nil {
			statusFromErr(w, err)
			return
		}

		results := make([]apiv1.Node, 0, len(out.Results))
		for i := range out.Results {
			results = append(results, nodeResponse(&out.Results[i]))
		}
		errorsOut := make([]apiv1.GlobSearchError, 0, len(out.Errors))
		for i := range out.Errors {
			errorsOut = append(errorsOut, apiv1.GlobSearchError{
				Path:  out.Errors[i].Path,
				Cause: out.Errors[i].Cause,
			})
		}
		pathsOut := make([]apiv1.GlobSearchPath, 0, len(out.Paths))
		for i := range out.Paths {
			pathsOut = append(pathsOut, apiv1.GlobSearchPath{
				Path:     out.Paths[i].Path,
				Returned: out.Paths[i].Returned,
				HasMore:  out.Paths[i].HasMore,
			})
		}

		writeJSON(w, http.StatusOK, apiv1.GlobSearchResponse{
			Results: results,
			Errors:  errorsOut,
			Meta: apiv1.GlobSearchMeta{
				Pattern:     pattern,
				Limit:       limit,
				ResultCount: len(results),
				ErrorCount:  len(out.Errors),
			},
			Paths: pathsOut,
		})
	})

	handleV1("POST /v1/uploads/chunked/start", chunked.handleStart)
	handleV1("GET /v1/uploads/chunked/{uploadId}", chunked.handleStatus)
	handleV1("PUT /v1/uploads/chunked/{uploadId}/chunks/{index}", chunked.handleChunk)

	handleV1("POST /v1/index/rescan", func(w http.ResponseWriter, _ *http.Request) {
		if opts.Rescan == nil {
			writeErr(w, http.StatusNotImplemented, "rescan not configured")
			return
		}
		if err := opts.Rescan(); err != nil {
			writeErr(w, http.StatusInternalServerError, "rescan failed")
			return
		}
		writeJSON(w, http.StatusOK, apiv1.OKResponse{OK: true})
	})

	handleV1("POST /v1/index/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body apiv1.IndexResolveRequest
		if ok := decodeJSONBody(w, r, &body); !ok {
			return
		}

		singlePath := strings.TrimSpace(body.Path)
		if singlePath != "" {
			item, err := resolveNodeByVirtualPath(svc, singlePath)
			if err != nil {
				statusFromErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, apiv1.IndexResolveSingleResponse{Item: item})
			return
		}

		if len(body.Paths) > 0 {
			items := make([]*apiv1.Node, 0, len(body.Paths))
			for _, raw := range body.Paths {
				item, err := resolveNodeByVirtualPath(svc, raw)
				if err != nil {
					statusFromErr(w, err)
					return
				}
				items = append(items, item)
			}
			writeJSON(w, http.StatusOK, apiv1.IndexResolveManyResponse{Items: items, Total: len(items)})
			return
		}

		singleID := strings.TrimSpace(body.ID)
		if singleID != "" {
			item, err := resolveNodeByID(svc, singleID)
			if err != nil {
				statusFromErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, apiv1.IndexResolveSingleResponse{Item: item})
			return
		}

		if len(body.IDs) == 0 {
			writeErr(w, http.StatusBadRequest, "path/paths or id/ids required")
			return
		}

		items := make([]*apiv1.Node, 0, len(body.IDs))
		for _, raw := range body.IDs {
			item, err := resolveNodeByID(svc, raw)
			if err != nil {
				statusFromErr(w, err)
				return
			}
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, apiv1.IndexResolveManyResponse{Items: items, Total: len(items)})
	})
	chain := []middlewareFunc{recoverMiddleware, requestIDMiddleware, realIPMiddleware, secureHeadersMiddleware}
	if opts.AccessLogEnabled {
		chain = append(chain, accessLogMiddleware)
	}
	handler := chainMiddleware(root, chain...)
	return &closeableHandler{
		handler: handler,
		closeFn: func() {
			chunked.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := thumbnailScheduler.Close(ctx); err != nil {
				log.Printf("[filegate] thumbnail scheduler close: %v", err)
			}
		},
	}
}

func respondNodeWithOptionalChildren(w http.ResponseWriter, r *http.Request, svc *domain.Service, id domain.FileID) {
	meta, err := svc.GetFile(id)
	if err != nil {
		statusFromErr(w, err)
		return
	}
	respondMetaWithOptionalChildren(w, r, svc, meta)
}

func respondMetaWithOptionalChildren(w http.ResponseWriter, r *http.Request, svc *domain.Service, meta *domain.FileMeta) {
	response := nodeResponse(meta)
	if meta.Type == "directory" {
		pageSize := parseIntDefault(r.URL.Query().Get("pageSize"), 100)
		cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
		computeRecursiveSizes := strings.EqualFold(r.URL.Query().Get("computeRecursiveSizes"), "true")
		listed, err := svc.ListNodeChildren(meta.ID, cursor, pageSize, computeRecursiveSizes)
		if err != nil {
			statusFromErr(w, err)
			return
		}
		items := make([]apiv1.Node, 0, len(listed.Items))
		for i := range listed.Items {
			items = append(items, nodeResponse(&listed.Items[i]))
		}
		response.Children = items
		response.PageSize = &pageSize
		if listed.NextCursor != "" {
			response.NextCursor = listed.NextCursor
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func parseIntDefault(v string, def int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func parseBoolDefault(v string, def bool) bool {
	if strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func parseList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

const maxJSONBodyBytes int64 = 1 << 20

func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func resolveNodeByID(svc *domain.Service, rawID string) (*apiv1.Node, error) {
	id, err := domain.ParseFileID(strings.TrimSpace(rawID))
	if err != nil {
		return nil, domain.ErrInvalidArgument
	}
	meta, err := svc.GetFile(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	node := nodeResponse(meta)
	return &node, nil
}

func resolveNodeByVirtualPath(svc *domain.Service, rawPath string) (*apiv1.Node, error) {
	id, err := svc.ResolvePath(rawPath)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	meta, err := svc.GetFile(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	node := nodeResponse(meta)
	return &node, nil
}

func dirSizeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

type mountInfo struct {
	devID      string
	mountPoint string
	fsType     string
	source     string
}

func collectDiskUsage(mounts []domain.StatsMount, svc *domain.Service) []apiv1.StatsDisk {
	infos := readMountInfoCached(30 * time.Second)
	type diskGroup struct {
		DiskName string
		FSType   string
		Used     uint64
		Size     uint64
		Roots    []string
	}
	groups := make(map[string]*diskGroup)

	for _, mount := range mounts {
		abs, err := svc.ResolveAbsPath(mount.ID)
		if err != nil {
			continue
		}

		var st syscall.Statfs_t
		if err := syscall.Statfs(abs, &st); err != nil {
			continue
		}
		size := uint64(st.Blocks) * uint64(st.Bsize)
		used := uint64(st.Blocks-st.Bfree) * uint64(st.Bsize)

		key := abs
		diskName := "unknown"
		fsType := ""
		if info, ok := bestMountInfo(abs, infos); ok {
			if strings.TrimSpace(info.devID) != "" {
				key = info.devID
			}
			if strings.TrimSpace(info.source) != "" {
				diskName = info.source
			}
			fsType = info.fsType
		}

		group, exists := groups[key]
		if !exists {
			group = &diskGroup{
				DiskName: diskName,
				FSType:   fsType,
				Used:     used,
				Size:     size,
				Roots:    []string{},
			}
			groups[key] = group
		}

		if !containsString(group.Roots, mount.Path) {
			group.Roots = append(group.Roots, mount.Path)
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]apiv1.StatsDisk, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		sort.Strings(group.Roots)
		out = append(out, apiv1.StatsDisk{
			DiskName: group.DiskName,
			FSType:   group.FSType,
			Used:     group.Used,
			Size:     group.Size,
			Roots:    group.Roots,
		})
	}
	return out
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func bestMountInfo(absPath string, infos []mountInfo) (mountInfo, bool) {
	bestLen := -1
	var best mountInfo
	for _, info := range infos {
		mp := strings.TrimSpace(info.mountPoint)
		if mp == "" {
			continue
		}
		if !pathHasPrefix(absPath, mp) {
			continue
		}
		if len(mp) > bestLen {
			bestLen = len(mp)
			best = info
		}
	}
	if bestLen < 0 {
		return mountInfo{}, false
	}
	return best, true
}

func pathHasPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	return strings.HasPrefix(path, prefix+"/")
}

func readMountInfo() []mountInfo {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	out := make([]mountInfo, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left := strings.Fields(parts[0])
		right := strings.Fields(parts[1])
		if len(left) < 5 || len(right) < 2 {
			continue
		}
		out = append(out, mountInfo{
			devID:      strings.TrimSpace(left[2]),
			mountPoint: decodeMountInfoField(left[4]),
			fsType:     strings.TrimSpace(right[0]),
			source:     strings.TrimSpace(right[1]),
		})
	}
	return out
}

func readMountInfoCached(ttl time.Duration) []mountInfo {
	now := time.Now()
	mountInfoCache.mu.Lock()
	defer mountInfoCache.mu.Unlock()
	if ttl > 0 && len(mountInfoCache.entries) > 0 && now.Sub(mountInfoCache.loaded) < ttl {
		cached := make([]mountInfo, len(mountInfoCache.entries))
		copy(cached, mountInfoCache.entries)
		return cached
	}
	fresh := readMountInfo()
	mountInfoCache.loaded = now
	mountInfoCache.entries = fresh
	cached := make([]mountInfo, len(fresh))
	copy(cached, fresh)
	return cached
}

func decodeMountInfoField(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+3 < len(v) {
			oct := v[i+1 : i+4]
			if oct[0] >= '0' && oct[0] <= '7' &&
				oct[1] >= '0' && oct[1] <= '7' &&
				oct[2] >= '0' && oct[2] <= '7' {
				n, err := strconv.ParseUint(oct, 8, 8)
				if err == nil {
					b.WriteByte(byte(n))
					i += 3
					continue
				}
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func writeDirectoryTar(w io.Writer, dirPath, rootName string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	return filepath.WalkDir(dirPath, func(current string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dirPath, current)
		if err != nil {
			return err
		}
		if rel == "." {
			rel = ""
		}
		name := filepath.ToSlash(filepath.Join(rootName, rel))
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		linfo, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if linfo.Mode()&os.ModeSymlink != 0 || !linfo.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(current)
		if err != nil {
			return err
		}
		bufPtr := copyBufPool.Get().(*[]byte)
		buf := *bufPtr
		_, copyErr := io.CopyBuffer(tw, f, buf)
		copyBufPool.Put(bufPtr)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func preflightDirectoryTar(dirPath string) error {
	return filepath.WalkDir(dirPath, func(current string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(current)
		if err != nil {
			return err
		}
		return f.Close()
	})
}

func resolveThumbnailJobWorkers(opts RouterOptions) int {
	if opts.ThumbnailJobWorkers > 0 {
		return opts.ThumbnailJobWorkers
	}
	if opts.JobWorkers > 0 {
		if opts.JobWorkers < 16 {
			return 16
		}
		return opts.JobWorkers
	}
	return 32
}

func resolveThumbnailQueueSize(opts RouterOptions) int {
	if opts.ThumbnailJobQueueSize > 0 {
		return opts.ThumbnailJobQueueSize
	}
	if opts.JobQueueSize > 0 {
		n := opts.JobQueueSize * 2
		if n < 8192 {
			return 8192
		}
		if n > 65536 {
			return 65536
		}
		return n
	}
	return 16384
}

func chainMiddleware(h http.Handler, middlewares ...middlewareFunc) http.Handler {
	out := h
	for i := len(middlewares) - 1; i >= 0; i-- {
		out = middlewares[i](out)
	}
	return out
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[filegate] panic: %v", rec)
				writeErr(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id string
		if v := strings.TrimSpace(r.Header.Get("X-Request-Id")); v != "" {
			id = v
		} else {
			var b [8]byte
			n := atomic.AddUint64(&reqIDCounter, 1)
			binary.BigEndian.PutUint64(b[:], n^uint64(time.Now().UnixNano()))
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func realIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		if xff != "" {
			if idx := strings.IndexByte(xff, ','); idx >= 0 {
				xff = strings.TrimSpace(xff[:idx])
			}
			if ip, err := netip.ParseAddr(xff); err == nil {
				r.RemoteAddr = ip.String()
			}
		} else if xrip := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xrip != "" {
			if ip, err := netip.ParseAddr(xrip); err == nil {
				r.RemoteAddr = ip.String()
			}
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("[filegate] method=%s path=%s status=%d dur=%s remote=%s",
			r.Method, r.URL.Path, sw.status, time.Since(start), r.RemoteAddr)
	})
}

func authMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			if token == "" {
				writeErr(w, http.StatusUnauthorized, "bearer token not configured")
				return
			}
			if !strings.HasPrefix(auth, "Bearer ") {
				writeErr(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			provided := strings.TrimPrefix(auth, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				writeErr(w, http.StatusUnauthorized, "invalid bearer token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setContentDisposition(w http.ResponseWriter, disposition, filename string) {
	clean := strings.NewReplacer("\r", "_", "\n", "_").Replace(filename)
	if clean == "" {
		clean = "download"
	}
	if v := mime.FormatMediaType(disposition, map[string]string{"filename": clean}); v != "" {
		w.Header().Set("Content-Disposition", v)
		return
	}
	w.Header().Set("Content-Disposition", disposition)
}

func secureHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func parseID(w http.ResponseWriter, v string) (domain.FileID, bool) {
	id, err := domain.ParseFileID(v)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return domain.FileID{}, false
	}
	return id, true
}

func ownershipToDomain(in *apiv1.Ownership) *domain.Ownership {
	if in == nil {
		return nil
	}
	return &domain.Ownership{
		UID:     in.UID,
		GID:     in.GID,
		Mode:    in.Mode,
		DirMode: in.DirMode,
	}
}

func nodeResponse(meta *domain.FileMeta) apiv1.Node {
	resp := apiv1.Node{
		ID:    meta.ID.String(),
		Type:  meta.Type,
		Name:  meta.Name,
		Path:  meta.Path,
		Size:  meta.Size,
		Mtime: meta.Mtime,
		Ownership: apiv1.OwnershipView{
			UID:  meta.UID,
			GID:  meta.GID,
			Mode: strconv.FormatUint(uint64(meta.Mode), 8),
		},
		Exif: map[string]string{},
	}
	if meta.MimeType != "" {
		resp.MimeType = meta.MimeType
	}
	if len(meta.Exif) > 0 {
		resp.Exif = meta.Exif
	}
	return resp
}

func statusFromErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrConflict):
		writeErr(w, http.StatusConflict, "conflict")
	case errors.Is(err, domain.ErrForbidden):
		writeErr(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, domain.ErrInvalidArgument):
		writeErr(w, http.StatusBadRequest, "invalid argument")
	case errors.Is(err, domain.ErrInsufficientStorage), errors.Is(err, syscall.ENOSPC):
		writeErr(w, http.StatusInsufficientStorage, "insufficient storage")
	default:
		log.Printf("[filegate] internal error: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiv1.ErrorResponse{Error: msg})
}

// writeConflict produces a 409 with diagnostic fields the client can use to
// render a "what should we do?" prompt without an extra resolve round trip.
// existingPath/existingID may be empty if not known.
func writeConflict(w http.ResponseWriter, msg, existingID, existingPath string) {
	writeJSON(w, http.StatusConflict, apiv1.ErrorResponse{
		Error:        msg,
		ExistingID:   existingID,
		ExistingPath: existingPath,
	})
}

// lookupExistingByPath best-effort resolves a virtual path back to an
// existing node so a 409 response can include diagnostic fields. Errors are
// silenced — the conflict response is still useful without these fields.
func lookupExistingByPath(svc *domain.Service, virtualPath string) (id, path string) {
	resolvedID, err := svc.ResolvePath(virtualPath)
	if err != nil {
		return "", ""
	}
	meta, err := svc.GetFile(resolvedID)
	if err != nil {
		return resolvedID.String(), ""
	}
	return meta.ID.String(), meta.Path
}

// lookupExistingUnderParent best-effort resolves parent+relPath to a
// node for the diagnostic fields on a mkdir conflict.
func lookupExistingUnderParent(svc *domain.Service, parentID domain.FileID, relPath string) (id, path string) {
	parent, err := svc.GetFile(parentID)
	if err != nil {
		return "", ""
	}
	cleaned := strings.Trim(strings.TrimSpace(relPath), "/")
	if cleaned == "" {
		return parent.ID.String(), parent.Path
	}
	return lookupExistingByPath(svc, parent.Path+"/"+cleaned)
}

// lookupChildOfParent reports whether a child with the given name exists
// under parent and, if so, returns the diagnostic id/path strings. Used by
// chunked upload start to fail fast before any chunk is uploaded.
func lookupChildOfParent(svc *domain.Service, parent *domain.FileMeta, name string) (id, path string, found bool) {
	if parent == nil {
		return "", "", false
	}
	id2, path2 := lookupExistingByPath(svc, parent.Path+"/"+name)
	if id2 == "" {
		return "", "", false
	}
	return id2, path2, true
}

// lookupExistingChildOfID resolves parentID + child name to the diagnostic
// fields for a 409 response on the transfer endpoint. Best-effort — empty
// strings on lookup failure.
func lookupExistingChildOfID(svc *domain.Service, parentID domain.FileID, name string) (id, path string) {
	parent, err := svc.GetFile(parentID)
	if err != nil {
		return "", ""
	}
	id2, path2, _ := lookupChildOfParent(svc, parent, name)
	return id2, path2
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
