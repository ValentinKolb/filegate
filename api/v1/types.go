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
//
// NextCursor is an opaque pagination token: pass it back verbatim as the
// `cursor` query parameter to fetch the next page of children. Clients
// must not construct or interpret it.
type Node struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Size       int64             `json:"size"`
	Mtime      int64             `json:"mtime"`
	Ownership  OwnershipView     `json:"ownership"`
	MimeType   string            `json:"mimeType,omitempty"`
	ETag       string            `json:"etag,omitempty"`
	SHA256     string            `json:"sha256,omitempty"`
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

// CapabilitiesResponse describes server-enforced limits clients can use to
// choose request sizes without guessing from defaults.
type CapabilitiesResponse struct {
	Uploads UploadCapabilities `json:"uploads"`
}

// UploadCapabilities describes upload-related limits from the running server
// configuration.
type UploadCapabilities struct {
	MaxChunkBytes              int64 `json:"maxChunkBytes"`
	MaxUploadBytes             int64 `json:"maxUploadBytes"`
	MaxSessionUploadBytes      int64 `json:"maxSessionUploadBytes"`
	MaxConcurrentSegmentWrites int   `json:"maxConcurrentSegmentWrites"`
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

// VersionRestoreRequest is the body for POST /v1/nodes/{id}/versions/{vid}/restore.
// Both fields are optional. AsNewFile=false (default) does an in-place
// restore — current bytes get snapshotted, then replaced. AsNewFile=true
// places the version's bytes into a fresh sibling file; Name overrides
// the default `<base>-restored<ext>` and gets a `-N` suffix on conflict.
type VersionRestoreRequest struct {
	AsNewFile bool   `json:"asNewFile,omitempty"`
	Name      string `json:"name,omitempty"`
}

// VersionRestoreResponse is returned after a successful restore.
// AsNew is true for as-new restores, false for in-place. Node holds the
// resulting file (the source for in-place, the new sibling for as-new).
type VersionRestoreResponse struct {
	Node  Node `json:"node"`
	AsNew bool `json:"asNew"`
}

// GlobSearchResponse is returned by GET /v1/search/glob.
type GlobSearchResponse struct {
	Results []Node            `json:"results"`
	Errors  []GlobSearchError `json:"errors"`
	Meta    GlobSearchMeta    `json:"meta"`
	Paths   []GlobSearchPath  `json:"paths"`
}

// DirectUploadURLRequest is the body for POST /v1/uploads/direct.
//
// Filegate returns a short-lived unauthenticated PUT URL scoped to exactly
// Path. The caller must be authenticated with the REST bearer token to mint
// the URL, but the final uploader only needs the returned URL.
type DirectUploadURLRequest struct {
	Path             string `json:"path"`
	ExpiresInSeconds int64  `json:"expiresInSeconds,omitempty"`
	ContentType      string `json:"contentType,omitempty"`
	OnConflict       string `json:"onConflict,omitempty"`
	MaxBytes         int64  `json:"maxBytes,omitempty"`
}

// DirectUploadURLResponse is returned after minting a direct upload URL.
type DirectUploadURLResponse struct {
	UploadURL string `json:"uploadUrl"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	ExpiresAt int64  `json:"expiresAt"`
	MaxBytes  int64  `json:"maxBytes"`
}

// UploadSessionDirectRequest asks Filegate to mint a scoped direct session token.
type UploadSessionDirectRequest struct {
	ExpiresInSeconds int64    `json:"expiresInSeconds,omitempty"`
	Allow            []string `json:"allow,omitempty"`
}

// UploadSessionCreateRequest creates one resumable upload session for one file.
// OnConflict accepts "error" (default) or "overwrite"; "rename" is rejected so
// crash recovery can always reason about one stable target path.
type UploadSessionCreateRequest struct {
	Path        string                      `json:"path"`
	Size        int64                       `json:"size"`
	Checksum    string                      `json:"checksum"`
	SegmentSize int64                       `json:"segmentSize"`
	ContentType string                      `json:"contentType,omitempty"`
	Ownership   *Ownership                  `json:"ownership,omitempty"`
	OnConflict  string                      `json:"onConflict,omitempty"`
	Direct      *UploadSessionDirectRequest `json:"direct,omitempty"`
}

// UploadSessionBatchCreateRequest creates independent one-file upload sessions.
type UploadSessionBatchCreateRequest struct {
	Uploads     []UploadSessionCreateRequest `json:"uploads"`
	SegmentSize int64                        `json:"segmentSize,omitempty"`
	Direct      *UploadSessionDirectRequest  `json:"direct,omitempty"`
}

// UploadSessionSegment describes one segment in the upload plan.
type UploadSessionSegment struct {
	Index  int   `json:"index"`
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
}

// UploadSessionDirect describes the scoped token usable by public clients.
type UploadSessionDirect struct {
	BaseURL   string   `json:"baseUrl"`
	Token     string   `json:"token"`
	ExpiresAt int64    `json:"expiresAt"`
	Allow     []string `json:"allow"`
}

// UploadSessionResponse describes the upload session and its current progress.
type UploadSessionResponse struct {
	ID               string                 `json:"id"`
	Path             string                 `json:"path"`
	Size             int64                  `json:"size"`
	Checksum         string                 `json:"checksum"`
	SegmentSize      int64                  `json:"segmentSize"`
	TotalSegments    int                    `json:"totalSegments"`
	Segments         []UploadSessionSegment `json:"segments"`
	UploadedSegments []int                  `json:"uploadedSegments"`
	Phase            string                 `json:"phase"`
	Direct           *UploadSessionDirect   `json:"direct,omitempty"`
}

// UploadSessionBatchCreateResponse contains the created sessions.
type UploadSessionBatchCreateResponse struct {
	Sessions []UploadSessionResponse `json:"sessions"`
}

// UploadSegmentResponse is returned after an idempotent segment PUT.
type UploadSegmentResponse struct {
	SessionID        string `json:"sessionId"`
	Index            int    `json:"index"`
	UploadedSegments []int  `json:"uploadedSegments"`
}

// UploadSessionCommitResponse is returned after a session commit.
type UploadSessionCommitResponse struct {
	Node     Node   `json:"node"`
	Checksum string `json:"checksum"`
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
