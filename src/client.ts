import type { z } from "zod";
import type {
  FileInfoSchema,
  DirInfoSchema,
  SearchResponseSchema,
  UploadStartResponseSchema,
  UploadChunkResponseSchema,
  ErrorSchema,
} from "./schemas";

// ============================================================================
// Types
// ============================================================================

export type FileInfo = z.infer<typeof FileInfoSchema>;
export type DirInfo = z.infer<typeof DirInfoSchema>;
export type SearchResponse = z.infer<typeof SearchResponseSchema>;
export type UploadStartResponse = z.infer<typeof UploadStartResponseSchema>;
export type UploadChunkResponse = z.infer<typeof UploadChunkResponseSchema>;
export type ApiError = z.infer<typeof ErrorSchema>;

export type FileProxyResponse<T> = { ok: true; data: T } | { ok: false; error: string; status: number };

type Headers = Record<string, string>;

export interface ClientOptions {
  url: string;
  token: string;
  fetch?: typeof fetch;
}

// --- Info ---
export interface InfoOptions {
  path: string;
  showHidden?: boolean;
}

// --- Download ---
export interface DownloadOptions {
  path: string;
}

// --- Upload Single ---
export interface UploadSingleOptions {
  path: string;
  filename: string;
  data: Blob | ArrayBuffer | Uint8Array;
  uid?: number;
  gid?: number;
  mode?: string;
}

// --- Upload Chunked Start ---
export interface UploadChunkedStartOptions {
  path: string;
  filename: string;
  size: number;
  checksum: string;
  chunkSize: number;
  uid?: number;
  gid?: number;
  mode?: string;
}

// --- Upload Chunked Send ---
export interface UploadChunkedSendOptions {
  uploadId: string;
  index: number;
  data: Blob | ArrayBuffer | Uint8Array;
  checksum?: string;
}

// --- Mkdir ---
export interface MkdirOptions {
  path: string;
  uid?: number;
  gid?: number;
  mode?: string;
}

// --- Delete ---
export interface DeleteOptions {
  path: string;
}

// --- Move ---
export interface MoveOptions {
  from: string;
  to: string;
}

// --- Copy ---
export interface CopyOptions {
  from: string;
  to: string;
}

// --- Glob (Search) ---
export interface GlobOptions {
  paths: string[];
  pattern: string;
  showHidden?: boolean;
  limit?: number;
}

// ============================================================================
// Upload Namespace Class
// ============================================================================

class UploadClient {
  constructor(
    private readonly url: string,
    private readonly hdrs: () => Headers,
    private readonly jsonHdrs: () => Headers,
    private readonly _fetch: typeof fetch,
    private readonly handleResponse: <T>(res: Response) => Promise<FileProxyResponse<T>>,
  ) {}

  async single(opts: UploadSingleOptions): Promise<FileProxyResponse<FileInfo>> {
    const uploadHdrs: Headers = {
      ...this.hdrs(),
      "X-File-Path": opts.path,
      "X-File-Name": opts.filename,
    };
    if (opts.uid !== undefined) uploadHdrs["X-Owner-UID"] = String(opts.uid);
    if (opts.gid !== undefined) uploadHdrs["X-Owner-GID"] = String(opts.gid);
    if (opts.mode) uploadHdrs["X-File-Mode"] = opts.mode;

    const res = await this._fetch(`${this.url}/files/content`, {
      method: "PUT",
      headers: uploadHdrs,
      body: opts.data,
    });
    return this.handleResponse(res);
  }

  readonly chunked = {
    start: async (opts: UploadChunkedStartOptions): Promise<FileProxyResponse<UploadStartResponse>> => {
      const body = {
        path: opts.path,
        filename: opts.filename,
        size: opts.size,
        checksum: opts.checksum,
        chunkSize: opts.chunkSize,
        ownerUid: opts.uid,
        ownerGid: opts.gid,
        mode: opts.mode,
      };

      const res = await this._fetch(`${this.url}/files/upload/start`, {
        method: "POST",
        headers: this.jsonHdrs(),
        body: JSON.stringify(body),
      });
      return this.handleResponse(res);
    },

    send: async (opts: UploadChunkedSendOptions): Promise<FileProxyResponse<UploadChunkResponse>> => {
      const headers: Headers = {
        ...this.hdrs(),
        "X-Upload-Id": opts.uploadId,
        "X-Chunk-Index": String(opts.index),
      };
      if (opts.checksum) {
        headers["X-Chunk-Checksum"] = opts.checksum;
      }

      const res = await this._fetch(`${this.url}/files/upload/chunk`, {
        method: "POST",
        headers,
        body: opts.data,
      });
      return this.handleResponse(res);
    },
  };
}

// ============================================================================
// Client Class
// ============================================================================

export class Filegate {
  private readonly url: string;
  private readonly token: string;
  private readonly _fetch: typeof fetch;

  readonly upload: UploadClient;

  constructor(opts: ClientOptions) {
    this.url = opts.url.replace(/\/$/, "");
    this.token = opts.token;
    this._fetch = opts.fetch ?? fetch;

    this.upload = new UploadClient(
      this.url,
      () => this.hdrs(),
      () => this.jsonHdrs(),
      this._fetch,
      (res) => this.handleResponse(res),
    );
  }

  private hdrs(): Headers {
    return { Authorization: `Bearer ${this.token}` };
  }

  private jsonHdrs(): Headers {
    return { ...this.hdrs(), "Content-Type": "application/json" };
  }

  private async handleResponse<T>(res: Response): Promise<FileProxyResponse<T>> {
    if (!res.ok) {
      const body = (await res.json().catch(() => ({ error: "unknown error" }))) as ApiError;
      return { ok: false, error: body.error || "unknown error", status: res.status };
    }
    const data = (await res.json()) as T;
    return { ok: true, data };
  }

  // ==========================================================================
  // Info
  // ==========================================================================

  async info(opts: InfoOptions): Promise<FileProxyResponse<FileInfo | DirInfo>> {
    const params = new URLSearchParams({
      path: opts.path,
      showHidden: String(opts.showHidden ?? false),
    });
    const res = await this._fetch(`${this.url}/files/info?${params}`, { headers: this.hdrs() });
    return this.handleResponse(res);
  }

  // ==========================================================================
  // Download
  // ==========================================================================

  async download(opts: DownloadOptions): Promise<FileProxyResponse<Response>> {
    const params = new URLSearchParams({ path: opts.path });
    const res = await this._fetch(`${this.url}/files/content?${params}`, { headers: this.hdrs() });
    if (!res.ok) {
      const body = (await res.json().catch(() => ({ error: "unknown error" }))) as ApiError;
      return { ok: false, error: body.error || "unknown error", status: res.status };
    }
    return { ok: true, data: res };
  }

  // ==========================================================================
  // Directory Operations
  // ==========================================================================

  async mkdir(opts: MkdirOptions): Promise<FileProxyResponse<FileInfo>> {
    const body: Record<string, unknown> = { path: opts.path };
    if (opts.uid !== undefined) body.ownerUid = opts.uid;
    if (opts.gid !== undefined) body.ownerGid = opts.gid;
    if (opts.mode) body.mode = opts.mode;

    const res = await this._fetch(`${this.url}/files/mkdir`, {
      method: "POST",
      headers: this.jsonHdrs(),
      body: JSON.stringify(body),
    });
    return this.handleResponse(res);
  }

  // ==========================================================================
  // Delete
  // ==========================================================================

  async delete(opts: DeleteOptions): Promise<FileProxyResponse<void>> {
    const params = new URLSearchParams({ path: opts.path });
    const res = await this._fetch(`${this.url}/files/delete?${params}`, {
      method: "DELETE",
      headers: this.hdrs(),
    });
    if (!res.ok) {
      const body = (await res.json().catch(() => ({ error: "unknown error" }))) as ApiError;
      return { ok: false, error: body.error || "unknown error", status: res.status };
    }
    return { ok: true, data: undefined };
  }

  // ==========================================================================
  // Move & Copy
  // ==========================================================================

  async move(opts: MoveOptions): Promise<FileProxyResponse<FileInfo>> {
    const res = await this._fetch(`${this.url}/files/move`, {
      method: "POST",
      headers: this.jsonHdrs(),
      body: JSON.stringify({ from: opts.from, to: opts.to }),
    });
    return this.handleResponse(res);
  }

  async copy(opts: CopyOptions): Promise<FileProxyResponse<FileInfo>> {
    const res = await this._fetch(`${this.url}/files/copy`, {
      method: "POST",
      headers: this.jsonHdrs(),
      body: JSON.stringify({ from: opts.from, to: opts.to }),
    });
    return this.handleResponse(res);
  }

  // ==========================================================================
  // Glob (Search)
  // ==========================================================================

  async glob(opts: GlobOptions): Promise<FileProxyResponse<SearchResponse>> {
    const params = new URLSearchParams({
      paths: opts.paths.join(","),
      pattern: opts.pattern,
    });
    if (opts.showHidden) params.set("showHidden", "true");
    if (opts.limit) params.set("limit", String(opts.limit));

    const res = await this._fetch(`${this.url}/files/search?${params}`, { headers: this.hdrs() });
    return this.handleResponse(res);
  }
}

// ============================================================================
// Default Instance (server-side only)
// ============================================================================

const createDefaultInstance = (): Filegate => {
  const url = process.env.FILEGATE_URL;
  const token = process.env.FILEGATE_TOKEN;

  if (!url || !token) {
    throw new Error(
      "FILEGATE_URL and FILEGATE_TOKEN environment variables are required.\n" +
        "Either set these variables or create an instance manually:\n\n" +
        '  import { Filegate } from "filegate/client";\n' +
        '  const client = new Filegate({ url: "...", token: "..." });',
    );
  }

  return new Filegate({ url, token });
};

let _instance: Filegate | null = null;

export const filegate: Filegate = new Proxy({} as Filegate, {
  get(_target, prop) {
    if (_instance === null) {
      _instance = createDefaultInstance();
    }
    const value = _instance[prop as keyof Filegate];
    if (typeof value === "function") {
      return value.bind(_instance);
    }
    return value;
  },
});
