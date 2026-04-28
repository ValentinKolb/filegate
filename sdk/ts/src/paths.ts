import { ClientCore } from "./core.js";
import { ensureSuccess } from "./errors.js";
import type { Node, NodeListResponse } from "./types.js";

export interface GetPathOptions {
  pageSize?: number;
  cursor?: string;
  computeRecursiveSizes?: boolean;
}

/**
 * Conflict-handling strategy for file writes. The default is `"error"`:
 * a 409 is returned when a file at the target path already exists.
 *
 * - `"error"` (default) — fail with 409 if the target exists.
 * - `"overwrite"` — replace the existing file in place; node id preserved.
 * - `"rename"` — pick a unique sibling name (foo.jpg → foo-01.jpg) and
 *   create a new file. The returned `node.name`/`node.path` reflect the
 *   actually-used name.
 */
export type FileConflictMode = "error" | "overwrite" | "rename";

export interface PutPathOptions {
  contentType?: string;
  onConflict?: FileConflictMode;
}

export interface PathPutResponse {
  node: Node;
  nodeId: string;
  createdId: string;
  statusCode: number;
}

function getQuery(opts?: GetPathOptions): Record<string, string> {
  const q: Record<string, string> = {};
  if (opts?.pageSize) q["pageSize"] = String(opts.pageSize);
  if (opts?.cursor) q["cursor"] = opts.cursor;
  if (opts?.computeRecursiveSizes) q["computeRecursiveSizes"] = "true";
  return q;
}

function encodeVirtualPath(path: string): string {
  return path
    .split("/")
    .filter((s) => s.length > 0)
    .map(encodeURIComponent)
    .join("/");
}

export class PathsClient {
  constructor(private readonly core: ClientCore) {}

  /**
   * Get a node by virtual path, or list roots when called without a path.
   *
   * - `get()` or `get("")` → lists root mounts (GET /v1/paths/)
   * - `get("data/files")` → metadata for that path (GET /v1/paths/data/files)
   */
  async get(virtualPath?: string, opts?: GetPathOptions): Promise<Node | NodeListResponse> {
    const trimmed = virtualPath?.trim().replace(/^\/+|\/+$/g, "") ?? "";
    if (trimmed === "") {
      return this.core.doJSON<NodeListResponse>("GET", "/v1/paths/", getQuery(opts));
    }
    const encoded = encodeVirtualPath(trimmed);
    return this.core.doJSON<Node>("GET", `/v1/paths/${encoded}`, getQuery(opts));
  }

  /** One-shot upload to a virtual path. */
  async put(virtualPath: string, data: BodyInit, opts?: PutPathOptions): Promise<PathPutResponse> {
    const encoded = encodeVirtualPath(virtualPath.trim());
    if (!encoded) throw new Error("path is required");
    const contentType = opts?.contentType ?? "application/octet-stream";
    const endpoint = `/v1/paths/${encoded}`;
    const resp = await this.core.doRaw("PUT", endpoint, putQuery(opts), data, contentType);
    await ensureSuccess(resp, "PUT", endpoint);
    const node = (await resp.json()) as Node;
    return {
      node,
      nodeId: resp.headers.get("X-Node-Id") ?? "",
      createdId: resp.headers.get("X-Created-Id") ?? "",
      statusCode: resp.status,
    };
  }

  /** One-shot upload returning the raw Response (for relay/proxy). */
  async putRaw(virtualPath: string, data: BodyInit, opts?: PutPathOptions): Promise<Response> {
    const encoded = encodeVirtualPath(virtualPath.trim());
    if (!encoded) throw new Error("path is required");
    const contentType = opts?.contentType ?? "application/octet-stream";
    return this.core.doRaw("PUT", `/v1/paths/${encoded}`, putQuery(opts), data, contentType);
  }
}

function putQuery(opts?: PutPathOptions): Record<string, string> | undefined {
  if (!opts?.onConflict) return undefined;
  return { onConflict: opts.onConflict };
}
