/** Ownership input for create/update operations. */
export interface Ownership {
  uid?: number;
  gid?: number;
  mode?: string;
  dirMode?: string;
}

/** Ownership as returned by the server. */
export interface OwnershipView {
  uid: number;
  gid: number;
  mode: string;
}

/** A file or directory node.
 *
 * `nextCursor` is an opaque pagination token: pass it back verbatim as
 * the `cursor` option to fetch the next page of children. Do not
 * construct or interpret it. */
export interface Node {
  id: string;
  type: "file" | "directory";
  name: string;
  path: string;
  size: number;
  mtime: number;
  ownership: OwnershipView;
  mimeType?: string;
  etag?: string;
  sha256?: string;
  exif: Record<string, string>;
  children?: Node[];
  pageSize?: number;
  nextCursor?: string;
}

/** Response for root listing (GET /v1/paths/). */
export interface NodeListResponse {
  items: Node[];
  total: number;
}

/** OK acknowledgement. */
export interface OKResponse {
  ok: boolean;
}

/** Error response body. On a 409 Conflict, `existingId` and `existingPath`
 * are populated so the client can render a "what should we do?" prompt
 * without an extra resolve call. Both undefined otherwise. */
export interface ErrorResponse {
  error: string;
  existingId?: string;
  existingPath?: string;
}

/** Conflict mode accepted by file-write endpoints. `"skip"` is mkdir-only and
 * not allowed here. Upload sessions narrow this to `"error" | "overwrite"`. */
export type FileConflictMode = "error" | "overwrite" | "rename";
export type UploadSessionConflictMode = Exclude<FileConflictMode, "rename">;
export type FingerprintMode = "none" | "cached" | "ensure";

/** Conflict mode accepted by mkdir. `"overwrite"` is forbidden — replacing
 * a directory subtree is a Transfer operation, not a mkdir one. */
export type MkdirConflictMode = "error" | "skip" | "rename";

// --- Stats ---

export interface StatsIndex {
  totalEntities: number;
  totalFiles: number;
  totalDirs: number;
  dbSizeBytes: number;
}

export interface StatsCache {
  pathEntries: number;
  pathCapacity: number;
  pathUtilRatio: number;
}

export interface StatsMount {
  id: string;
  name: string;
  path: string;
  files: number;
  dirs: number;
}

export interface StatsDisk {
  diskName: string;
  fsType: string;
  used: number;
  size: number;
  roots: string[];
}

export interface StatsResponse {
  generatedAt: number;
  index: StatsIndex;
  cache: StatsCache;
  mounts: StatsMount[];
  disks: StatsDisk[];
}

// --- Activity ---

export interface ActivityActor {
  kind: "system" | "bearer_token" | "s3_key" | "signed_url" | string;
  id: string;
  label?: string;
  delegatedActor?: string;
}

export interface ActivityTarget {
  kind: string;
  id?: string;
  path?: string;
}

export interface ActivityEvent {
  id: string;
  at: number;
  actor: ActivityActor;
  operation: string;
  outcome: "succeeded" | "failed" | "skipped" | string;
  target?: ActivityTarget;
  durationMs?: number;
  requestId?: string;
  error?: string;
  meta?: Record<string, string | number | boolean>;
}

export interface ActivityListResponse {
  items: ActivityEvent[];
  total: number;
  offset: number;
  limit: number;
  retained: number;
  capacity: number;
  operations: string[];
}

// --- Capabilities ---

export interface CapabilitiesResponse {
  uploads: UploadCapabilities;
}

export interface UploadCapabilities {
  maxChunkBytes: number;
  maxUploadBytes: number;
  maxSessionUploadBytes: number;
  maxConcurrentSegmentWrites: number;
}

// --- Mkdir ---

export interface MkdirRequest {
  path: string;
  recursive?: boolean;
  ownership?: Ownership;
  /** Default `"error"`. `"skip"` returns the existing directory unchanged
   * if one with the same name exists. `"rename"` picks a unique sibling
   * name and creates a fresh directory there. `"overwrite"` is rejected. */
  onConflict?: MkdirConflictMode;
}

// --- Update ---

export interface UpdateNodeRequest {
  name?: string;
  ownership?: Ownership;
}

// --- Transfer ---

export interface TransferRequest {
  op: "move" | "copy";
  sourceId: string;
  targetParentId: string;
  targetName: string;
  /** Default `"error"`. Same vocabulary as the other file-write endpoints —
   * see `FileConflictMode`. */
  onConflict?: FileConflictMode;
  ownership?: Ownership;
}

export interface TransferResponse {
  node: Node;
  op: string;
}

// --- Search ---

export interface GlobSearchError {
  path: string;
  cause: string;
}

export interface GlobSearchPath {
  path: string;
  returned: number;
  hasMore: boolean;
}

export interface GlobSearchMeta {
  pattern: string;
  limit: number;
  resultCount: number;
  errorCount: number;
}

export interface GlobSearchResponse {
  results: Node[];
  errors: GlobSearchError[];
  meta: GlobSearchMeta;
  paths: GlobSearchPath[];
}

// --- Direct uploads ---

export interface DirectUploadURLRequest {
	path: string;
	expiresInSeconds?: number;
	contentType?: string;
	onConflict?: FileConflictMode;
	maxBytes?: number;
}

export interface DirectUploadURLResponse {
  uploadUrl: string;
  method: "PUT";
  path: string;
  expiresAt: number;
  maxBytes: number;
}

// --- Direct downloads ---

export interface DirectDownloadURLRequest {
  nodeId?: string;
  path?: string;
  expiresInSeconds?: number;
  inline?: boolean;
}

export interface DirectDownloadURLResponse {
  downloadUrl: string;
  method: "GET";
  expiresAt: number;
  node: Node;
}

// --- Upload sessions ---

export interface UploadSessionDirectRequest {
  expiresInSeconds?: number;
  allow?: Array<"putSegment" | "status" | "commit" | "abort">;
}

export interface UploadSessionCreateRequest {
	path: string;
	size: number;
	checksum: string;
	segmentSize?: number;
	contentType?: string;
	ownership?: Ownership;
	onConflict?: UploadSessionConflictMode;
	direct?: UploadSessionDirectRequest;
}

export interface UploadSessionBatchCreateRequest {
  uploads: UploadSessionCreateRequest[];
  segmentSize?: number;
  direct?: UploadSessionDirectRequest;
}

export interface UploadSessionSegment {
  index: number;
  offset: number;
  size: number;
}

export interface UploadSessionDirect {
  baseUrl: string;
  token: string;
  expiresAt: number;
  allow: Array<"putSegment" | "status" | "commit" | "abort">;
}

export interface UploadSessionResponse {
  id: string;
  path: string;
  size: number;
  checksum: string;
  segmentSize: number;
  totalSegments: number;
  segments: UploadSessionSegment[];
  uploadedSegments: number[];
  phase: "in_progress" | "committing" | "committed" | "aborted";
  direct?: UploadSessionDirect;
}

export interface UploadSessionBatchCreateResponse {
  sessions: UploadSessionResponse[];
}

export interface UploadSegmentResponse {
  sessionId: string;
  index: number;
  uploadedSegments: number[];
}

export interface UploadSessionCommitResponse {
  node: Node;
  checksum: string;
}

// --- Index resolve ---

export interface IndexResolveRequest {
  path?: string;
  paths?: string[];
  id?: string;
  ids?: string[];
}

export interface IndexResolveSingleResponse {
  item: Node | null;
}

export interface IndexResolveManyResponse {
  items: (Node | null)[];
  total: number;
}
