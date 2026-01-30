import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { mkdir, readdir, rm, stat, rename, readFile, writeFile } from "node:fs/promises";
import { join, dirname } from "node:path";
import { getSemaphore } from "@henrygd/semaphore";
import { validatePath } from "../lib/path";
import { applyOwnership, type Ownership } from "../lib/ownership";
import { jsonResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
import {
  UploadStartBodySchema,
  UploadStartResponseSchema,
  UploadChunkResponseSchema,
  ErrorSchema,
  UploadChunkHeadersSchema,
} from "../schemas";
import { config } from "../config";

const app = new Hono();

// Deterministic upload ID from path + filename + checksum
const computeUploadId = (path: string, filename: string, checksum: string): string => {
  const hasher = new Bun.CryptoHasher("sha256");
  hasher.update(`${path}:${filename}:${checksum}`);
  return hasher.digest("hex").slice(0, 16);
};

// Chunk storage paths
const chunksDir = (id: string) => join(config.uploadTempDir, id);
const chunkPath = (id: string, idx: number) => join(chunksDir(id), String(idx));
const metaPath = (id: string) => join(chunksDir(id), "meta.json");

type UploadMeta = {
  uploadId: string;
  path: string;
  filename: string;
  size: number;
  checksum: string;
  chunkSize: number;
  totalChunks: number;
  ownership: Ownership | null;
  createdAt: number; // Unix timestamp for expiry check
};

const saveMeta = async (meta: UploadMeta): Promise<void> => {
  await mkdir(chunksDir(meta.uploadId), { recursive: true });
  await writeFile(metaPath(meta.uploadId), JSON.stringify(meta));
};

const loadMeta = async (id: string): Promise<UploadMeta | null> => {
  try {
    const data = await readFile(metaPath(id), "utf-8");
    return JSON.parse(data) as UploadMeta;
  } catch {
    return null;
  }
};

const refreshExpiry = async (id: string, meta: UploadMeta): Promise<void> => {
  // Update createdAt to extend expiry
  meta.createdAt = Date.now();
  await saveMeta(meta);
};

// Get uploaded chunks from filesystem
const getUploadedChunks = async (id: string): Promise<number[]> => {
  try {
    const files = await readdir(chunksDir(id));
    return files
      .filter((f) => f !== "meta.json")
      .map((f) => parseInt(f, 10))
      .filter((n) => !isNaN(n))
      .sort((a, b) => a - b);
  } catch {
    return [];
  }
};

const cleanupUpload = async (id: string): Promise<void> => {
  await rm(chunksDir(id), { recursive: true }).catch(() => {});
};

const assembleFile = async (meta: UploadMeta): Promise<string | null> => {
  // Use semaphore to prevent concurrent assembly of the same upload
  const semaphore = getSemaphore(`assemble:${meta.uploadId}`, 1);
  await semaphore.acquire();

  try {
    // Check if assembly was already completed by another request while we waited
    const chunks = await getUploadedChunks(meta.uploadId);
    if (chunks.length === 0) {
      // Chunks were cleaned up - assembly was already completed
      return null;
    }
    if (chunks.length !== meta.totalChunks) return "missing chunks";

    // Verify all expected chunk indices are present (0 to totalChunks-1)
    const expectedChunks = Array.from({ length: meta.totalChunks }, (_, i) => i);
    const missingChunks = expectedChunks.filter((i) => !chunks.includes(i));
    if (missingChunks.length > 0) {
      return `missing chunk indices: ${missingChunks.join(", ")}`;
    }

    const fullPath = join(meta.path, meta.filename);
    const pathResult = await validatePath(fullPath);
    if (!pathResult.ok) return pathResult.error;

    await mkdir(dirname(pathResult.realPath), { recursive: true });

    const hasher = new Bun.CryptoHasher("sha256");
    const file = Bun.file(pathResult.realPath);
    const writer = file.writer();

    try {
      for (let i = 0; i < meta.totalChunks; i++) {
        const chunkFilePath = chunkPath(meta.uploadId, i);
        const chunkFile = Bun.file(chunkFilePath);

        // Verify chunk exists before reading
        if (!(await chunkFile.exists())) {
          writer.end();
          await rm(pathResult.realPath).catch(() => {});
          return `chunk ${i} not found during assembly`;
        }

        // Read chunk as buffer (more reliable than streaming)
        const data = new Uint8Array(await chunkFile.arrayBuffer());
        hasher.update(data);
        writer.write(data);
      }
      await writer.end();
    } catch (e) {
      writer.end();
      await rm(pathResult.realPath).catch(() => {});
      throw e;
    }

    const finalChecksum = `sha256:${hasher.digest("hex")}`;
    if (finalChecksum !== meta.checksum) {
      await rm(pathResult.realPath).catch(() => {});
      return `checksum mismatch: expected ${meta.checksum}, got ${finalChecksum}`;
    }

    const ownershipError = await applyOwnership(pathResult.realPath, meta.ownership);
    if (ownershipError) {
      await rm(pathResult.realPath).catch(() => {});
      return ownershipError;
    }

    await cleanupUpload(meta.uploadId);
    return null;
  } finally {
    semaphore.release();
  }
};

// POST /upload/start
app.post(
  "/start",
  describeRoute({
    tags: ["Upload"],
    summary: "Start or resume chunked upload",
    description: "Initialize a new upload or get status of existing upload with same checksum",
    ...requiresAuth,
    responses: {
      200: jsonResponse(UploadStartResponseSchema, "Upload initialized or resumed"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
    },
  }),
  v("json", UploadStartBodySchema),
  async (c) => {
    const body = c.req.valid("json");

    // Validate size limits
    if (body.size > config.maxUploadBytes) {
      return c.json({ error: `file size exceeds maximum (${config.maxUploadBytes / 1024 / 1024}MB)` }, 413);
    }
    if (body.chunkSize > config.maxChunkBytes) {
      return c.json({ error: `chunk size exceeds maximum (${config.maxChunkBytes / 1024 / 1024}MB)` }, 400);
    }

    const fullPath = join(body.path, body.filename);

    // Build ownership from body
    const ownership: Ownership | null =
      body.ownerUid != null && body.ownerGid != null && body.mode
        ? {
            uid: body.ownerUid,
            gid: body.ownerGid,
            mode: parseInt(body.mode, 8),
            dirMode: body.dirMode ? parseInt(body.dirMode, 8) : undefined,
          }
        : null;

    // Validate path and create parent directories with ownership
    const pathResult = await validatePath(fullPath, { createParents: true, ownership });
    if (!pathResult.ok) return c.json({ error: pathResult.error }, pathResult.status);

    // Deterministic upload ID - same file/path/checksum = same ID (enables resume)
    const uploadId = computeUploadId(body.path, body.filename, body.checksum);

    // Check for existing upload (resume)
    const existingMeta = await loadMeta(uploadId);
    if (existingMeta) {
      // Refresh expiry on resume
      await refreshExpiry(uploadId, existingMeta);
      // Get chunks from filesystem
      const uploadedChunks = await getUploadedChunks(uploadId);
      return c.json({
        uploadId,
        totalChunks: existingMeta.totalChunks,
        chunkSize: existingMeta.chunkSize,
        uploadedChunks,
        completed: false as const,
      });
    }

    // New upload
    const chunkSize = body.chunkSize;
    const totalChunks = Math.ceil(body.size / chunkSize);

    const meta: UploadMeta = {
      uploadId,
      path: body.path,
      filename: body.filename,
      size: body.size,
      checksum: body.checksum,
      chunkSize,
      totalChunks,
      ownership,
      createdAt: Date.now(),
    };

    await saveMeta(meta);

    return c.json({
      uploadId,
      totalChunks,
      chunkSize,
      uploadedChunks: [],
      completed: false as const,
    });
  },
);

// POST /upload/chunk
app.post(
  "/chunk",
  describeRoute({
    tags: ["Upload"],
    summary: "Upload a chunk",
    description: "Upload a single chunk. Auto-completes when all chunks received.",
    ...requiresAuth,
    responses: {
      200: jsonResponse(UploadChunkResponseSchema, "Chunk received"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      404: jsonResponse(ErrorSchema, "Upload not found"),
    },
  }),
  v("header", UploadChunkHeadersSchema),
  async (c) => {
    const headers = c.req.valid("header");
    const uploadId = headers["x-upload-id"];
    const chunkIndex = headers["x-chunk-index"];
    const chunkChecksum = headers["x-chunk-checksum"];

    const meta = await loadMeta(uploadId);
    if (!meta) return c.json({ error: "upload not found" }, 404);

    if (chunkIndex >= meta.totalChunks) {
      return c.json({ error: `chunk index ${chunkIndex} exceeds total ${meta.totalChunks}` }, 400);
    }

    const body = c.req.raw.body;
    if (!body) return c.json({ error: "missing body" }, 400);

    // Stream chunks to a temporary file to avoid memory accumulation
    const tempChunkPath = chunkPath(uploadId, chunkIndex) + ".tmp";
    let chunkSize = 0;
    const hasher = new Bun.CryptoHasher("sha256");

    await mkdir(chunksDir(uploadId), { recursive: true });
    const tempFile = Bun.file(tempChunkPath);
    const writer = tempFile.writer();

    try {
      for await (const chunk of body) {
        chunkSize += chunk.length;
        if (chunkSize > config.maxChunkBytes) {
          writer.end();
          await rm(tempChunkPath).catch(() => {});
          return c.json({ error: `chunk size exceeds maximum (${config.maxChunkBytes / 1024 / 1024}MB)` }, 413);
        }
        hasher.update(chunk);
        writer.write(chunk);
      }
      await writer.end();
    } catch (e) {
      writer.end();
      await rm(tempChunkPath).catch(() => {});
      throw e;
    }

    // Verify checksum if provided
    if (chunkChecksum) {
      const computed = `sha256:${hasher.digest("hex")}`;
      if (computed !== chunkChecksum) {
        await rm(tempChunkPath).catch(() => {});
        return c.json({ error: `chunk checksum mismatch: expected ${chunkChecksum}, got ${computed}` }, 400);
      }
    }

    // Move temp file to final location (atomic rename, no memory copy)
    const finalChunkPath = chunkPath(uploadId, chunkIndex);
    await rename(tempChunkPath, finalChunkPath);

    // Get uploaded chunks from filesystem
    const uploadedChunks = await getUploadedChunks(uploadId);

    if (uploadedChunks.length === meta.totalChunks) {
      const assembleError = await assembleFile(meta);
      if (assembleError) return c.json({ error: assembleError }, 500);

      const fullPath = join(meta.path, meta.filename);
      const file = Bun.file(fullPath);
      const s = await stat(fullPath);

      return c.json({
        completed: true as const,
        file: {
          name: meta.filename,
          path: fullPath,
          type: "file" as const,
          size: s.size,
          mtime: s.mtime.toISOString(),
          isHidden: meta.filename.startsWith("."),
          checksum: meta.checksum,
          mimeType: file.type,
        },
      });
    }

    return c.json({
      chunkIndex,
      uploadedChunks,
      completed: false as const,
    });
  },
);

// Cleanup expired upload directories
export const cleanupOrphanedChunks = async () => {
  try {
    const dirs = await readdir(config.uploadTempDir);
    let cleaned = 0;
    const now = Date.now();
    const expiryMs = config.uploadExpirySecs * 1000;

    for (const dir of dirs) {
      const meta = await loadMeta(dir);

      // Remove if no meta or expired
      if (!meta || now - meta.createdAt > expiryMs) {
        await rm(chunksDir(dir), { recursive: true }).catch(() => {});
        cleaned++;
      }
    }

    if (cleaned > 0) {
      console.log(`[Filegate] Cleaned up ${cleaned} expired upload${cleaned === 1 ? "" : "s"}`);
    }
  } catch {
    // Directory doesn't exist yet
  }
};

export default app;
