import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { readdir, mkdir, rm, rename, cp, stat } from "node:fs/promises";
import { join, basename, relative } from "node:path";
import sanitizeFilename from "sanitize-filename";
import { validatePath, validateSameBase } from "../lib/path";
import { parseOwnershipBody, applyOwnership, applyOwnershipRecursive } from "../lib/ownership";
import { jsonResponse, binaryResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
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
    const { path, showHidden } = c.req.valid("query");

    const result = await validatePath(path, { allowBasePath: true });
    if (!result.ok) return c.json({ error: result.error }, result.status);

    let s;
    try {
      s = await stat(result.realPath);
    } catch {
      return c.json({ error: "not found" }, 404);
    }

    if (!s.isDirectory()) {
      return c.json(await getFileInfo(result.realPath));
    }

    const entries = await readdir(result.realPath, { withFileTypes: true });

    // Parallel file info retrieval
    const items = (
      await Promise.all(
        entries
          .filter((e) => showHidden || !e.name.startsWith("."))
          .map((e) => getFileInfo(join(result.realPath, e.name), result.realPath, true).catch(() => null)),
      )
    ).filter((item): item is FileInfo => item !== null);

    const info = await getFileInfo(result.realPath);
    return c.json({ ...info, items, total: items.length });
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
    const { path, inline } = c.req.valid("query");

    const result = await validatePath(path);
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
            files[archivePath] = Bun.file(fullPath);
          }
        }
      };

      await addDirectoryToArchive(result.realPath, result.realPath);

      // Generate the tar archive
      const archive = new Bun.Archive(files);
      const archiveBlob = await archive.blob();

      return new Response(archiveBlob, {
        headers: {
          "Content-Type": "application/x-tar",
          "Content-Disposition": `attachment; filename="${dirName}.tar"`,
          "X-File-Name": `${dirName}.tar`,
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
    const disposition = inline ? "inline" : "attachment";

    return new Response(file.stream(), {
      headers: {
        "Content-Type": file.type,
        "Content-Length": String(file.size),
        "Content-Disposition": `${disposition}; filename="${filename}"`,
        "X-File-Name": filename,
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

    return c.json(await getFileInfo(result.realPath), 201);
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

    return c.json(await getFileInfo(result.realPath), 201);
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
    const { path } = c.req.valid("query");

    const result = await validatePath(path);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    let s;
    try {
      s = await stat(result.realPath);
    } catch {
      return c.json({ error: "not found" }, 404);
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
    const { from, to, mode, ownerUid, ownerGid, fileMode, dirMode } = c.req.valid("json");

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

      await mkdir(join(result.realTo, ".."), { recursive: true });
      await rename(result.realPath, result.realTo);

      // Apply ownership if provided (for move within same base)
      if (ownership) {
        const ownershipError = await applyOwnershipRecursive(result.realTo, ownership);
        if (ownershipError) {
          return c.json({ error: ownershipError }, 500);
        }
      }

      return c.json(await getFileInfo(result.realTo));
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

      await mkdir(join(sameBaseResult.realTo, ".."), { recursive: true });
      await cp(sameBaseResult.realPath, sameBaseResult.realTo, { recursive: true });

      // Apply ownership if provided
      if (ownership) {
        const ownershipError = await applyOwnershipRecursive(sameBaseResult.realTo, ownership);
        if (ownershipError) {
          await rm(sameBaseResult.realTo, { recursive: true }).catch(() => {});
          return c.json({ error: ownershipError }, 500);
        }
      }

      return c.json(await getFileInfo(sameBaseResult.realTo));
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

    await mkdir(join(toResult.realPath, ".."), { recursive: true });
    await cp(fromResult.realPath, toResult.realPath, { recursive: true });

    // Apply ownership recursively to copied content
    const ownershipError = await applyOwnershipRecursive(toResult.realPath, ownership);
    if (ownershipError) {
      await rm(toResult.realPath, { recursive: true }).catch(() => {});
      return c.json({ error: ownershipError }, 500);
    }

    return c.json(await getFileInfo(toResult.realPath));
  },
);

export default app;
