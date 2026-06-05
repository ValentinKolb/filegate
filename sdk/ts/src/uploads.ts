import { ClientCore, type FetchImpl } from "./core.js";
import { ensureSuccess } from "./errors.js";
import type {
  ChunkedCompleteResponse,
  ChunkedProgressResponse,
  ChunkedStartRequest,
  ChunkedStatusResponse,
  DirectUploadURLRequest,
  DirectUploadURLResponse,
  Node,
} from "./types.js";

export interface ChunkedSendResult {
  completed: boolean;
  progress?: ChunkedProgressResponse;
  complete?: ChunkedCompleteResponse;
}

export class ChunkedUploadClient {
  constructor(private readonly core: ClientCore) {}

  /** Start or resume a chunked upload session. */
  async start(req: ChunkedStartRequest): Promise<ChunkedStatusResponse> {
    return this.core.doJSON<ChunkedStatusResponse>(
      "POST",
      "/v1/uploads/chunked/start",
      undefined,
      JSON.stringify(req),
      "application/json",
    );
  }

  /** Get current upload status. */
  async status(uploadId: string): Promise<ChunkedStatusResponse> {
    if (!uploadId.trim()) throw new Error("uploadId is required");
    return this.core.doJSON<ChunkedStatusResponse>(
      "GET",
      `/v1/uploads/chunked/${encodeURIComponent(uploadId)}`,
    );
  }

  /** Upload a single chunk. Returns progress or completion. */
  async sendChunk(
    uploadId: string,
    index: number,
    data: BodyInit,
    checksum?: string,
  ): Promise<ChunkedSendResult> {
    if (!uploadId.trim()) throw new Error("uploadId is required");
    if (index < 0) throw new Error("index must be >= 0");

    const resp = await this.sendChunkRaw(uploadId, index, data, checksum);
    await ensureSuccess(resp, "PUT", `/v1/uploads/chunked/${uploadId}/chunks/${index}`);
    const body = (await resp.json()) as { completed: boolean; file?: unknown };

    if (body.file) {
      const complete = body as unknown as ChunkedCompleteResponse;
      return { completed: true, complete };
    }

    const progress = body as unknown as ChunkedProgressResponse;
    return { completed: body.completed, progress };
  }

  /** Upload a chunk and return the raw Response (for relay). */
  async sendChunkRaw(
    uploadId: string,
    index: number,
    data: BodyInit,
    checksum?: string,
  ): Promise<Response> {
    if (!uploadId.trim()) throw new Error("uploadId is required");
    if (index < 0) throw new Error("index must be >= 0");

    const endpoint = `/v1/uploads/chunked/${encodeURIComponent(uploadId)}/chunks/${index}`;
    const extra: Record<string, string> = {};
    if (checksum?.trim()) extra["X-Chunk-Checksum"] = checksum.trim();

    return this.core.doRaw("PUT", endpoint, undefined, data, "application/octet-stream", extra);
  }
}

export class UploadsClient {
  readonly chunked: ChunkedUploadClient;

  constructor(private readonly core: ClientCore) {
    this.chunked = new ChunkedUploadClient(core);
  }

  /** Mint a short-lived direct PUT URL for a browser/client upload. */
  async createDirectUploadURL(req: DirectUploadURLRequest): Promise<DirectUploadURLResponse> {
    return this.core.doJSON<DirectUploadURLResponse>(
      "POST",
      "/v1/uploads/direct",
      undefined,
      JSON.stringify(req),
      "application/json",
    );
  }
}

export interface DirectUploadResult {
  node: Node;
  nodeId: string;
  createdId: string;
  statusCode: number;
  headers: Headers;
}

export type DirectUploadFinish =
  | { ok: true; result: DirectUploadResult }
  | { ok: false; error: unknown };

export interface DirectUploadOptions {
  fetchImpl?: FetchImpl;
  contentType?: string;
  signal?: AbortSignal;
  onSuccess?: (result: DirectUploadResult) => void | Promise<void>;
  onError?: (error: unknown) => void | Promise<void>;
  onFinish?: (outcome: DirectUploadFinish) => void | Promise<void>;
}

/** Upload to a presigned Filegate direct-upload URL without a REST bearer token. */
export async function uploadDirect(
  uploadUrl: string,
  data: BodyInit,
  opts?: DirectUploadOptions,
): Promise<DirectUploadResult> {
  const fetchImpl = opts?.fetchImpl ?? globalThis.fetch?.bind(globalThis);
  if (!fetchImpl) throw new Error("fetch is not available");
  if (!uploadUrl.trim()) throw new Error("uploadUrl is required");

  let result: DirectUploadResult | undefined;
  let caught: unknown;
  try {
    const headers: Record<string, string> = {};
    const contentType = opts?.contentType ?? bodyContentType(data);
    if (contentType) headers["Content-Type"] = contentType;

    const resp = await fetchImpl(uploadUrl, {
      method: "PUT",
      headers,
      body: data,
      signal: opts?.signal,
    });
    await ensureSuccess(resp, "PUT", uploadUrl);
    const node = (await resp.json()) as Node;
    result = {
      node,
      nodeId: resp.headers.get("X-Node-Id") ?? "",
      createdId: resp.headers.get("X-Created-Id") ?? "",
      statusCode: resp.status,
      headers: resp.headers,
    };
    await opts?.onSuccess?.(result);
    return result;
  } catch (err) {
    caught = err;
    await opts?.onError?.(err);
    throw err;
  } finally {
    if (result) {
      await opts?.onFinish?.({ ok: true, result });
    } else {
      await opts?.onFinish?.({ ok: false, error: caught });
    }
  }
}

function bodyContentType(data: BodyInit): string | undefined {
  const maybeTyped = data as { type?: unknown };
  if (typeof maybeTyped.type === "string" && maybeTyped.type.trim()) {
    return maybeTyped.type;
  }
  return undefined;
}
