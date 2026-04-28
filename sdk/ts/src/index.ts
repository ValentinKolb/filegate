export { Filegate, filegate, type FilegateConfig } from "./client.js";
export { FilegateError } from "./errors.js";
export type { FetchImpl } from "./core.js";

export type { GetPathOptions, PathPutResponse, PutPathOptions } from "./paths.js";
export type { ThumbnailOptions } from "./nodes.js";
export type { ChunkedSendResult } from "./uploads.js";
export type { GlobOptions } from "./search.js";

export type { FileConflictMode, MkdirConflictMode } from "./types.js";

// Pure helpers are intentionally NOT re-exported here. Import them from the
// dedicated entrypoint to keep tree-shaking honest:
//   import { chunks } from "@valentinkolb/filegate/utils";

export type {
  ChunkedCompleteResponse,
  ChunkedProgressResponse,
  ChunkedStartRequest,
  ChunkedStatusResponse,
  ErrorResponse,
  GlobSearchError,
  GlobSearchMeta,
  GlobSearchPath,
  GlobSearchResponse,
  IndexResolveManyResponse,
  IndexResolveRequest,
  IndexResolveSingleResponse,
  MkdirRequest,
  Node,
  NodeListResponse,
  NodeWithChecksum,
  OKResponse,
  Ownership,
  OwnershipView,
  StatsCache,
  StatsDisk,
  StatsIndex,
  StatsMount,
  StatsResponse,
  TransferRequest,
  TransferResponse,
  UpdateNodeRequest,
} from "./types.js";
