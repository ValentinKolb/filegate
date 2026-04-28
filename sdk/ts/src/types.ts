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

/** A file or directory node. */
export interface Node {
  id: string;
  type: "file" | "directory";
  name: string;
  path: string;
  size: number;
  mtime: number;
  ownership: OwnershipView;
  mimeType?: string;
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

/** Conflict mode accepted by file-write endpoints (PUT /v1/paths,
 * chunked upload start). `"skip"` is mkdir-only and not allowed here. */
export type FileConflictMode = "error" | "overwrite" | "rename";

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

// --- Chunked uploads ---

export interface ChunkedStartRequest {
  parentId: string;
  filename: string;
  size: number;
  checksum: string;
  chunkSize: number;
  ownership?: Ownership;
  /** Default `"error"`. The check is performed both at start (optimistic,
   * saves bandwidth) and at finalize (race-safe). The chosen mode is
   * persisted in the upload manifest and survives Resume — in fact, a
   * resumed start may upgrade the mode (e.g. retry with `"overwrite"`
   * after the first attempt collided at finalize). */
  onConflict?: FileConflictMode;
}

export interface ChunkedStatusResponse {
  uploadId: string;
  chunkSize: number;
  totalChunks: number;
  uploadedChunks: number[];
  completed: boolean;
}

export interface ChunkedProgressResponse {
  chunkIndex: number;
  uploadedChunks: number[];
  completed: boolean;
}

export interface NodeWithChecksum extends Node {
  checksum: string;
}

export interface ChunkedCompleteResponse {
  completed: boolean;
  file: NodeWithChecksum;
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
