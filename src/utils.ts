// ============================================================================
// Filegate Utils - Browser-compatible utilities for chunked uploads
// ============================================================================

// ============================================================================
// Type Declarations
// ============================================================================

// Blob.slice() is standard but missing from TypeScript's lib.dom.d.ts
declare global {
  interface Blob {
    slice(start?: number, end?: number, contentType?: string): Blob;
  }
}

// ============================================================================
// Types
// ============================================================================

export interface ChunkInfo {
  index: number;
  data: Blob;
  total: number;
}

export interface UploadState {
  uploaded: number;
  total: number;
  percent: number;
  status: "pending" | "uploading" | "completed" | "error";
}

export type StateSubscriber = (state: UploadState) => void;

export interface PrepareOptions {
  file: Blob | File;
  chunkSize?: number;
}

export interface SendOptions {
  index: number;
  retries?: number;
  fn: (chunk: { index: number; data: Blob }) => Promise<void>;
}

export interface SendAllOptions {
  skip?: number[];
  retries?: number;
  concurrency?: number;
  fn: (chunk: { index: number; data: Blob }) => Promise<void>;
}

// ============================================================================
// ChunkedUpload Class
// ============================================================================

export class ChunkedUpload {
  readonly file: Blob | File;
  readonly fileSize: number;
  readonly chunkSize: number;
  readonly totalChunks: number;
  readonly checksum: string;

  private _state: UploadState;
  private _subscribers: Set<StateSubscriber> = new Set();
  private _completedChunks: Set<number> = new Set();

  constructor(opts: { file: Blob | File; fileSize: number; chunkSize: number; totalChunks: number; checksum: string }) {
    this.file = opts.file;
    this.fileSize = opts.fileSize;
    this.chunkSize = opts.chunkSize;
    this.totalChunks = opts.totalChunks;
    this.checksum = opts.checksum;
    this._state = {
      uploaded: 0,
      total: opts.totalChunks,
      percent: 0,
      status: "pending",
    };
  }

  // ==========================================================================
  // State Management
  // ==========================================================================

  get state(): UploadState {
    return { ...this._state };
  }

  subscribe(fn: StateSubscriber): () => void {
    this._subscribers.add(fn);
    // Emit current state immediately
    fn(this.state);
    return () => {
      this._subscribers.delete(fn);
    };
  }

  private _updateState(partial: Partial<UploadState>): void {
    this._state = { ...this._state, ...partial };
    for (const fn of this._subscribers) {
      fn(this.state);
    }
  }

  complete(opts: { index: number }): void {
    if (this._completedChunks.has(opts.index)) return;

    this._completedChunks.add(opts.index);
    const uploaded = this._completedChunks.size;
    const percent = Math.round((uploaded / this.totalChunks) * 100);
    const status = uploaded === this.totalChunks ? "completed" : "uploading";

    this._updateState({ uploaded, percent, status });
  }

  reset(): void {
    this._completedChunks.clear();
    this._updateState({
      uploaded: 0,
      percent: 0,
      status: "pending",
    });
  }

  // ==========================================================================
  // Chunk Access
  // ==========================================================================

  get(opts: { index: number }): Blob {
    const start = opts.index * this.chunkSize;
    const end = Math.min(start + this.chunkSize, this.fileSize);
    // Blob.prototype.slice is standard but TypeScript's lib.dom.d.ts doesn't include it
    // We use a type-safe wrapper that preserves the Blob type
    return (this.file as Blob).slice(start, end);
  }

  async hash(opts: { data: Blob | ArrayBuffer }): Promise<string> {
    const buffer = opts.data instanceof Blob ? await opts.data.arrayBuffer() : opts.data;
    const hashBuffer = await crypto.subtle.digest("SHA-256", buffer);
    const hashArray = new Uint8Array(hashBuffer);
    return `sha256:${Array.from(hashArray)
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("")}`;
  }

  // ==========================================================================
  // Upload Helpers
  // ==========================================================================

  async send(opts: SendOptions): Promise<void> {
    const { index, retries = 0, fn } = opts;
    const data = this.get({ index });

    let lastError: Error | null = null;
    for (let attempt = 0; attempt <= retries; attempt++) {
      try {
        await fn({ index, data });
        this.complete({ index });
        return;
      } catch (err) {
        lastError = err instanceof Error ? err : new Error(String(err));
        if (attempt < retries) {
          // Exponential backoff: 100ms, 200ms, 400ms, ...
          await new Promise((r) => setTimeout(r, 100 * Math.pow(2, attempt)));
        }
      }
    }

    this._updateState({ status: "error" });
    throw lastError;
  }

  async sendAll(opts: SendAllOptions): Promise<void> {
    const { skip = [], retries = 0, concurrency = 1, fn } = opts;
    const skipSet = new Set(skip);

    // Mark skipped chunks as completed
    for (const index of skip) {
      this.complete({ index });
    }

    this._updateState({ status: "uploading" });

    // Get indices that need to be uploaded
    const toUpload: number[] = [];
    for (let i = 0; i < this.totalChunks; i++) {
      if (!skipSet.has(i) && !this._completedChunks.has(i)) {
        toUpload.push(i);
      }
    }

    if (concurrency === 1) {
      // Sequential upload
      for (const index of toUpload) {
        await this.send({ index, retries, fn });
      }
    } else {
      // Concurrent upload with limited parallelism
      const queue = [...toUpload];
      const inFlight: Promise<void>[] = [];

      while (queue.length > 0 || inFlight.length > 0) {
        // Start new uploads up to concurrency limit
        while (queue.length > 0 && inFlight.length < concurrency) {
          const index = queue.shift()!;
          const promise = this.send({ index, retries, fn }).then(() => {
            inFlight.splice(inFlight.indexOf(promise), 1);
          });
          inFlight.push(promise);
        }

        // Wait for at least one to complete
        if (inFlight.length > 0) {
          await Promise.race(inFlight);
        }
      }
    }

    this._updateState({ status: "completed" });
  }

  // ==========================================================================
  // Async Iterator
  // ==========================================================================

  async *[Symbol.asyncIterator](): AsyncGenerator<ChunkInfo> {
    for (let index = 0; index < this.totalChunks; index++) {
      yield {
        index,
        data: this.get({ index }),
        total: this.totalChunks,
      };
    }
  }
}

// ============================================================================
// chunks namespace
// ============================================================================

async function prepare(opts: PrepareOptions): Promise<ChunkedUpload> {
  const { file, chunkSize = 5 * 1024 * 1024 } = opts;
  const fileSize = file.size;
  const totalChunks = Math.ceil(fileSize / chunkSize);

  // Calculate checksum using WebCrypto
  const buffer = await file.arrayBuffer();
  const hashBuffer = await crypto.subtle.digest("SHA-256", buffer);
  const hashArray = new Uint8Array(hashBuffer);
  const checksum = `sha256:${Array.from(hashArray)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("")}`;

  return new ChunkedUpload({
    file,
    fileSize,
    chunkSize,
    totalChunks,
    checksum,
  });
}

export const chunks = {
  prepare,
};

// ============================================================================
// formatBytes
// ============================================================================

export function formatBytes(opts: { bytes: number; decimals?: number }): string {
  const { bytes, decimals = 2 } = opts;

  if (bytes === 0) return "0 Bytes";

  const k = 1024;
  const sizes = ["Bytes", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));

  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(decimals))} ${sizes[i]}`;
}
