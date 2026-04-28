import { ClientCore } from "./core.js";
import { ensureSuccess } from "./errors.js";
import type {
  ChunkedCompleteResponse,
  ChunkedProgressResponse,
  ChunkedStartRequest,
  ChunkedStatusResponse,
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

  constructor(core: ClientCore) {
    this.chunked = new ChunkedUploadClient(core);
  }
}
