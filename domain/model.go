package domain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	xattrIDKey = "user.filegate.id"
)

// ErrInvalidID is returned when a string cannot be parsed as a FileID.
var ErrInvalidID = errors.New("invalid file id")

// FileID is a 16-byte stable identity for files and directories, derived from UUID v7.
type FileID [16]byte

// ParseFileID parses a UUID string (with or without dashes) into a FileID.
func ParseFileID(v string) (FileID, error) {
	var id FileID
	clean := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(v)), "-", "")
	if len(clean) != 32 {
		return id, ErrInvalidID
	}
	decoded, err := hex.DecodeString(clean)
	if err != nil || len(decoded) != 16 {
		return id, ErrInvalidID
	}
	copy(id[:], decoded)
	return id, nil
}

// String returns the FileID formatted as a standard UUID string with dashes.
func (id FileID) String() string {
	hexStr := hex.EncodeToString(id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32])
}

// IsZero reports whether the FileID is the zero value (all bytes zero).
func (id FileID) IsZero() bool {
	var zero FileID
	return id == zero
}

// Bytes returns a copy of the FileID as a byte slice.
func (id FileID) Bytes() []byte {
	out := make([]byte, 16)
	copy(out, id[:])
	return out
}

// XAttrIDKey returns the extended attribute key used to persist FileIDs on disk.
func XAttrIDKey() string {
	return xattrIDKey
}

// Entity is the full indexed metadata for a file or directory.
type Entity struct {
	ID       FileID
	ParentID FileID
	Name     string
	IsDir    bool
	Size     int64
	Mtime    int64
	UID      uint32
	GID      uint32
	Mode     uint32
	// Device and Inode together identify a file on disk independent of
	// its path. Persisted so resolveOrReissueID can detect xattr-clone
	// duplicates (snapshot, cp -a) by comparing the entity's recorded
	// inode against the path being synced — when they differ, a fresh
	// UUID is minted for the new path. Zero means "unknown" (e.g. mount
	// roots created without stat info).
	Device uint64
	Inode  uint64
	// Nlink is the hard-link count. PutEntity skips its same-id stale-
	// child cleanup when nlink > 1 — hard-link siblings legitimately
	// share an entity record across multiple (parent, name) pairs.
	Nlink    uint32
	MimeType string
	Exif     map[string]string
}

// DirEntry is a compact listing entry for a child of a directory.
type DirEntry struct {
	ID    FileID `json:"id"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// FileMeta is the JSON-serializable metadata representation with a virtual path.
type FileMeta struct {
	ID       FileID            `json:"id"`
	Type     string            `json:"type"`
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	Size     int64             `json:"size"`
	Mtime    int64             `json:"mtime"`
	UID      uint32            `json:"uid"`
	GID      uint32            `json:"gid"`
	Mode     uint32            `json:"mode"`
	MimeType string            `json:"mimeType,omitempty"`
	Exif     map[string]string `json:"exif"`
	IsRoot   bool              `json:"-"`
}

// Ownership specifies optional permission overrides for file operations.
type Ownership struct {
	UID     *int   `json:"uid,omitempty"`
	GID     *int   `json:"gid,omitempty"`
	Mode    string `json:"mode,omitempty"`
	DirMode string `json:"dirMode,omitempty"`
}

// TransferRequest describes a move or copy operation between nodes.
//
// OnConflict is a typed ConflictMode here. The HTTP layer parses the wire
// string via ParseConflictMode(..., FileConflictModes) before constructing
// this request — keeping vocabulary validation at the boundary instead of
// re-parsing strings inside the domain.
type TransferRequest struct {
	Op                 string       `json:"op"`
	SourceID           FileID       `json:"sourceId"`
	TargetParentID     FileID       `json:"targetParentId"`
	TargetName         string       `json:"targetName"`
	OnConflict         ConflictMode `json:"onConflict"`
	Ownership          *Ownership   `json:"ownership,omitempty"`
	RecursiveOwnership *bool        `json:"-"`
}

// ListedNodes is a paginated response for directory listing operations.
type ListedNodes struct {
	Items      []FileMeta `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// MountEntry describes a configured mount point with its name, ID, and filesystem path.
type MountEntry struct {
	Name string `json:"name"`
	ID   FileID `json:"id"`
	Path string `json:"path"`
}

// GlobSearchRequest specifies parameters for a glob-based file search.
type GlobSearchRequest struct {
	Pattern      string
	Paths        []string
	Limit        int
	ShowHidden   bool
	IncludeFiles bool
	IncludeDirs  bool
}

// GlobSearchError describes an error encountered while searching a specific base path.
type GlobSearchError struct {
	Path  string `json:"path"`
	Cause string `json:"cause"`
}

// GlobSearchPathResult describes the search result for a single base path.
type GlobSearchPathResult struct {
	Path     string `json:"path"`
	Returned int    `json:"returned"`
	HasMore  bool   `json:"hasMore"`
}

// GlobSearchResponse contains the aggregated results of a glob search.
type GlobSearchResponse struct {
	Results []FileMeta             `json:"results"`
	Errors  []GlobSearchError      `json:"errors"`
	Paths   []GlobSearchPathResult `json:"paths"`
}

// StatsMount contains per-mount file and directory counts.
type StatsMount struct {
	ID    FileID `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Files int    `json:"files"`
	Dirs  int    `json:"dirs"`
}

// ServiceStats contains runtime statistics for the service.
type ServiceStats struct {
	GeneratedAt        int64        `json:"generatedAt"`
	TotalEntities      int          `json:"totalEntities"`
	TotalFiles         int          `json:"totalFiles"`
	TotalDirs          int          `json:"totalDirs"`
	PathCacheEntries   int          `json:"pathCacheEntries"`
	PathCacheCapacity  int          `json:"pathCacheCapacity"`
	PathCacheUtilRatio float64      `json:"pathCacheUtilRatio"`
	Mounts             []StatsMount `json:"mounts"`
}
