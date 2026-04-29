package domain

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
)

type pathCacheEntry struct {
	ID FileID
}

type normalizedOwnership struct {
	uid     *int
	gid     *int
	mode    *os.FileMode
	dirMode *os.FileMode
}

// Service is the central orchestrator that manages mounts, caching, and all
// CRUD operations against the index and filesystem store.
type Service struct {
	idx   Index
	store Store
	bus   EventBus

	mountByName   map[string]string
	mountIDByName map[string]FileID
	mountNameByID map[FileID]string
	mountNames    []string

	cache         *lru.Cache[string, pathCacheEntry]
	idPathCache   *lru.Cache[FileID, string]
	pathCacheSize int
	dirSync       dirSyncer
	mu            sync.RWMutex
	rescanMu      sync.Mutex
}

// NewService creates a Service with the given infrastructure adapters and mount paths.
func NewService(idx Index, store Store, bus EventBus, basePaths []string, pathCacheSize int) (*Service, error) {
	if len(basePaths) == 0 {
		return nil, fmt.Errorf("at least one base path required")
	}

	effectivePathCacheSize := max(pathCacheSize, 1000)
	cache, err := lru.New[string, pathCacheEntry](effectivePathCacheSize)
	if err != nil {
		return nil, err
	}
	idPathCache, err := lru.New[FileID, string](effectivePathCacheSize)
	if err != nil {
		return nil, err
	}

	svc := &Service{
		idx:           idx,
		store:         store,
		bus:           bus,
		mountByName:   make(map[string]string, len(basePaths)),
		mountIDByName: make(map[string]FileID, len(basePaths)),
		mountNameByID: make(map[FileID]string, len(basePaths)),
		cache:         cache,
		idPathCache:   idPathCache,
		pathCacheSize: effectivePathCacheSize,
		dirSync:       newDirSyncer(),
	}

	for _, p := range basePaths {
		abs, err := store.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("base path invalid %q: %w", p, err)
		}
		name := filepath.Base(abs)
		if name == "." || name == string(filepath.Separator) || name == "" {
			return nil, fmt.Errorf("base path %q has invalid mount name", abs)
		}
		if _, exists := svc.mountByName[name]; exists {
			return nil, fmt.Errorf("duplicate mount name %q from base path %q", name, abs)
		}
		svc.mountByName[name] = abs
		svc.mountNames = append(svc.mountNames, name)
	}
	sort.Strings(svc.mountNames)

	if err := svc.bootstrapMounts(); err != nil {
		return nil, err
	}
	if err := svc.Rescan(); err != nil {
		return nil, err
	}

	return svc, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func newID() (FileID, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return FileID{}, err
	}
	var id FileID
	copy(id[:], u[:16])
	return id, nil
}

func parseModeString(v string) (os.FileMode, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, ErrInvalidArgument
	}
	u, err := strconv.ParseUint(v, 8, 32)
	if err != nil {
		return 0, ErrInvalidArgument
	}
	return os.FileMode(u), nil
}

func fileModeToDirMode(mode os.FileMode) os.FileMode {
	dir := mode
	if mode&0o400 != 0 {
		dir |= 0o100
	}
	if mode&0o040 != 0 {
		dir |= 0o010
	}
	if mode&0o004 != 0 {
		dir |= 0o001
	}
	return dir
}

func normalizeOwnership(ownership *Ownership) (*normalizedOwnership, error) {
	if ownership == nil {
		return nil, nil
	}
	out := &normalizedOwnership{
		uid: ownership.UID,
		gid: ownership.GID,
	}
	if (out.uid == nil) != (out.gid == nil) {
		return nil, ErrInvalidArgument
	}
	if ownership.Mode != "" {
		mode, err := parseModeString(ownership.Mode)
		if err != nil {
			return nil, err
		}
		out.mode = &mode
	}
	if ownership.DirMode != "" {
		mode, err := parseModeString(ownership.DirMode)
		if err != nil {
			return nil, err
		}
		out.dirMode = &mode
	}
	if out.mode != nil && out.dirMode == nil {
		derived := fileModeToDirMode(*out.mode)
		out.dirMode = &derived
	}
	return out, nil
}

func ownershipIsEmpty(ownership *Ownership) bool {
	if ownership == nil {
		return true
	}
	return ownership.UID == nil &&
		ownership.GID == nil &&
		strings.TrimSpace(ownership.Mode) == "" &&
		strings.TrimSpace(ownership.DirMode) == ""
}

func deriveFileModeFromDirMode(dirMode os.FileMode) os.FileMode {
	mode := dirMode &^ 0o111
	if mode == 0 {
		return 0o644
	}
	return mode
}

func (s *Service) inheritedOwnershipFromParent(parentID FileID) (*Ownership, error) {
	parent, err := s.GetFile(parentID)
	if err != nil {
		return nil, err
	}
	if parent.Type != "directory" {
		return nil, ErrInvalidArgument
	}
	uid := int(parent.UID)
	gid := int(parent.GID)
	dirMode := os.FileMode(parent.Mode & 0o777)
	fileMode := deriveFileModeFromDirMode(dirMode)
	return &Ownership{
		UID:     &uid,
		GID:     &gid,
		Mode:    fmt.Sprintf("%o", fileMode),
		DirMode: fmt.Sprintf("%o", dirMode),
	}, nil
}

func (s *Service) effectiveOwnership(parentID FileID, ownership *Ownership) (*Ownership, error) {
	if !ownershipIsEmpty(ownership) {
		return ownership, nil
	}
	return s.inheritedOwnershipFromParent(parentID)
}

func sanitizeRelativePath(relPath string) (string, error) {
	rel := strings.TrimSpace(relPath)
	if rel == "" {
		return "", ErrInvalidArgument
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") {
		return "", ErrInvalidArgument
	}
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return "", ErrInvalidArgument
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", ErrInvalidArgument
		}
	}
	return rel, nil
}

func (s *Service) bootstrapMounts() error {
	for _, name := range s.mountNames {
		basePath := s.mountByName[name]
		id, err := s.store.GetID(basePath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			id, err = newID()
			if err != nil {
				return err
			}
			if err := s.store.SetID(basePath, id); err != nil {
				return err
			}
		}

		s.mountIDByName[name] = id
		s.mountNameByID[id] = name

		entity := Entity{ID: id, ParentID: FileID{}, Name: name, IsDir: true}
		if err := s.idx.Batch(func(b Batch) error {
			b.PutEntity(entity)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListRoot() []MountEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]MountEntry, 0, len(s.mountNames))
	for _, name := range s.mountNames {
		out = append(out, MountEntry{Name: name, ID: s.mountIDByName[name], Path: "/" + name})
	}
	return out
}

func (s *Service) ResolvePath(virtualPath string) (FileID, error) {
	vp, parts, err := normalizeVirtualPathInput(virtualPath)
	if err != nil {
		return FileID{}, err
	}
	return s.resolvePathID(vp, parts)
}

func normalizeVirtualPathInput(virtualPath string) (string, []string, error) {
	raw := strings.TrimSpace(virtualPath)
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return "", nil, ErrInvalidArgument
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", nil, ErrInvalidArgument
		}
	}

	vp := path.Clean("/" + raw)
	vp = strings.TrimPrefix(vp, "/")
	if vp == "" || vp == "." {
		return "", nil, ErrInvalidArgument
	}
	return vp, strings.Split(vp, "/"), nil
}

func (s *Service) resolvePathID(vp string, parts []string) (FileID, error) {
	if cached, ok := s.cache.Get(vp); ok {
		s.idPathCache.Add(cached.ID, "/"+vp)
		return cached.ID, nil
	}

	s.mu.RLock()
	cur, ok := s.mountIDByName[parts[0]]
	s.mu.RUnlock()
	if !ok {
		return FileID{}, ErrNotFound
	}

	for i := 1; i < len(parts); i++ {
		child, err := s.idx.LookupChild(cur, parts[i])
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return FileID{}, ErrNotFound
			}
			return FileID{}, err
		}
		cur = child.ID
	}

	s.cache.Add(vp, pathCacheEntry{ID: cur})
	s.idPathCache.Add(cur, "/"+vp)
	return cur, nil
}

func isWithinBase(realPath, basePath string) bool {
	return realPath == basePath || strings.HasPrefix(realPath, basePath+string(os.PathSeparator))
}

func safeResolvedPath(candidatePath, basePath string) (string, error) {
	realPath, err := filepath.EvalSymlinks(candidatePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			parentReal, parentErr := filepath.EvalSymlinks(filepath.Dir(candidatePath))
			if parentErr != nil {
				if errors.Is(parentErr, os.ErrNotExist) {
					return "", ErrNotFound
				}
				return "", parentErr
			}
			realPath = filepath.Join(parentReal, filepath.Base(candidatePath))
		} else {
			return "", err
		}
	}
	if !isWithinBase(realPath, basePath) {
		return "", ErrForbidden
	}
	return realPath, nil
}

func (s *Service) ResolveAbsPath(id FileID) (string, error) {
	if id.IsZero() {
		return "", ErrInvalidArgument
	}

	segments := make([]string, 0, 8)
	cur := id
	for {
		e, err := s.idx.GetEntity(cur)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return "", ErrNotFound
			}
			return "", err
		}
		segments = append(segments, e.Name)
		if e.ParentID.IsZero() {
			break
		}
		cur = e.ParentID
	}

	mountName := segments[len(segments)-1]
	s.mu.RLock()
	basePath, ok := s.mountByName[mountName]
	s.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	if len(segments) == 1 {
		return basePath, nil
	}

	rel := make([]string, 0, len(segments)-1)
	for i := len(segments) - 2; i >= 0; i-- {
		rel = append(rel, segments[i])
	}
	candidate := filepath.Join(append([]string{basePath}, rel...)...)
	return safeResolvedPath(candidate, basePath)
}

func (s *Service) VirtualPath(id FileID) (string, error) {
	if cached, ok := s.idPathCache.Get(id); ok {
		return cached, nil
	}

	segments := make([]string, 0, 8)
	cur := id
	for {
		e, err := s.idx.GetEntity(cur)
		if err != nil {
			return "", err
		}
		segments = append(segments, e.Name)
		if e.ParentID.IsZero() {
			break
		}
		cur = e.ParentID
	}

	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	vp := "/" + strings.Join(segments, "/")
	s.idPathCache.Add(id, vp)
	return vp, nil
}

func fileOwnership(info os.FileInfo) (uid uint32, gid uint32, mode uint32) {
	mode = uint32(info.Mode().Perm())
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, mode
	}
	return st.Uid, st.Gid, mode
}

// fileInodeIdentity extracts (device, inode, nlink) from a stat result. On
// platforms or filesystems where Sys() doesn't yield a *syscall.Stat_t the
// returned tuple is all zero — Inode reconciliation treats zero as "unknown"
// and skips, so this is the safe default for cross-platform builds.
func fileInodeIdentity(info os.FileInfo) (device, inode uint64, nlink uint32) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino), uint32(st.Nlink)
}

func buildEntityMetadata(id, parentID FileID, name, absPath string, info os.FileInfo) Entity {
	uid, gid, mode := fileOwnership(info)
	device, inode, nlink := fileInodeIdentity(info)
	exif := map[string]string{}
	mimeType := ""
	if !info.IsDir() {
		mimeType = detectMimeType(name)
		exif = readEXIF(absPath, mimeType, name)
	}
	return Entity{
		ID:       id,
		ParentID: parentID,
		Name:     name,
		IsDir:    info.IsDir(),
		Size:     info.Size(),
		Mtime:    info.ModTime().UnixMilli(),
		UID:      uid,
		GID:      gid,
		Mode:     mode,
		Device:   device,
		Inode:    inode,
		Nlink:    nlink,
		MimeType: mimeType,
		Exif:     exif,
	}
}

func fileMetaFromEntity(entity *Entity, vp string) *FileMeta {
	meta := &FileMeta{
		ID:       entity.ID,
		Type:     "file",
		Name:     entity.Name,
		Path:     vp,
		Size:     entity.Size,
		Mtime:    entity.Mtime,
		UID:      entity.UID,
		GID:      entity.GID,
		Mode:     entity.Mode,
		MimeType: entity.MimeType,
		Exif:     entity.Exif,
		IsRoot:   entity.ParentID.IsZero(),
	}
	if entity.IsDir {
		meta.Type = "directory"
		meta.MimeType = ""
		meta.Exif = nil
	} else if meta.MimeType == "" {
		meta.MimeType = "application/octet-stream"
	}
	return meta
}

func (s *Service) GetFileByVirtualPath(virtualPath string) (*FileMeta, error) {
	vp, parts, err := normalizeVirtualPathInput(virtualPath)
	if err != nil {
		return nil, err
	}
	id, err := s.resolvePathID(vp, parts)
	if err != nil {
		return nil, err
	}
	entity, err := s.idx.GetEntity(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return fileMetaFromEntity(entity, "/"+vp), nil
}

func (s *Service) GetFile(id FileID) (*FileMeta, error) {
	entity, err := s.idx.GetEntity(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	vp, err := s.VirtualPath(id)
	if err != nil {
		return nil, err
	}
	return fileMetaFromEntity(entity, vp), nil
}

func (s *Service) ensureIndexed(absPath string) (FileID, error) {
	id, err := s.store.GetID(absPath)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return FileID{}, err
	}
	if err := s.syncSingle(absPath); err != nil {
		return FileID{}, err
	}
	return s.store.GetID(absPath)
}

func (s *Service) computeDirectorySizeByIDBudget(dirID FileID, remainingNodes *int, deadline time.Time) (int64, bool) {
	if remainingNodes == nil || *remainingNodes <= 0 {
		return 0, false
	}
	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, false
	}

	var total int64
	stack := []FileID{dirID}
	seen := map[FileID]struct{}{}
	for len(stack) > 0 {
		if remainingNodes != nil {
			if *remainingNodes <= 0 {
				return 0, false
			}
			*remainingNodes--
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return 0, false
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, exists := seen[cur]; exists {
			continue
		}
		seen[cur] = struct{}{}
		children, err := s.listAllChildren(cur)
		if err != nil {
			continue
		}
		for _, child := range children {
			if child.IsDir {
				stack = append(stack, child.ID)
				continue
			}
			entity, err := s.idx.GetEntity(child.ID)
			if err != nil {
				continue
			}
			total += entity.Size
		}
	}
	return total, true
}

func (s *Service) ListNodeChildren(parentID FileID, cursor string, pageSize int, computeRecursiveSizes bool) (*ListedNodes, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}

	meta, err := s.GetFile(parentID)
	if err != nil {
		return nil, err
	}
	if meta.Type != "directory" {
		return nil, ErrInvalidArgument
	}
	if cursor != "" {
		if _, err := s.idx.LookupChild(parentID, cursor); err != nil {
			return nil, ErrInvalidArgument
		}
	}

	entries, err := s.idx.ListChildren(parentID, cursor, pageSize+1)
	if err != nil {
		return nil, err
	}
	hasMore := len(entries) > pageSize
	if hasMore {
		entries = entries[:pageSize]
	}

	items := make([]FileMeta, 0, pageSize)
	remainingNodes := 300_000
	deadline := time.Now().Add(2 * time.Second)
	for _, entry := range entries {
		childMeta, err := s.GetFile(entry.ID)
		if err != nil {
			continue
		}
		if childMeta.Type == "directory" && computeRecursiveSizes {
			if size, ok := s.computeDirectorySizeByIDBudget(entry.ID, &remainingNodes, deadline); ok {
				childMeta.Size = size
			}
		}
		items = append(items, *childMeta)
	}

	listed := &ListedNodes{Items: items}
	if hasMore && len(entries) > 0 {
		listed.NextCursor = entries[len(entries)-1].Name
	}
	return listed, nil
}

func splitEvenly(limit, n int) []int {
	if n <= 0 {
		return nil
	}
	if limit < 0 {
		limit = 0
	}
	out := make([]int, n)
	base := limit / n
	rest := limit % n
	for i := range n {
		out[i] = base
		if i < rest {
			out[i]++
		}
	}
	return out
}

func hasHiddenSegment(relPath string) bool {
	for _, seg := range strings.Split(relPath, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func sanitizeVirtualPath(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", ErrInvalidArgument
	}
	v = strings.TrimPrefix(v, "/")
	return sanitizeRelativePath(v)
}

func globErrorCause(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "not found"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrInvalidArgument):
		return "invalid path"
	default:
		return err.Error()
	}
}

func (s *Service) SearchGlob(req GlobSearchRequest) (*GlobSearchResponse, error) {
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return nil, ErrInvalidArgument
	}
	if _, err := doublestar.Match(pattern, ""); err != nil {
		return nil, ErrInvalidArgument
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 5000 {
		limit = 5000
	}

	includeFiles := req.IncludeFiles
	includeDirs := req.IncludeDirs
	if !includeFiles && !includeDirs {
		includeFiles = true
	}

	response := &GlobSearchResponse{
		Results: make([]FileMeta, 0, limit),
		Errors:  make([]GlobSearchError, 0),
		Paths:   make([]GlobSearchPathResult, 0),
	}

	s.mu.RLock()
	defaultMounts := append([]string(nil), s.mountNames...)
	s.mu.RUnlock()

	requestedPaths := uniqueStrings(req.Paths)
	if len(requestedPaths) == 0 {
		for _, mount := range defaultMounts {
			requestedPaths = append(requestedPaths, "/"+mount)
		}
	}
	if len(requestedPaths) == 0 || limit == 0 {
		return response, nil
	}

	type searchTarget struct {
		requestedPath string
		virtualPath   string
		absPath       string
	}
	targets := make([]searchTarget, 0, len(requestedPaths))
	seenVirtualPaths := make(map[string]struct{}, len(requestedPaths))
	for _, requestedPath := range requestedPaths {
		virtualPath, err := sanitizeVirtualPath(requestedPath)
		if err != nil {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  requestedPath,
				Cause: "invalid path",
			})
			continue
		}
		id, err := s.ResolvePath(virtualPath)
		if err != nil {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  requestedPath,
				Cause: globErrorCause(err),
			})
			continue
		}
		meta, err := s.GetFile(id)
		if err != nil {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  requestedPath,
				Cause: globErrorCause(err),
			})
			continue
		}
		if meta.Type != "directory" {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  requestedPath,
				Cause: "path is not a directory",
			})
			continue
		}
		absPath, err := s.ResolveAbsPath(id)
		if err != nil {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  requestedPath,
				Cause: globErrorCause(err),
			})
			continue
		}
		virtualCanonical, err := s.VirtualPath(id)
		if err != nil {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  requestedPath,
				Cause: globErrorCause(err),
			})
			continue
		}
		if _, exists := seenVirtualPaths[virtualCanonical]; exists {
			continue
		}
		seenVirtualPaths[virtualCanonical] = struct{}{}
		targets = append(targets, searchTarget{
			requestedPath: requestedPath,
			virtualPath:   virtualCanonical,
			absPath:       absPath,
		})
	}
	if len(targets) == 0 {
		return response, nil
	}

	quotas := splitEvenly(limit, len(targets))
	type rootResult struct {
		items    []FileMeta
		err      string
		pathInfo GlobSearchPathResult
	}
	out := make([]rootResult, len(targets))

	var wg sync.WaitGroup
	for i := range targets {
		target := targets[i]
		quota := quotas[i]
		if quota <= 0 {
			out[i].pathInfo = GlobSearchPathResult{
				Path:     target.virtualPath,
				Returned: 0,
				HasMore:  true,
			}
			continue
		}

		wg.Add(1)
		go func(idx int, virtualPath, rootPath string, maxItems int) {
			defer wg.Done()

			if _, err := os.Stat(rootPath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					out[idx].err = "path does not exist"
					return
				}
				out[idx].err = err.Error()
				return
			}

			items := make([]FileMeta, 0, maxItems)
			hasMore := false
			walkErr := filepath.WalkDir(rootPath, func(current string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if current == rootPath {
					return nil
				}

				rel, err := filepath.Rel(rootPath, current)
				if err != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)

				if !req.ShowHidden && hasHiddenSegment(rel) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}

				match, err := doublestar.Match(pattern, rel)
				if err != nil || !match {
					return nil
				}

				if d.IsDir() && !includeDirs {
					return nil
				}
				if !d.IsDir() && !includeFiles {
					return nil
				}
				if len(items) >= maxItems {
					hasMore = true
					return io.EOF
				}

				id, err := s.ensureIndexed(current)
				if err != nil {
					return nil
				}
				meta, err := s.GetFile(id)
				if err != nil {
					return nil
				}
				items = append(items, *meta)
				return nil
			})
			if walkErr != nil && !errors.Is(walkErr, io.EOF) {
				out[idx].err = walkErr.Error()
			}
			out[idx].pathInfo = GlobSearchPathResult{
				Path:     virtualPath,
				Returned: len(items),
				HasMore:  hasMore,
			}
			out[idx].items = items
		}(i, target.virtualPath, target.absPath, quota)
	}
	wg.Wait()

	for i := range targets {
		if out[i].pathInfo.Path == "" {
			out[i].pathInfo = GlobSearchPathResult{
				Path:     targets[i].virtualPath,
				Returned: len(out[i].items),
				HasMore:  false,
			}
		}
		response.Paths = append(response.Paths, out[i].pathInfo)

		if out[i].err != "" {
			response.Errors = append(response.Errors, GlobSearchError{
				Path:  targets[i].requestedPath,
				Cause: out[i].err,
			})
			continue
		}
		response.Results = append(response.Results, out[i].items...)
	}

	return response, nil
}

func (s *Service) OpenContent(id FileID) (io.ReadCloser, int64, bool, error) {
	abs, err := s.ResolveAbsPath(id)
	if err != nil {
		return nil, 0, false, err
	}
	info, err := s.store.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, false, ErrNotFound
		}
		return nil, 0, false, err
	}
	if info.IsDir() {
		return nil, 0, true, nil
	}
	r, err := s.store.OpenRead(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, false, ErrNotFound
		}
		return nil, 0, false, err
	}
	return r, info.Size(), false, nil
}

func (s *Service) WriteContent(id FileID, body io.Reader) error {
	meta, err := s.GetFile(id)
	if err != nil {
		return err
	}
	if meta.Type != "file" {
		return ErrInvalidArgument
	}

	abs, err := s.ResolveAbsPath(id)
	if err != nil {
		return err
	}

	preserveID := meta.ID
	if err := s.writeFileAtomic(abs, body, os.FileMode(meta.Mode), ownershipFromFileMeta(meta), &preserveID, false); err != nil {
		return err
	}
	return s.syncSingle(abs)
}

// ReplaceFile places the file at srcPath under parentID/name, honoring the
// supplied ConflictMode. The returned FileMeta reflects the actually-used
// final name, which may differ from `name` when mode is ConflictRename.
func (s *Service) ReplaceFile(parentID FileID, name string, srcPath string, ownership *Ownership, mode ConflictMode) (*FileMeta, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") {
		return nil, ErrInvalidArgument
	}
	parentMeta, err := s.GetFile(parentID)
	if err != nil {
		return nil, err
	}
	if parentMeta.Type != "directory" {
		return nil, ErrInvalidArgument
	}

	parentAbs, err := s.ResolveAbsPath(parentID)
	if err != nil {
		return nil, err
	}
	targetPath := filepath.Join(parentAbs, name)

	// Resolve the conflict before we touch anything else. We want to fail
	// fast and preserve the existing file untouched if the mode demands it.
	if info, statErr := s.store.Stat(targetPath); statErr == nil {
		if info.IsDir() {
			// A file upload cannot replace a directory regardless of mode —
			// silently nuking a subtree from a single PUT is too dangerous
			// and not what `overwrite` is supposed to mean here.
			return nil, ErrConflict
		}
		switch mode {
		case ConflictError, "":
			return nil, ErrConflict
		case ConflictRename:
			targetPath = makeUniquePath(targetPath)
		case ConflictOverwrite:
			// fall through — the existing replace path below handles it.
		default:
			return nil, ErrInvalidArgument
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	existingID, err := s.store.GetID(targetPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sourceID, err := s.store.GetID(srcPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	fallbackCopy := false

	// On Linux, rename(2) is an atomic replace when the target exists and
	// is a regular file. Doing a Remove+Rename creates a TOCTOU window
	// where readers see "no such file" between the two syscalls AND a
	// crash between the two leaves the target permanently gone. Rely on
	// rename's atomic replace; reserve the copy fallback for cross-device
	// failures only.
	if err := s.store.Rename(srcPath, targetPath); err != nil {
		fallbackCopy = true
		src, openErr := s.store.OpenRead(srcPath)
		if openErr != nil {
			return nil, openErr
		}
		defer src.Close()

		dst, createErr := s.store.OpenWrite(targetPath, 0o644)
		if createErr != nil {
			return nil, createErr
		}
		if _, copyErr := io.Copy(dst, src); copyErr != nil {
			_ = dst.Close()
			return nil, copyErr
		}
		if closeErr := dst.Close(); closeErr != nil {
			return nil, closeErr
		}
		if err := syncFilePath(targetPath); err != nil {
			return nil, err
		}
		_ = s.store.Remove(srcPath)
	}

	if !existingID.IsZero() {
		if err := s.store.SetID(targetPath, existingID); err != nil {
			return nil, err
		}
	} else if fallbackCopy && !sourceID.IsZero() {
		if err := s.store.SetID(targetPath, sourceID); err != nil {
			return nil, err
		}
	}
	effectiveOwnership, err := s.effectiveOwnership(parentID, ownership)
	if err != nil {
		return nil, err
	}
	if err := s.applyOwnership(targetPath, effectiveOwnership, false); err != nil {
		return nil, err
	}
	if err := s.syncSingle(targetPath); err != nil {
		return nil, err
	}
	id, err := s.store.GetID(targetPath)
	if err != nil {
		return nil, err
	}
	return s.GetFile(id)
}

func applyOwnershipOne(path string, isDir bool, normalized *normalizedOwnership) error {
	if normalized.uid != nil && normalized.gid != nil {
		if err := os.Chown(path, *normalized.uid, *normalized.gid); err != nil {
			if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
				return err
			}
			info, statErr := os.Stat(path)
			if statErr != nil {
				return err
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(st.Uid) != *normalized.uid || int(st.Gid) != *normalized.gid {
				return err
			}
		}
	}
	if isDir {
		if normalized.dirMode != nil {
			if err := os.Chmod(path, *normalized.dirMode); err != nil {
				return err
			}
		}
		return nil
	}
	if normalized.mode != nil {
		if err := os.Chmod(path, *normalized.mode); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyOwnership(absPath string, ownership *Ownership, recursive bool) error {
	normalized, err := normalizeOwnership(ownership)
	if err != nil {
		return err
	}
	if normalized == nil {
		return nil
	}

	if !recursive {
		info, err := os.Stat(absPath)
		if err != nil {
			return err
		}
		return applyOwnershipOne(absPath, info.IsDir(), normalized)
	}

	return filepath.WalkDir(absPath, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Never follow symlinks during recursive ownership changes.
			return nil
		}
		isDir := d.IsDir()
		return applyOwnershipOne(current, isDir, normalized)
	})
}

func (s *Service) isMountRoot(id FileID) bool {
	e, err := s.idx.GetEntity(id)
	if err != nil {
		return false
	}
	return e.ParentID.IsZero()
}

// MkdirRelative creates relPath under parentID. The leaf segment respects
// the supplied ConflictMode (error/skip/rename). Intermediate segments are
// always treated as ConflictSkip — an existing directory in the middle of
// the path is reused, otherwise mkdir -p style traversal would be
// impossible.
//
// Allowed modes: ConflictError, ConflictSkip, ConflictRename. Any other
// mode (notably ConflictOverwrite — which would mean recursive subtree
// deletion) returns ErrInvalidArgument; use Transfer with overwrite for
// that.
func (s *Service) MkdirRelative(parentID FileID, relPath string, recursive bool, ownership *Ownership, mode ConflictMode) (*FileMeta, error) {
	switch mode {
	case "":
		mode = ConflictError
	case ConflictError, ConflictSkip, ConflictRename:
		// allowed
	default:
		return nil, ErrInvalidArgument
	}

	parentMeta, err := s.GetFile(parentID)
	if err != nil {
		return nil, err
	}
	if parentMeta.Type != "directory" {
		return nil, ErrInvalidArgument
	}
	rel, err := sanitizeRelativePath(relPath)
	if err != nil {
		return nil, err
	}

	effectiveOwnership, err := s.effectiveOwnership(parentID, ownership)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeOwnership(effectiveOwnership)
	if err != nil {
		return nil, err
	}

	parentAbs, err := s.ResolveAbsPath(parentID)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, "/")
	current := parentAbs
	createdAny := false
	firstCreated := ""
	for i, seg := range parts {
		isLeaf := i == len(parts)-1
		next := filepath.Join(current, seg)
		info, lstatErr := os.Lstat(next)
		if lstatErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, ErrForbidden
			}
			// rename at the leaf always wins: even if the existing entry is
			// a file (type mismatch), we just produce a unique sibling name
			// and create our directory there.
			if isLeaf && mode == ConflictRename {
				next = makeUniquePath(next)
			} else if !info.IsDir() {
				// Existing file blocks any non-rename mode at any segment:
				// no mode can sensibly turn a file into a directory.
				return nil, ErrConflict
			} else if !isLeaf {
				// Intermediate dir: always reuse — otherwise mkdir -p fails.
				current = next
				continue
			} else {
				// Leaf is an existing dir; user's mode decides.
				switch mode {
				case ConflictError:
					return nil, ErrConflict
				case ConflictSkip:
					current = next
					continue
				}
			}
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return nil, lstatErr
		}

		if !recursive && i < len(parts)-1 {
			return nil, ErrNotFound
		}

		dirPerm := os.FileMode(0o755)
		if normalized != nil && normalized.dirMode != nil {
			dirPerm = *normalized.dirMode
		}
		if err := s.store.MkdirAll(next, dirPerm); err != nil {
			return nil, err
		}
		createdAny = true
		if firstCreated == "" {
			firstCreated = next
		}
		current = next
	}

	targetAbs := current
	if createdAny {
		// Apply ownership to the full newly-created subtree, including intermediate dirs.
		if err := s.applyOwnership(firstCreated, effectiveOwnership, true); err != nil {
			return nil, err
		}
		if err := s.syncSingle(targetAbs); err != nil {
			return nil, err
		}
	}

	id, err := s.ensureIndexed(targetAbs)
	if err != nil {
		return nil, err
	}
	return s.GetFile(id)
}

// WriteContentByVirtualPath writes body to the file at virtualPath. The
// returned FileMeta reflects the actually-written name, which may differ
// from the requested one when mode is ConflictRename. The bool result is
// true when a new file was created (false when an existing one was
// replaced).
func (s *Service) WriteContentByVirtualPath(virtualPath string, body io.Reader, mode ConflictMode) (*FileMeta, bool, error) {
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

	parentID := mountID
	if len(parts) > 2 {
		parentPath := strings.Join(parts[1:len(parts)-1], "/")
		// Parent path must always be skip-on-existing-dir, otherwise every
		// PUT to data/foo/bar.txt would 409 the second time. The user's
		// onConflict only governs the leaf file.
		parentMeta, err := s.MkdirRelative(mountID, parentPath, true, nil, ConflictSkip)
		if err != nil {
			return nil, false, err
		}
		parentID = parentMeta.ID
	}

	targetID, err := s.ResolvePath(vp)
	if err == nil {
		targetMeta, getErr := s.GetFile(targetID)
		if getErr != nil {
			return nil, false, getErr
		}
		// Rename always succeeds: pick a unique sibling name regardless of
		// whether the existing target is a file or a directory. The new
		// upload becomes its own file with the unique name.
		if mode == ConflictRename {
			parentAbs, resolveErr := s.ResolveAbsPath(parentID)
			if resolveErr != nil {
				return nil, false, resolveErr
			}
			fileName = filepath.Base(makeUniquePath(filepath.Join(parentAbs, fileName)))
		} else if targetMeta.Type != "file" {
			// For error/overwrite, refuse to act on a non-file target
			// (overwriting a directory subtree from a single PUT is too
			// dangerous; that's what Transfer with overwrite is for).
			return nil, false, ErrConflict
		} else {
			switch mode {
			case ConflictError, "":
				return nil, false, ErrConflict
			case ConflictOverwrite:
				if writeErr := s.WriteContent(targetID, body); writeErr != nil {
					return nil, false, writeErr
				}
				updated, getErr := s.GetFile(targetID)
				if getErr != nil {
					return nil, false, getErr
				}
				return updated, false, nil
			default:
				return nil, false, ErrInvalidArgument
			}
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}

	updated, err := s.createAndWriteContent(parentID, fileName, body, nil)
	if err != nil {
		return nil, false, err
	}
	return updated, true, nil
}

func (s *Service) CreateChild(parentID FileID, name string, isDir bool, ownership *Ownership) (*FileMeta, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") {
		return nil, ErrInvalidArgument
	}
	parentMeta, err := s.GetFile(parentID)
	if err != nil {
		return nil, err
	}
	if parentMeta.Type != "directory" {
		return nil, ErrInvalidArgument
	}

	parentAbs, err := s.ResolveAbsPath(parentID)
	if err != nil {
		return nil, err
	}
	abs := filepath.Join(parentAbs, name)
	if _, err := s.store.Stat(abs); err == nil {
		return nil, ErrConflict
	}
	effectiveOwnership, err := s.effectiveOwnership(parentID, ownership)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeOwnership(effectiveOwnership)
	if err != nil {
		return nil, err
	}

	filePerm := os.FileMode(0o644)
	dirPerm := os.FileMode(0o755)
	if normalized != nil {
		if normalized.mode != nil {
			filePerm = *normalized.mode
		}
		if normalized.dirMode != nil {
			dirPerm = *normalized.dirMode
		}
	}

	if isDir {
		if err := s.store.MkdirAll(abs, dirPerm); err != nil {
			return nil, err
		}
	} else {
		w, err := s.store.OpenWrite(abs, filePerm)
		if err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	}

	if err := s.applyOwnership(abs, effectiveOwnership, isDir); err != nil {
		return nil, err
	}
	if err := s.syncSingle(abs); err != nil {
		return nil, err
	}
	id, err := s.store.GetID(abs)
	if err != nil {
		return nil, err
	}
	return s.GetFile(id)
}

func (s *Service) UpdateNode(id FileID, name *string, ownership *Ownership, recursiveOwnership bool) (*FileMeta, error) {
	if name == nil && ownership == nil {
		return nil, ErrInvalidArgument
	}

	entity, err := s.idx.GetEntity(id)
	if err != nil {
		return nil, err
	}

	abs, err := s.ResolveAbsPath(id)
	if err != nil {
		return nil, err
	}
	oldCachePath := ""
	if vp, err := s.VirtualPath(id); err == nil {
		oldCachePath = normalizeCacheKey(vp)
	}

	if name != nil {
		newName := strings.TrimSpace(*name)
		if newName == "" || strings.Contains(newName, "/") {
			return nil, ErrInvalidArgument
		}
		if entity.ParentID.IsZero() {
			return nil, ErrForbidden
		}

		if newName != entity.Name {
			parentAbs, err := s.ResolveAbsPath(entity.ParentID)
			if err != nil {
				return nil, err
			}
			targetAbs := filepath.Join(parentAbs, newName)
			if _, err := s.store.Stat(targetAbs); err == nil {
				return nil, ErrConflict
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}

			if err := s.store.Rename(abs, targetAbs); err != nil {
				return nil, err
			}
			abs = targetAbs

			info, err := s.store.Stat(abs)
			if err != nil {
				return nil, err
			}
			oldName := entity.Name
			entity.Name = newName
			if err := s.idx.Batch(func(b Batch) error {
				b.DelChild(entity.ParentID, oldName)
				b.PutEntity(*entity)
				b.PutChild(entity.ParentID, newName, DirEntry{
					ID:    id,
					Name:  newName,
					IsDir: info.IsDir(),
					Size:  info.Size(),
					Mtime: info.ModTime().UnixMilli(),
				})
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	if err := s.applyOwnership(abs, ownership, recursiveOwnership); err != nil {
		return nil, err
	}
	if recursiveOwnership && entity.IsDir {
		if entity.ParentID.IsZero() {
			if err := s.RescanMount(abs); err != nil {
				return nil, err
			}
		} else {
			if err := s.syncSubtree(abs); err != nil {
				return nil, err
			}
		}
	} else {
		// syncSingle is not suitable for mount roots because it tries to index the parent.
		if !entity.ParentID.IsZero() {
			if err := s.syncSingle(abs); err != nil {
				return nil, err
			}
		} else {
			if err := s.RescanMount(abs); err != nil {
				return nil, err
			}
		}
	}

	if oldCachePath != "" {
		s.invalidateCachePrefix(oldCachePath)
		if parent := parentCacheKey(oldCachePath); parent != "" {
			s.cache.Remove(parent)
		}
	}
	s.invalidateCacheByID(id)
	return s.GetFile(id)
}

func makeUniquePath(target string) string {
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; i <= 999; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%02d%s", stem, i, ext))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixMilli(), ext))
}

func (s *Service) copyPath(sourceAbs, targetAbs string, preserveIDs bool) error {
	linfo, err := os.Lstat(sourceAbs)
	if err != nil {
		return err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return ErrForbidden
	}

	info, err := s.store.Stat(sourceAbs)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err := s.store.MkdirAll(targetAbs, info.Mode().Perm()); err != nil {
			return err
		}
		if preserveIDs {
			if id, err := s.store.GetID(sourceAbs); err == nil {
				if err := s.store.SetID(targetAbs, id); err != nil {
					return err
				}
			}
		}
		entries, err := s.store.ReadDir(sourceAbs)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := s.copyPath(filepath.Join(sourceAbs, entry.Name()), filepath.Join(targetAbs, entry.Name()), preserveIDs); err != nil {
				return err
			}
		}
		return nil
	}

	r, err := s.store.OpenRead(sourceAbs)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := s.store.OpenWrite(targetAbs, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.Chmod(targetAbs, info.Mode().Perm()); err != nil {
		return err
	}
	if preserveIDs {
		if id, err := s.store.GetID(sourceAbs); err == nil {
			if err := s.store.SetID(targetAbs, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Transfer(req TransferRequest) (*FileMeta, error) {
	req.Op = strings.ToLower(strings.TrimSpace(req.Op))
	if req.Op != "move" && req.Op != "copy" {
		return nil, ErrInvalidArgument
	}
	if strings.TrimSpace(req.TargetName) == "" || strings.Contains(req.TargetName, "/") {
		return nil, ErrInvalidArgument
	}
	// Caller (HTTP adapter) must pass a parsed/validated ConflictMode.
	// Defensively normalize an empty value to the default to avoid an
	// uncovered code path if a non-HTTP caller forgot to.
	if req.OnConflict == "" {
		req.OnConflict = ConflictError
	}
	switch req.OnConflict {
	case ConflictError, ConflictOverwrite, ConflictRename:
		// allowed for transfer
	default:
		return nil, ErrInvalidArgument
	}
	recursiveOwnership := true
	if req.RecursiveOwnership != nil {
		recursiveOwnership = *req.RecursiveOwnership
	}

	sourceMeta, err := s.GetFile(req.SourceID)
	if err != nil {
		return nil, err
	}
	if sourceMeta.IsRoot {
		return nil, ErrForbidden
	}
	parentMeta, err := s.GetFile(req.TargetParentID)
	if err != nil {
		return nil, err
	}
	if parentMeta.Type != "directory" {
		return nil, ErrInvalidArgument
	}
	effectiveOwnership, err := s.effectiveOwnership(req.TargetParentID, req.Ownership)
	if err != nil {
		return nil, err
	}

	sourceAbs, err := s.ResolveAbsPath(req.SourceID)
	if err != nil {
		return nil, err
	}
	targetParentAbs, err := s.ResolveAbsPath(req.TargetParentID)
	if err != nil {
		return nil, err
	}
	if req.Op == "move" && sourceMeta.Type == "directory" {
		rel, relErr := filepath.Rel(sourceAbs, targetParentAbs)
		if relErr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return nil, ErrInvalidArgument
		}
	}

	targetAbs := filepath.Join(targetParentAbs, req.TargetName)
	finalTargetName := req.TargetName
	if _, err := s.store.Stat(targetAbs); err == nil {
		switch req.OnConflict {
		case ConflictOverwrite:
			if existing, lookupErr := s.idx.LookupChild(req.TargetParentID, req.TargetName); lookupErr == nil {
				if err := s.deleteSubtree(existing.ID); err != nil {
					return nil, err
				}
			}
			if err := s.store.RemoveAll(targetAbs); err != nil {
				return nil, err
			}
		case ConflictRename:
			targetAbs = makeUniquePath(targetAbs)
			finalTargetName = filepath.Base(targetAbs)
		case ConflictError:
			return nil, ErrConflict
		default:
			return nil, ErrConflict
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if req.Op == "move" {
		if err := s.store.Rename(sourceAbs, targetAbs); err != nil {
			if err := s.copyPath(sourceAbs, targetAbs, true); err != nil {
				return nil, err
			}
			if err := s.store.RemoveAll(sourceAbs); err != nil {
				return nil, err
			}
		}
		if err := s.applyOwnership(targetAbs, effectiveOwnership, recursiveOwnership); err != nil {
			return nil, err
		}
		if err := s.reParentNode(req.SourceID, sourceMeta.Name, req.TargetParentID, finalTargetName); err != nil {
			return nil, err
		}
		if recursiveOwnership && sourceMeta.Type == "directory" {
			if err := s.syncSubtree(targetAbs); err != nil {
				return nil, err
			}
		}
		return s.GetFile(req.SourceID)
	}

	if err := s.copyPath(sourceAbs, targetAbs, false); err != nil {
		return nil, err
	}
	if err := s.applyOwnership(targetAbs, effectiveOwnership, recursiveOwnership); err != nil {
		return nil, err
	}
	if err := s.syncSubtree(targetAbs); err != nil {
		return nil, err
	}
	newID, err := s.store.GetID(targetAbs)
	if err != nil {
		return nil, err
	}
	return s.GetFile(newID)
}

func (s *Service) reParentNode(id FileID, oldName string, newParentID FileID, newName string) error {
	entity, err := s.idx.GetEntity(id)
	if err != nil {
		return err
	}
	oldParentID := entity.ParentID

	newParentAbs, err := s.ResolveAbsPath(newParentID)
	if err != nil {
		return err
	}
	newAbs := filepath.Join(newParentAbs, newName)
	info, err := s.store.Stat(newAbs)
	if err != nil {
		return err
	}

	updated := buildEntityMetadata(id, newParentID, newName, newAbs, info)
	if err := s.idx.Batch(func(b Batch) error {
		b.DelChild(oldParentID, oldName)
		b.PutEntity(updated)
		b.PutChild(newParentID, newName, DirEntry{
			ID:    id,
			Name:  newName,
			IsDir: updated.IsDir,
			Size:  updated.Size,
			Mtime: updated.Mtime,
		})
		return nil
	}); err != nil {
		return err
	}
	oldParentPath, _ := s.VirtualPath(oldParentID)
	newParentPath, _ := s.VirtualPath(newParentID)
	s.invalidateCacheByID(id)
	s.InvalidatePathCache(oldParentPath)
	s.InvalidatePathCache(newParentPath)
	return nil
}

func (s *Service) syncSubtree(absPath string) error {
	absPath = filepath.Clean(absPath)

	parentAbs := filepath.Dir(absPath)
	parentID, err := s.store.GetID(parentAbs)
	if err != nil {
		return err
	}

	type item struct {
		entity Entity
		entry  DirEntry
	}
	pathToID := map[string]FileID{parentAbs: parentID}
	collected := make([]item, 0, 64)

	err = filepath.WalkDir(absPath, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		parent := filepath.Dir(current)
		pid, ok := pathToID[parent]
		if !ok {
			return nil
		}

		id, err := s.store.GetID(current)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			id, err = newID()
			if err != nil {
				return err
			}
			if err := s.store.SetID(current, id); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					// File vanished between WalkDir and SetID. Skip and
					// continue the scan — losing this entry is benign, but
					// aborting the whole subtree is not.
					return nil
				}
				return err
			}
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		pathToID[current] = id
		entity := buildEntityMetadata(id, pid, d.Name(), current, info)
		collected = append(collected, item{
			entity: entity,
			entry: DirEntry{
				ID:    id,
				Name:  d.Name(),
				IsDir: d.IsDir(),
				Size:  info.Size(),
				Mtime: info.ModTime().UnixMilli(),
			},
		})
		return nil
	})
	if err != nil {
		return err
	}

	if err := s.idx.Batch(func(b Batch) error {
		for _, it := range collected {
			b.PutEntity(it.entity)
			b.PutChild(it.entity.ParentID, it.entity.Name, it.entry)
		}
		return nil
	}); err != nil {
		return err
	}
	// Publish a single bulk EventUpdated for the subtree root. Subscribers
	// only need to know "something under this path changed" — emitting one
	// event per descendant would be wasteful for big subtrees and would
	// also surprise callers who expect symmetry with the deleteSubtree
	// publish below.
	rootID, idErr := s.store.GetID(absPath)
	if idErr == nil {
		s.invalidateCacheByID(rootID)
	} else {
		s.purgePathCaches()
	}
	s.bus.Publish(Event{Type: EventUpdated, ID: rootID, Path: absPath, At: time.Now()})
	return nil
}

func (s *Service) Delete(id FileID) error {
	meta, err := s.GetFile(id)
	if err != nil {
		return err
	}
	if meta.IsRoot {
		return ErrForbidden
	}
	abs, err := s.ResolveAbsPath(id)
	if err != nil {
		return err
	}
	if err := s.store.RemoveAll(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if err := s.deleteSubtree(id); err != nil {
		return err
	}
	// EventDeleted is published by deleteSubtree itself.
	return nil
}

func (s *Service) syncSingle(absPath string) error {
	linfo, err := os.Lstat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return ErrForbidden
	}

	info, err := s.store.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}

	id, err := s.store.GetID(absPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		id, err = newID()
		if err != nil {
			return err
		}
		if err := s.store.SetID(absPath, id); err != nil {
			return err
		}
	}

	parentAbs := filepath.Dir(absPath)
	parentID, err := s.store.GetID(parentAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := s.syncSingle(parentAbs); err != nil {
				return err
			}
			parentID, err = s.store.GetID(parentAbs)
		}
		if err != nil {
			return err
		}
	}

	name := filepath.Base(absPath)
	entity := buildEntityMetadata(id, parentID, name, absPath, info)
	entry := DirEntry{
		ID:    id,
		Name:  name,
		IsDir: info.IsDir(),
		Size:  info.Size(),
		Mtime: info.ModTime().UnixMilli(),
	}

	if err := s.idx.Batch(func(b Batch) error {
		b.PutEntity(entity)
		b.PutChild(parentID, name, entry)
		return nil
	}); err != nil {
		return err
	}

	s.invalidateCacheByID(id)
	if parentVP, err := s.VirtualPath(parentID); err == nil {
		s.InvalidatePathCache(parentVP)
	}
	s.bus.Publish(Event{Type: EventUpdated, ID: id, Path: absPath, At: time.Now()})

	// Inode-based reconciliation: after this entity has settled in the
	// index, look for other entities claiming the same (device, inode).
	// External rename within the same mount produces a stale Index entry
	// at the old path that the regular sync flow has no signal to clean
	// up; reconcileByInode is that signal.
	if reconErr := s.reconcileByInode(absPath, entity.Device, entity.Inode, entity.Nlink, id); reconErr != nil {
		log.Printf("[filegate] reconcileByInode after sync of %q: %v", absPath, reconErr)
	}
	return nil
}

// reconcileByInode walks every entity that claims the given (device, inode)
// tuple and drops the ones whose stored path no longer points at this inode
// on disk. Called from syncSingle after a successful entity write to detect
// stale entries left behind by external renames.
//
// Skipped silently when:
//   - device and inode are both zero (caller had no stat info — e.g. mount
//     root entries created before the inode-tracking field landed)
//   - nlink > 1 (file is hard-linked; multiple paths legitimately share the
//     inode and removing any of them would corrupt the index)
//
// keepID is the entity that just succeeded; it is excluded from the candidate
// list. All other claimants are stat-checked: if their stored path is gone
// or now points to a different inode they are deleted via deleteSubtree.
func (s *Service) reconcileByInode(currentAbsPath string, device, inode uint64, nlink uint32, keepID FileID) error {
	if device == 0 && inode == 0 {
		return nil
	}
	if nlink > 1 {
		return nil
	}
	candidates, err := s.idx.LookupByInode(device, inode)
	if err != nil {
		return err
	}
	for _, candidateID := range candidates {
		if candidateID == keepID {
			continue
		}
		oldAbs, err := s.ResolveAbsPath(candidateID)
		if err != nil {
			// Resolve failed (e.g. dangling parent chain). The entity is
			// already wedged; let deleteSubtree clean up using the ID alone.
			if delErr := s.deleteSubtree(candidateID); delErr != nil {
				log.Printf("[filegate] reconcileByInode: delete dangling %s failed: %v", candidateID, delErr)
			}
			continue
		}
		// Skip if the stale candidate path is the one we just synced — this
		// guards against false positives if ResolveAbsPath returns the same
		// path through a cache.
		if filepath.Clean(oldAbs) == filepath.Clean(currentAbsPath) {
			continue
		}
		// Re-stat the candidate path. If it's gone or its inode doesn't
		// match the one we hold for it, the entity is stale.
		info, statErr := s.store.Stat(oldAbs)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				if delErr := s.deleteSubtree(candidateID); delErr != nil {
					log.Printf("[filegate] reconcileByInode: delete vanished %s (%s) failed: %v", candidateID, oldAbs, delErr)
				}
			}
			// Other stat errors (permission, IO): leave the entity alone
			// rather than risk false deletion.
			continue
		}
		curDev, curIno, _ := fileInodeIdentity(info)
		if curDev != device || curIno != inode {
			if delErr := s.deleteSubtree(candidateID); delErr != nil {
				log.Printf("[filegate] reconcileByInode: delete stale %s (%s, dev=%d ino=%d) failed: %v",
					candidateID, oldAbs, curDev, curIno, delErr)
			}
		}
	}
	return nil
}

func (s *Service) mountForAbsPath(absPath string) (mountName, basePath, rel string, ok bool) {
	absPath = filepath.Clean(absPath)
	s.mu.RLock()
	defer s.mu.RUnlock()

	bestLen := -1
	bestName := ""
	bestBase := ""
	for name, base := range s.mountByName {
		if !isWithinBase(absPath, base) {
			continue
		}
		if len(base) > bestLen {
			bestLen = len(base)
			bestName = name
			bestBase = base
		}
	}
	if bestLen < 0 {
		return "", "", "", false
	}
	relative, err := filepath.Rel(bestBase, absPath)
	if err != nil {
		return "", "", "", false
	}
	if relative == "." {
		relative = ""
	}
	return bestName, bestBase, relative, true
}

func (s *Service) virtualPathFromAbs(absPath string) (string, error) {
	mountName, _, rel, ok := s.mountForAbsPath(absPath)
	if !ok {
		return "", ErrForbidden
	}
	vp := "/" + mountName
	if rel != "" {
		vp += "/" + filepath.ToSlash(rel)
	}
	return vp, nil
}

func (s *Service) SyncAbsPath(absPath string) error {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ErrInvalidArgument
	}
	vp, err := s.virtualPathFromAbs(absPath)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(vp, "/"), "/")
	if len(parts) <= 1 {
		return nil
	}
	return s.syncSingle(absPath)
}

func (s *Service) RemoveAbsPath(absPath string) error {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ErrInvalidArgument
	}
	vp, err := s.virtualPathFromAbs(absPath)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return nil
		}
		return err
	}
	id, err := s.ResolvePath(vp)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if s.isMountRoot(id) {
		return ErrForbidden
	}
	if err := s.deleteSubtree(id); err != nil {
		return err
	}
	// EventDeleted is published by deleteSubtree itself.
	return nil
}

func (s *Service) listAllChildren(parentID FileID) ([]DirEntry, error) {
	cursor := ""
	out := make([]DirEntry, 0, 32)
	for {
		chunk, err := s.idx.ListChildren(parentID, cursor, 1000)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		out = append(out, chunk...)
		if len(chunk) < 1000 {
			break
		}
		cursor = chunk[len(chunk)-1].Name
	}
	return out, nil
}

func (s *Service) deleteSubtree(rootID FileID) error {
	rootEntity, err := s.idx.GetEntity(rootID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	// Capture the absolute path before we tear down the index entry, so the
	// EventDeleted we publish below carries a meaningful Path field.
	rootAbsPath, _ := s.ResolveAbsPath(rootID)
	_ = rootEntity // value retained for the path lookup above

	stack := []FileID{rootID}
	order := make([]Entity, 0, 64)
	seen := make(map[FileID]struct{}, 64)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, exists := seen[cur]; exists {
			continue
		}
		seen[cur] = struct{}{}

		entity, err := s.idx.GetEntity(cur)
		if err != nil {
			continue
		}
		order = append(order, *entity)
		if !entity.IsDir {
			continue
		}
		children, err := s.listAllChildren(cur)
		if err != nil {
			return err
		}
		for _, child := range children {
			stack = append(stack, child.ID)
		}
	}

	if len(order) == 0 {
		return nil
	}
	rootPath := ""
	if vp, err := s.VirtualPath(rootID); err == nil {
		rootPath = normalizeCacheKey(vp)
	}
	if err := s.idx.Batch(func(b Batch) error {
		for i := len(order) - 1; i >= 0; i-- {
			e := order[i]
			if !e.ParentID.IsZero() {
				b.DelChild(e.ParentID, e.Name)
			}
			b.DelEntity(e.ID)
		}
		return nil
	}); err != nil {
		return err
	}

	if rootPath != "" {
		s.invalidateCachePrefix(rootPath)
		if parent := parentCacheKey(rootPath); parent != "" {
			s.cache.Remove(parent)
		}
	} else {
		s.purgePathCaches()
	}
	// Single bulk EventDeleted for the subtree root. Callers (Delete,
	// RemoveAbsPath, Transfer overwrite) used to publish this themselves
	// — that's now centralised here so no caller can forget.
	s.bus.Publish(Event{Type: EventDeleted, ID: rootID, Path: rootAbsPath, At: time.Now()})
	return nil
}

func (s *Service) Rescan() error {
	s.rescanMu.Lock()
	defer s.rescanMu.Unlock()
	return s.rescanWithScope(nil)
}

func (s *Service) RescanMount(absPath string) error {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ErrInvalidArgument
	}
	mountName, _, _, ok := s.mountForAbsPath(absPath)
	if !ok {
		return nil
	}
	target := map[string]struct{}{mountName: {}}
	s.rescanMu.Lock()
	defer s.rescanMu.Unlock()
	return s.rescanWithScope(target)
}

func (s *Service) resolveRootID(id FileID, memo map[FileID]FileID) (FileID, bool, error) {
	var zero FileID
	if id.IsZero() {
		return zero, false, nil
	}
	if memo == nil {
		memo = make(map[FileID]FileID, 64)
	}
	if rootID, ok := memo[id]; ok {
		return rootID, !rootID.IsZero(), nil
	}

	current := id
	chain := make([]FileID, 0, 8)
	seen := make(map[FileID]struct{}, 8)
	for {
		if rootID, ok := memo[current]; ok {
			for _, n := range chain {
				memo[n] = rootID
			}
			return rootID, !rootID.IsZero(), nil
		}
		if _, exists := seen[current]; exists {
			for _, n := range chain {
				memo[n] = zero
			}
			return zero, false, nil
		}
		seen[current] = struct{}{}
		chain = append(chain, current)

		entity, err := s.idx.GetEntity(current)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				for _, n := range chain {
					memo[n] = zero
				}
				return zero, false, nil
			}
			return zero, false, err
		}
		if entity.ParentID.IsZero() {
			for _, n := range chain {
				memo[n] = entity.ID
			}
			return entity.ID, true, nil
		}
		current = entity.ParentID
	}
}

func (s *Service) rescanWithScope(targetMounts map[string]struct{}) error {
	type scanned struct {
		entity Entity
		entry  DirEntry
	}

	seen := make(map[FileID]struct{}, 1024)
	collected := make([]scanned, 0, 1024)
	targetRootIDs := make(map[FileID]struct{}, len(s.mountIDByName))

	for _, mountName := range s.mountNames {
		if targetMounts != nil {
			if _, ok := targetMounts[mountName]; !ok {
				continue
			}
		}
		basePath := s.mountByName[mountName]
		mountID := s.mountIDByName[mountName]
		targetRootIDs[mountID] = struct{}{}

		info, err := s.store.Stat(basePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		rootEntity := buildEntityMetadata(mountID, FileID{}, mountName, basePath, info)
		seen[mountID] = struct{}{}
		collected = append(collected, scanned{entity: rootEntity})

		pathToID := map[string]FileID{basePath: mountID}
		err = filepath.WalkDir(basePath, func(current string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if current == basePath {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			parent := filepath.Dir(current)
			parentID, ok := pathToID[parent]
			if !ok {
				return nil
			}

			id, err := s.store.GetID(current)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				id, err = newID()
				if err != nil {
					return err
				}
				if err := s.store.SetID(current, id); err != nil {
					// File may have been removed between WalkDir enumerating
					// it and us writing the xattr. That is an inevitable race
					// during a live rescan and must not abort the scan — the
					// caller would lose every other file in the tree.
					if errors.Is(err, os.ErrNotExist) {
						return nil
					}
					return err
				}
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}
			seen[id] = struct{}{}
			pathToID[current] = id
			entity := buildEntityMetadata(id, parentID, d.Name(), current, info)
			collected = append(collected, scanned{
				entity: entity,
				entry: DirEntry{
					ID:    id,
					Name:  d.Name(),
					IsDir: d.IsDir(),
					Size:  info.Size(),
					Mtime: info.ModTime().UnixMilli(),
				},
			})
			return nil
		})
		if err != nil {
			return err
		}
	}

	if err := s.idx.Batch(func(b Batch) error {
		for _, item := range collected {
			b.PutEntity(item.entity)
			if !item.entity.ParentID.IsZero() {
				b.PutChild(item.entity.ParentID, item.entity.Name, item.entry)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	type staleRef struct {
		id       FileID
		parentID FileID
		name     string
	}
	rootMemo := make(map[FileID]FileID, 4096)
	stale := make([]staleRef, 0, 1024)
	if err := s.idx.ForEachEntity(func(e Entity) error {
		if _, ok := seen[e.ID]; ok {
			return nil
		}
		if targetMounts != nil {
			rootID, ok, err := s.resolveRootID(e.ID, rootMemo)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if _, isTarget := targetRootIDs[rootID]; !isTarget {
				return nil
			}
		}
		stale = append(stale, staleRef{id: e.ID, parentID: e.ParentID, name: e.Name})
		return nil
	}); err != nil {
		return err
	}
	for start := 0; start < len(stale); start += 4096 {
		end := start + 4096
		if end > len(stale) {
			end = len(stale)
		}
		chunk := stale[start:end]
		if err := s.idx.Batch(func(b Batch) error {
			for _, e := range chunk {
				if !e.parentID.IsZero() {
					b.DelChild(e.parentID, e.name)
				}
				b.DelEntity(e.id)
			}
			return nil
		}); err != nil {
			return err
		}
	}

	s.purgePathCaches()
	eventPath := "*"
	if len(targetMounts) == 1 {
		for mountName := range targetMounts {
			eventPath = "/" + mountName
		}
	}
	s.bus.Publish(Event{Type: EventScanned, Path: eventPath, At: time.Now()})
	return nil
}

func (s *Service) Stats() (*ServiceStats, error) {
	s.mu.RLock()
	mountNames := append([]string(nil), s.mountNames...)
	mountIDByName := make(map[string]FileID, len(s.mountIDByName))
	for k, v := range s.mountIDByName {
		mountIDByName[k] = v
	}
	pathCacheEntries := s.cache.Len()
	pathCacheCapacity := s.pathCacheSize
	s.mu.RUnlock()

	mountByID := make(map[FileID]*StatsMount, len(mountNames))
	for _, name := range mountNames {
		mountID := mountIDByName[name]
		mountByID[mountID] = &StatsMount{
			ID:   mountID,
			Name: name,
			Path: "/" + name,
		}
	}
	rootMemo := make(map[FileID]FileID, 4096)
	totalEntities := 0
	totalFiles := 0
	totalDirs := 0
	if err := s.idx.ForEachEntity(func(e Entity) error {
		totalEntities++
		if e.IsDir {
			totalDirs++
		} else {
			totalFiles++
		}

		if e.ParentID.IsZero() {
			return nil
		}
		rootID, ok, err := s.resolveRootID(e.ID, rootMemo)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		mount := mountByID[rootID]
		if mount == nil {
			return nil
		}
		if e.IsDir {
			mount.Dirs++
		} else {
			mount.Files++
		}
		return nil
	}); err != nil {
		return nil, err
	}

	mounts := make([]StatsMount, 0, len(mountNames))
	for _, name := range mountNames {
		mountID := mountIDByName[name]
		if mount, ok := mountByID[mountID]; ok {
			mounts = append(mounts, *mount)
		}
	}

	util := 0.0
	if pathCacheCapacity > 0 {
		util = float64(pathCacheEntries) / float64(pathCacheCapacity)
	}

	return &ServiceStats{
		GeneratedAt:        time.Now().UnixMilli(),
		TotalEntities:      totalEntities,
		TotalFiles:         totalFiles,
		TotalDirs:          totalDirs,
		PathCacheEntries:   pathCacheEntries,
		PathCacheCapacity:  pathCacheCapacity,
		PathCacheUtilRatio: util,
		Mounts:             mounts,
	}, nil
}

func (s *Service) InvalidatePathCache(path string) {
	key := normalizeCacheKey(path)
	if key == "" {
		return
	}
	s.cache.Remove(key)
	s.idPathCache.Purge()
}

func (s *Service) purgePathCaches() {
	s.cache.Purge()
	s.idPathCache.Purge()
}

func normalizeCacheKey(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "/" {
		return ""
	}
	v = strings.TrimPrefix(v, "/")
	v = strings.Trim(strings.ReplaceAll(v, "\\", "/"), "/")
	v = path.Clean(v)
	if v == "." || v == "" {
		return ""
	}
	return v
}

func parentCacheKey(v string) string {
	if v == "" {
		return ""
	}
	p := path.Dir(v)
	if p == "." || p == "/" {
		return ""
	}
	return p
}

func (s *Service) invalidateCachePrefix(pathPrefix string) {
	prefix := normalizeCacheKey(pathPrefix)
	if prefix == "" {
		s.purgePathCaches()
		return
	}
	keys := s.cache.Keys()
	for _, key := range keys {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			s.cache.Remove(key)
		}
	}
	s.idPathCache.Purge()
}

func (s *Service) invalidateCacheByID(id FileID) {
	s.idPathCache.Remove(id)
	vp, err := s.VirtualPath(id)
	if err != nil {
		s.purgePathCaches()
		return
	}
	key := normalizeCacheKey(vp)
	if key == "" {
		return
	}
	s.invalidateCachePrefix(key)
	parent := parentCacheKey(key)
	if parent != "" {
		s.cache.Remove(parent)
	}
}
