package v1

// ErrorResponse is the standard JSON error envelope returned by all endpoints.
type ErrorResponse struct {
	Error string `json:"error"`
	// ExistingID and ExistingPath are populated on a 409 Conflict response
	// when the conflict is caused by an existing node. They give the client
	// enough information to render a useful UI ("File foo.jpg already
	// exists, choose: overwrite / rename / cancel") without an extra
	// resolve call. Both empty otherwise.
	ExistingID   string `json:"existingId,omitempty"`
	ExistingPath string `json:"existingPath,omitempty"`
}

// OKResponse is returned by endpoints that acknowledge an action without data.
type OKResponse struct {
	OK bool `json:"ok"`
}

// Ownership specifies optional permission fields for create and update operations.
type Ownership struct {
	UID     *int   `json:"uid,omitempty"`
	GID     *int   `json:"gid,omitempty"`
	Mode    string `json:"mode,omitempty"`
	DirMode string `json:"dirMode,omitempty"`
}

// OwnershipView is the read-only ownership representation returned in node metadata.
type OwnershipView struct {
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	Mode string `json:"mode"`
}

// Node represents a file or directory with its metadata and optional children.
type Node struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Size       int64             `json:"size"`
	Mtime      int64             `json:"mtime"`
	Ownership  OwnershipView     `json:"ownership"`
	MimeType   string            `json:"mimeType,omitempty"`
	Exif       map[string]string `json:"exif"`
	Children   []Node            `json:"children,omitempty"`
	PageSize   *int              `json:"pageSize,omitempty"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// NodeListResponse is returned by the root listing endpoint.
type NodeListResponse struct {
	Items []Node `json:"items"`
	Total int    `json:"total"`
}

// StatsIndex contains index-level statistics.
type StatsIndex struct {
	TotalEntities int   `json:"totalEntities"`
	TotalFiles    int   `json:"totalFiles"`
	TotalDirs     int   `json:"totalDirs"`
	DBSizeBytes   int64 `json:"dbSizeBytes"`
}

// StatsCache contains path cache utilization statistics.
type StatsCache struct {
	PathEntries   int     `json:"pathEntries"`
	PathCapacity  int     `json:"pathCapacity"`
	PathUtilRatio float64 `json:"pathUtilRatio"`
}

// StatsMount contains per-mount file and directory counts.
type StatsMount struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Files int    `json:"files"`
	Dirs  int    `json:"dirs"`
}

// StatsDisk contains disk usage information for a storage device.
type StatsDisk struct {
	DiskName string   `json:"diskName"`
	FSType   string   `json:"fsType"`
	Used     uint64   `json:"used"`
	Size     uint64   `json:"size"`
	Roots    []string `json:"roots"`
}

// StatsResponse is the complete runtime statistics snapshot.
type StatsResponse struct {
	GeneratedAt int64        `json:"generatedAt"`
	Index       StatsIndex   `json:"index"`
	Cache       StatsCache   `json:"cache"`
	Mounts      []StatsMount `json:"mounts"`
	Disks       []StatsDisk  `json:"disks"`
}

// MkdirRequest is the body for POST /v1/nodes/{id}/mkdir.
//
// OnConflict accepts "error" (default), "skip", or "rename". "overwrite" is
// rejected — replacing a directory subtree is a Transfer operation, not a
// mkdir one.
type MkdirRequest struct {
	Path       string     `json:"path"`
	Recursive  *bool      `json:"recursive,omitempty"`
	Ownership  *Ownership `json:"ownership,omitempty"`
	OnConflict string     `json:"onConflict,omitempty"`
}

// UpdateNodeRequest is the body for PATCH /v1/nodes/{id}.
type UpdateNodeRequest struct {
	Name      *string    `json:"name,omitempty"`
	Ownership *Ownership `json:"ownership,omitempty"`
}

// TransferRequest is the body for POST /v1/transfers.
type TransferRequest struct {
	Op             string     `json:"op"`
	SourceID       string     `json:"sourceId"`
	TargetParentID string     `json:"targetParentId"`
	TargetName     string     `json:"targetName"`
	OnConflict     string     `json:"onConflict"`
	Ownership      *Ownership `json:"ownership,omitempty"`
}

// TransferResponse is returned after a successful move or copy operation.
type TransferResponse struct {
	Node Node   `json:"node"`
	Op   string `json:"op"`
}

// GlobSearchError describes an error encountered while searching a specific path.
type GlobSearchError struct {
	Path  string `json:"path"`
	Cause string `json:"cause"`
}

// GlobSearchPath describes the search result for a single base path.
type GlobSearchPath struct {
	Path     string `json:"path"`
	Returned int    `json:"returned"`
	HasMore  bool   `json:"hasMore"`
}

// GlobSearchMeta contains aggregate metadata about a glob search.
type GlobSearchMeta struct {
	Pattern     string `json:"pattern"`
	Limit       int    `json:"limit"`
	ResultCount int    `json:"resultCount"`
	ErrorCount  int    `json:"errorCount"`
}

// VersionResponse is the JSON shape for a single per-file version. The
// label is opaque server-side: clients may store plain text or JSON.
// DeletedAt is non-zero only after the source file has been deleted and
// the version entered the post-delete grace window.
type VersionResponse struct {
	VersionID string `json:"versionId"`
	FileID    string `json:"fileId"`
	Timestamp int64  `json:"timestamp"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	Pinned    bool   `json:"pinned"`
	Label     string `json:"label,omitempty"`
	DeletedAt int64  `json:"deletedAt,omitempty"`
}

// ListVersionsResponse is returned by GET /v1/nodes/{id}/versions.
// NextCursor is empty when the response covers the final page.
type ListVersionsResponse struct {
	Items      []VersionResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// VersionSnapshotRequest is the body for POST /v1/nodes/{id}/versions/snapshot.
// Both fields are optional. Label is opaque server-side, capped at the
// configured max_label_bytes.
type VersionSnapshotRequest struct {
	Label string `json:"label,omitempty"`
}

// VersionPinRequest is the body for POST /v1/nodes/{id}/versions/{vid}/pin.
// Label is optional; nil leaves the existing label unchanged, "" clears it.
type VersionPinRequest struct {
	Label *string `json:"label,omitempty"`
}

// GlobSearchResponse is returned by GET /v1/search/glob.
type GlobSearchResponse struct {
	Results []Node            `json:"results"`
	Errors  []GlobSearchError `json:"errors"`
	Meta    GlobSearchMeta    `json:"meta"`
	Paths   []GlobSearchPath  `json:"paths"`
}

// ChunkedStartRequest is the body for POST /v1/uploads/chunked/start.
//
// OnConflict accepts "error" (default), "overwrite", or "rename". The check
// runs both at start (optimistic, saves bandwidth on the common case) and
// again at finalize (race-safe). The chosen mode is persisted in the upload
// manifest and survives Resume.
type ChunkedStartRequest struct {
	ParentID   string     `json:"parentId"`
	Filename   string     `json:"filename"`
	Size       int64      `json:"size"`
	Checksum   string     `json:"checksum"`
	ChunkSize  int64      `json:"chunkSize"`
	Ownership  *Ownership `json:"ownership,omitempty"`
	OnConflict string     `json:"onConflict,omitempty"`
}

// ChunkedStatusResponse describes the current state of a chunked upload session.
type ChunkedStatusResponse struct {
	UploadID       string `json:"uploadId"`
	ChunkSize      int64  `json:"chunkSize"`
	TotalChunks    int    `json:"totalChunks"`
	UploadedChunks []int  `json:"uploadedChunks"`
	Completed      bool   `json:"completed"`
}

// ChunkedProgressResponse is returned after a chunk is accepted but the upload is not yet complete.
type ChunkedProgressResponse struct {
	ChunkIndex     int   `json:"chunkIndex"`
	UploadedChunks []int `json:"uploadedChunks"`
	Completed      bool  `json:"completed"`
}

// NodeWithChecksum extends Node with a checksum field for completed uploads.
type NodeWithChecksum struct {
	Node
	Checksum string `json:"checksum"`
}

// ChunkedCompleteResponse is returned when the final chunk completes the upload.
type ChunkedCompleteResponse struct {
	Completed bool             `json:"completed"`
	File      NodeWithChecksum `json:"file"`
}

// IndexResolveRequest is the body for POST /v1/index/resolve.
type IndexResolveRequest struct {
	Path  string   `json:"path,omitempty"`
	Paths []string `json:"paths,omitempty"`
	ID    string   `json:"id,omitempty"`
	IDs   []string `json:"ids,omitempty"`
}

// IndexResolveSingleResponse is returned when resolving a single path or ID.
type IndexResolveSingleResponse struct {
	Item *Node `json:"item"`
}

// IndexResolveManyResponse is returned when resolving multiple paths or IDs.
type IndexResolveManyResponse struct {
	Items []*Node `json:"items"`
	Total int     `json:"total"`
}
