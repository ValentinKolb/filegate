import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { readdir, mkdir, rm, rename, cp, stat, access } from "node:fs/promises";
import { join, basename, relative, dirname, extname } from "node:path";
import sanitizeFilename from "sanitize-filename";
import { validatePath, validateSameBase } from "../lib/path";
import { parseOwnershipBody, applyOwnership, applyOwnershipRecursive } from "../lib/ownership";
import { jsonResponse, binaryResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
import {
  indexFile,
  identifyPath,
  resolveId,
  removeFromIndex,
  removeFromIndexRecursive,
  updateIndexPath,
  enrichFileInfoBatch,
} from "../lib/index";
import {
  FileInfoSchema,
  DirInfoSchema,
  ErrorSchema,
  InfoQuerySchema,
  PathQuerySchema,
  ContentQuerySchema,
  MkdirBodySchema,
  TransferBodySchema,
  UploadFileHeadersSchema,
  type FileInfo,
} from "../schemas";
import { config } from "../config";

const app = new Hono();

// Generate a unique path by appending -01, -02, etc. if target exists
const getUniquePath = async (targetPath: string): Promise<string> => {
  // Check if target exists
  try {
    await access(targetPath);
  } catch {
    // Doesn't exist, use as-is
    return targetPath;
  }

  const dir = dirname(targetPath);
  const ext = extname(targetPath);
  const base = basename(targetPath, ext);

  for (let i = 1; i <= 99; i++) {
    const suffix = i.toString().padStart(2, "0");
    const newPath = join(dir, `${base}-${suffix}${ext}`);
    try {
      await access(newPath);
    } catch {
      return newPath;
    }
  }

  // Fallback: use timestamp if all 99 are taken
  const timestamp = Date.now();
  return join(dir, `${base}-${timestamp}${ext}`);
};

// Cross-platform directory size using `du` command
const getDirSize = async (dirPath: string): Promise<number> => {
  const isMac = process.platform === "darwin";

  // macOS (BSD): du -sk (kilobytes), Linux (GNU): du -sb (bytes)
  const args = isMac ? ["-sk", dirPath] : ["-sb", dirPath];
  const multiplier = isMac ? 1024 : 1;

  try {
    const proc = Bun.spawn(["du", ...args], {
      stdout: "pipe",
      stderr: "ignore",
    });
    const output = await new Response(proc.stdout).text();
    await proc.exited;

    const value = parseInt(output.split("\t")[0] ?? "", 10);
    return isNaN(value) ? 0 : value * multiplier;
  } catch {
    return 0;
  }
};

const resolveQueryPath = async (
  path: string | undefined,
  id: string | undefined,
): Promise<{ ok: true; path: string } | { ok: false; status: 400 | 404; error: string }> => {
  if (id) {
    if (!config.indexEnabled) {
      return { ok: false, status: 400, error: "index disabled" };
    }
    const resolved = await resolveId(id);
    if (!resolved) return { ok: false, status: 404, error: "not found" };
    return { ok: true, path: join(resolved.basePath, resolved.relPath) };
  }

  if (!path) {
    return { ok: false, status: 400, error: "path or id required" };
  }

  return { ok: true, path };
};

const withFileId = async (info: FileInfo, basePath: string, absPath: string): Promise<FileInfo> => {
  if (!config.indexEnabled) return info;
  const relPath = relative(basePath, absPath);
  const fileId = await identifyPath(basePath, relPath);
  return fileId ? { ...info, fileId } : info;
};

const enrichListingItems = async (items: FileInfo[], basePath: string, dirPath: string): Promise<FileInfo[]> => {
  if (!config.indexEnabled || items.length === 0) return items;
  const relPaths = items.map((item) => relative(basePath, join(dirPath, item.path)));
  const tempItems = items.map((item, i) => ({ ...item, path: relPaths[i] ?? item.path }));
  const enriched = await enrichFileInfoBatch(tempItems, basePath);
  return items.map((item, i) => {
    const fileId = enriched[i]?.fileId;
    return fileId ? { ...item, fileId } : item;
  });
};

const getFileInfo = async (path: string, relativeTo?: string, computeDirSize?: boolean): Promise<FileInfo> => {
  const file = Bun.file(path);
  const s = await stat(path);
  const name = basename(path);
  const isDir = s.isDirectory();

  return {
    name,
    path: relativeTo ? relative(relativeTo, path) : path,
    type: isDir ? "directory" : "file",
    size: isDir ? (computeDirSize ? await getDirSize(path) : 0) : s.size,
    mtime: s.mtime.toISOString(),
    isHidden: name.startsWith("."),
    mimeType: isDir ? undefined : file.type,
  };
};

// GET /info
app.get(
  "/info",
  describeRoute({
    tags: ["Files"],
    summary: "Get file or directory info",
    ...requiresAuth,
    responses: {
      200: jsonResponse(DirInfoSchema, "File or directory info"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("query", InfoQuerySchema),
  async (c) => {
    const { path, id, showHidden, computeSizes } = c.req.valid("query");

    const resolved = await resolveQueryPath(path, id);
    if (!resolved.ok) return c.json({ error: resolved.error }, resolved.status);

    const result = await validatePath(resolved.path, { allowBasePath: true });
    if (!result.ok) return c.json({ error: result.error }, result.status);

    let s;
    try {
      s = await stat(result.realPath);
    } catch {
      return c.json({ error: "not found" }, 404);
    }

    if (!s.isDirectory()) {
      const info = await getFileInfo(result.realPath);
      return c.json(await withFileId(info, result.basePath, result.realPath));
    }

    const entries = await readdir(result.realPath, { withFileTypes: true });

    // Parallel file info retrieval (computeSizes only when requested)
    let items = (
      await Promise.all(
        entries
          .filter((e) => showHidden || !e.name.startsWith("."))
          .map((e) => getFileInfo(join(result.realPath, e.name), result.realPath, computeSizes).catch(() => null)),
      )
    ).filter((item): item is FileInfo => item !== null);

    items = await enrichListingItems(items, result.basePath, result.realPath);

    const info = await getFileInfo(result.realPath);
    const infoWithId = await withFileId(info, result.basePath, result.realPath);
    const totalSize = computeSizes ? items.reduce((sum, item) => sum + item.size, 0) : 0;
    return c.json({ ...infoWithId, size: totalSize, items, total: items.length });
  },
);

// GET /content
app.get(
  "/content",
  describeRoute({
    tags: ["Files"],
    summary: "Download file or directory",
    description:
      "Downloads a file directly or a directory as a TAR archive. Size limit applies to both. Use ?inline=true to display in browser instead of downloading.",
    ...requiresAuth,
    responses: {
      200: binaryResponse("application/octet-stream", "File content or TAR archive"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
      413: jsonResponse(ErrorSchema, "Content too large"),
    },
  }),
  v("query", ContentQuerySchema),
  async (c) => {
    const { path, id, inline } = c.req.valid("query");

    const resolved = await resolveQueryPath(path, id);
    if (!resolved.ok) return c.json({ error: resolved.error }, resolved.status);

    const result = await validatePath(resolved.path);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    let s;
    try {
      s = await stat(result.realPath);
    } catch {
      return c.json({ error: "not found" }, 404);
    }

    if (s.isDirectory()) {
      const size = await getDirSize(result.realPath);
      if (size > config.maxDownloadBytes) {
        return c.json(
          { error: `directory exceeds max download size (${Math.round(config.maxDownloadBytes / 1024 / 1024)}MB)` },
          413,
        );
      }

      const dirName = basename(result.realPath);

      // Create TAR archive using Bun's native API
      const files: Record<string, Blob> = {};

      // Add directory contents recursively
      const addDirectoryToArchive = async (dirPath: string, basePath: string) => {
        const entries = await readdir(dirPath, { withFileTypes: true });
        for (const entry of entries) {
          const fullPath = join(dirPath, entry.name);
          const archivePath = relative(basePath, fullPath);

          if (entry.isDirectory()) {
            await addDirectoryToArchive(fullPath, basePath);
          } else if (entry.isFile()) {
            // Read file content as Blob - Bun.file() is lazy and doesn't work reliably with Archive
            const fileContent = await Bun.file(fullPath).arrayBuffer();
            files[archivePath] = new Blob([fileContent]);
          }
        }
      };

      await addDirectoryToArchive(result.realPath, result.realPath);

      // Generate the tar archive
      const archive = new Bun.Archive(files);
      const archiveBlob = await archive.blob();

      const tarName = `${dirName}.tar`;
      return new Response(archiveBlob, {
        headers: {
          "Content-Type": "application/x-tar",
          "Content-Disposition": `attachment; filename="${encodeURIComponent(tarName)}"; filename*=UTF-8''${encodeURIComponent(tarName)}`,
          "X-File-Name": encodeURIComponent(tarName),
        },
      });
    }

    const file = Bun.file(result.realPath);
    if (!(await file.exists())) return c.json({ error: "not found" }, 404);

    if (file.size > config.maxDownloadBytes) {
      return c.json(
        { error: `file exceeds max download size (${Math.round(config.maxDownloadBytes / 1024 / 1024)}MB)` },
        413,
      );
    }

    const filename = basename(result.realPath);
    const encodedFilename = encodeURIComponent(filename);
    const disposition = inline ? "inline" : "attachment";

    return new Response(file.stream(), {
      headers: {
        "Content-Type": file.type,
        "Content-Length": String(file.size),
        "Content-Disposition": `${disposition}; filename="${encodedFilename}"; filename*=UTF-8''${encodedFilename}`,
        "X-File-Name": encodedFilename,
      },
    });
  },
);

// PUT /content
app.put(
  "/content",
  describeRoute({
    tags: ["Files"],
    summary: "Upload file",
    description:
      "Upload a file with optional ownership. Use headers X-File-Path, X-File-Name, and optionally X-Owner-UID, X-Owner-GID, X-File-Mode.",
    ...requiresAuth,
    responses: {
      201: jsonResponse(FileInfoSchema, "File created"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      413: jsonResponse(ErrorSchema, "File too large"),
    },
  }),
  v("header", UploadFileHeadersSchema),
  async (c) => {
    const headers = c.req.valid("header");
    const dirPath = headers["x-file-path"];
    const rawFilename = headers["x-file-name"];

    // Sanitize filename to prevent path traversal
    const filename = sanitizeFilename(rawFilename);
    if (!filename || filename !== rawFilename) {
      return c.json({ error: "invalid filename" }, 400);
    }

    const fullPath = join(dirPath, filename);

    // Build ownership from validated headers
    const ownership: import("../lib/ownership").Ownership | null =
      headers["x-owner-uid"] != null && headers["x-owner-gid"] != null && headers["x-file-mode"] != null
        ? {
            uid: headers["x-owner-uid"],
            gid: headers["x-owner-gid"],
            mode: parseInt(headers["x-file-mode"], 8),
            dirMode: headers["x-dir-mode"] ? parseInt(headers["x-dir-mode"], 8) : undefined,
          }
        : null;

    // Validate path and create parent directories with ownership
    const result = await validatePath(fullPath, { createParents: true, ownership });
    if (!result.ok) return c.json({ error: result.error }, result.status);

    const body = c.req.raw.body;
    if (!body) return c.json({ error: "missing body" }, 400);

    // Stream to file instead of buffering in memory
    let written = 0;
    const file = Bun.file(result.realPath);
    const writer = file.writer();

    try {
      for await (const chunk of body) {
        written += chunk.length;
        if (written > config.maxUploadBytes) {
          writer.end();
          await rm(result.realPath).catch(() => {});
          return c.json({ error: "file exceeds max upload size" }, 413);
        }
        writer.write(chunk);
      }
      await writer.end();
    } catch (e) {
      writer.end();
      await rm(result.realPath).catch(() => {});
      throw e;
    }

    const ownershipError = await applyOwnership(result.realPath, ownership);
    if (ownershipError) {
      await rm(result.realPath).catch(() => {});
      return c.json({ error: ownershipError }, 500);
    }

    const info = await getFileInfo(result.realPath);

    if (!config.indexEnabled) {
      return c.json(info, 201);
    }

    try {
      const s = await stat(result.realPath);
      const relPath = relative(result.basePath, result.realPath);
      const outcome = await indexFile(result.basePath, relPath, {
        dev: s.dev,
        ino: s.ino,
        size: s.size,
        mtimeMs: s.mtimeMs,
        isDirectory: s.isDirectory(),
      });
      return c.json({ ...info, fileId: outcome.id }, 201);
    } catch (err) {
      console.error("[Filegate] Index update failed:", err);
      return c.json(info, 201);
    }
  },
);

// POST /mkdir
app.post(
  "/mkdir",
  describeRoute({
    tags: ["Files"],
    summary: "Create directory",
    ...requiresAuth,
    responses: {
      201: jsonResponse(FileInfoSchema, "Directory created"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
    },
  }),
  v("json", MkdirBodySchema),
  async (c) => {
    const body = c.req.valid("json");

    const result = await validatePath(body.path);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    const ownership = parseOwnershipBody(body);

    await mkdir(result.realPath, { recursive: true });

    const ownershipError = await applyOwnership(result.realPath, ownership);
    if (ownershipError) {
      await rm(result.realPath, { recursive: true }).catch(() => {});
      return c.json({ error: ownershipError }, 500);
    }

    const info = await getFileInfo(result.realPath);

    if (!config.indexEnabled) {
      return c.json(info, 201);
    }

    try {
      const s = await stat(result.realPath);
      const relPath = relative(result.basePath, result.realPath);
      const outcome = await indexFile(result.basePath, relPath, {
        dev: s.dev,
        ino: s.ino,
        size: s.size,
        mtimeMs: s.mtimeMs,
        isDirectory: s.isDirectory(),
      });
      return c.json({ ...info, fileId: outcome.id }, 201);
    } catch (err) {
      console.error("[Filegate] Index update failed:", err);
      return c.json(info, 201);
    }
  },
);

// DELETE /delete
app.delete(
  "/delete",
  describeRoute({
    tags: ["Files"],
    summary: "Delete file or directory",
    ...requiresAuth,
    responses: {
      204: { description: "Deleted" },
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("query", PathQuerySchema),
  async (c) => {
    const { path, id } = c.req.valid("query");

    const resolved = await resolveQueryPath(path, id);
    if (!resolved.ok) return c.json({ error: resolved.error }, resolved.status);

    const result = await validatePath(resolved.path);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    let s;
    try {
      s = await stat(result.realPath);
    } catch {
      return c.json({ error: "not found" }, 404);
    }

    if (config.indexEnabled) {
      const relPath = relative(result.basePath, result.realPath);
      if (s.isDirectory()) {
        await removeFromIndexRecursive(result.basePath, relPath).catch((err) => {
          console.error("[Filegate] Index remove failed:", err);
        });
      } else {
        await removeFromIndex(result.basePath, relPath).catch((err) => {
          console.error("[Filegate] Index remove failed:", err);
        });
      }
    }

    await rm(result.realPath, { recursive: s.isDirectory() });
    return c.body(null, 204);
  },
);

// POST /transfer
app.post(
  "/transfer",
  describeRoute({
    tags: ["Files"],
    summary: "Move or copy file/directory",
    description:
      "Transfer files between locations. Mode 'move' requires same base path. " +
      "Mode 'copy' allows cross-base transfer when ownership (ownerUid, ownerGid, fileMode) is provided.",
    ...requiresAuth,
    responses: {
      200: jsonResponse(FileInfoSchema, "Transferred"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("json", TransferBodySchema),
  async (c) => {
    const { from, to, mode, ensureUniqueName, ownerUid, ownerGid, fileMode, dirMode } = c.req.valid("json");

    // Build ownership if provided
    const ownership =
      ownerUid != null && ownerGid != null && fileMode != null
        ? {
            uid: ownerUid,
            gid: ownerGid,
            mode: parseInt(fileMode, 8),
            dirMode: dirMode ? parseInt(dirMode, 8) : undefined,
          }
        : null;

    // Move always requires same base
    if (mode === "move") {
      const result = await validateSameBase(from, to);
      if (!result.ok) return c.json({ error: result.error }, result.status);

      try {
        await stat(result.realPath);
      } catch {
        return c.json({ error: "source not found" }, 404);
      }

      const sourceRelPath = relative(result.basePath, result.realPath);
      const existingId = config.indexEnabled ? await identifyPath(result.basePath, sourceRelPath) : null;

      const targetPath = ensureUniqueName ? await getUniquePath(result.realTo) : result.realTo;

      await mkdir(join(targetPath, ".."), { recursive: true });
      await rename(result.realPath, targetPath);

      // Apply ownership if provided (for move within same base)
      if (ownership) {
        const ownershipError = await applyOwnershipRecursive(targetPath, ownership);
        if (ownershipError) {
          return c.json({ error: ownershipError }, 500);
        }
      }

      const info = await getFileInfo(targetPath);

      if (!config.indexEnabled) {
        return c.json(info);
      }

      try {
        const targetRelPath = relative(result.basePath, targetPath);
        if (existingId) {
          await updateIndexPath(existingId, result.basePath, targetRelPath);
          return c.json({ ...info, fileId: existingId });
        }

        const s = await stat(targetPath);
        const outcome = await indexFile(result.basePath, targetRelPath, {
          dev: s.dev,
          ino: s.ino,
          size: s.size,
          mtimeMs: s.mtimeMs,
          isDirectory: s.isDirectory(),
        });
        return c.json({ ...info, fileId: outcome.id });
      } catch (err) {
        console.error("[Filegate] Index update failed:", err);
        return c.json(info);
      }
    }

    // Copy: check if same base or cross-base with ownership
    const sameBaseResult = await validateSameBase(from, to);

    if (sameBaseResult.ok) {
      // Same base - no ownership required
      try {
        await stat(sameBaseResult.realPath);
      } catch {
        return c.json({ error: "source not found" }, 404);
      }

      const targetPath = ensureUniqueName ? await getUniquePath(sameBaseResult.realTo) : sameBaseResult.realTo;

      await mkdir(join(targetPath, ".."), { recursive: true });
      await cp(sameBaseResult.realPath, targetPath, { recursive: true });

      // Apply ownership if provided
      if (ownership) {
        const ownershipError = await applyOwnershipRecursive(targetPath, ownership);
        if (ownershipError) {
          await rm(targetPath, { recursive: true }).catch(() => {});
          return c.json({ error: ownershipError }, 500);
        }
      }

      const info = await getFileInfo(targetPath);

      if (!config.indexEnabled) {
        return c.json(info);
      }

      try {
        const targetRelPath = relative(sameBaseResult.basePath, targetPath);
        const s = await stat(targetPath);
        const outcome = await indexFile(sameBaseResult.basePath, targetRelPath, {
          dev: s.dev,
          ino: s.ino,
          size: s.size,
          mtimeMs: s.mtimeMs,
          isDirectory: s.isDirectory(),
        });
        return c.json({ ...info, fileId: outcome.id });
      } catch (err) {
        console.error("[Filegate] Index update failed:", err);
        return c.json(info);
      }
    }

    // Cross-base copy - ownership is required
    if (!ownership) {
      return c.json({ error: "cross-base copy requires ownership (ownerUid, ownerGid, fileMode)" }, 400);
    }

    // Validate source and destination separately
    const fromResult = await validatePath(from);
    if (!fromResult.ok) return c.json({ error: fromResult.error }, fromResult.status);

    const toResult = await validatePath(to, { createParents: true, ownership });
    if (!toResult.ok) return c.json({ error: toResult.error }, toResult.status);

    try {
      await stat(fromResult.realPath);
    } catch {
      return c.json({ error: "source not found" }, 404);
    }

    const targetPath = ensureUniqueName ? await getUniquePath(toResult.realPath) : toResult.realPath;

    await mkdir(join(targetPath, ".."), { recursive: true });
    await cp(fromResult.realPath, targetPath, { recursive: true });

    // Apply ownership recursively to copied content
    const ownershipError = await applyOwnershipRecursive(targetPath, ownership);
    if (ownershipError) {
      await rm(targetPath, { recursive: true }).catch(() => {});
      return c.json({ error: ownershipError }, 500);
    }

    const info = await getFileInfo(targetPath);

    if (!config.indexEnabled) {
      return c.json(info);
    }

    try {
      const targetRelPath = relative(toResult.basePath, targetPath);
      const s = await stat(targetPath);
      const outcome = await indexFile(toResult.basePath, targetRelPath, {
        dev: s.dev,
        ino: s.ino,
        size: s.size,
        mtimeMs: s.mtimeMs,
        isDirectory: s.isDirectory(),
      });
      return c.json({ ...info, fileId: outcome.id });
    } catch (err) {
      console.error("[Filegate] Index update failed:", err);
      return c.json(info);
    }
  },
);

export default app;
