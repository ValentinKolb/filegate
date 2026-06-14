// Pure browser- and runtime-safe helpers for working with uploads.
// No HTTP, no fetch, no token — safe to import from environments that have
// no Filegate connection (e.g. a Web Worker that hashes files for upload).

/** Number of segments needed for a given total size and segment size. */
const count = (req: { size: number; segmentSize: number }): number => {
  if (req.size <= 0 || req.segmentSize <= 0) return 0;
  return Math.ceil(req.size / req.segmentSize);
};

/** [start, end) byte offsets for the given segment index. */
const bounds = (
  index: number,
  size: number,
  segmentSize: number,
): { start: number; end: number } => {
  if (index < 0) throw new Error("index must be >= 0");
  const total = count({ size, segmentSize });
  if (index >= total) throw new Error("index out of range");
  const start = index * segmentSize;
  return { start, end: Math.min(start + segmentSize, size) };
};

const plan = (req: {
  size: number;
  segmentSize: number;
}): Array<{ index: number; offset: number; size: number }> => {
  const total = count(req);
  const out: Array<{ index: number; offset: number; size: number }> = [];
  for (let index = 0; index < total; index++) {
    const { start, end } = bounds(index, req.size, req.segmentSize);
    out.push({ index, offset: start, size: end - start });
  }
  return out;
};

/** SHA-256 checksum of a Uint8Array in filegate's `sha256:<hex>` format. */
const sha256 = async (data: Uint8Array): Promise<string> => {
  const hash = await crypto.subtle.digest(
    "SHA-256",
    data as ArrayBufferView<ArrayBuffer>,
  );
  const hex = Array.from(new Uint8Array(hash))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
  return `sha256:${hex}`;
};

export const uploads = {
  segments: { count, bounds, plan },
  checksum: { sha256 },
} as const;

export type UploadUtils = typeof uploads;
