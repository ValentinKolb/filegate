// Pure browser- and runtime-safe helpers for working with chunked uploads.
// No HTTP, no fetch, no token — safe to import from environments that have
// no Filegate connection (e.g. a Web Worker that hashes files for upload).

/** Number of chunks needed for a given total size and chunk size. */
const totalChunks = (size: number, chunkSize: number): number => {
  if (size <= 0 || chunkSize <= 0) return 0;
  return Math.ceil(size / chunkSize);
};

/** [start, end) byte offsets for the given chunk index. */
const bounds = (
  index: number,
  size: number,
  chunkSize: number,
): { start: number; end: number } => {
  if (index < 0) throw new Error("index must be >= 0");
  const total = totalChunks(size, chunkSize);
  if (index >= total) throw new Error("index out of range");
  const start = index * chunkSize;
  return { start, end: Math.min(start + chunkSize, size) };
};

/** SHA-256 checksum of a Uint8Array in filegate's `sha256:<hex>` format. */
const sha256Bytes = async (data: Uint8Array): Promise<string> => {
  const hash = await crypto.subtle.digest(
    "SHA-256",
    data as ArrayBufferView<ArrayBuffer>,
  );
  const hex = Array.from(new Uint8Array(hash))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
  return `sha256:${hex}`;
};

export const chunks = { totalChunks, bounds, sha256Bytes } as const;

export type Chunks = typeof chunks;
