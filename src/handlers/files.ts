import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { readdir, mkdir, rm, rename, cp, stat } from "node:fs/promises";
import { join, basename, relative } from "node:path";
import sanitizeFilename from "sanitize-filename";
import { validatePath, validateSameBase } from "../lib/path";
import { parseOwnershipBody, applyOwnership } from "../lib/ownership";
import { jsonResponse, binaryResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
import {
  FileInfoSchema,
  DirInfoSchema,
  ErrorSchema,
  InfoQuerySchema,
  PathQuerySchema,
  MkdirBodySchema,
  MoveBodySchema,
  CopyBodySchema,
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

const getFileInfo = async (path: string, relativeTo?: string): Promise<FileInfo> => {
  const file = Bun.file(path);
  const s = await stat(path);
  const name = basename(path);

  return {
    name,
    path: relativeTo ? relative(relativeTo, path) : path,
    type: s.isDirectory() ? "directory" : "file",
    size: s.isDirectory() ? 0 : s.size,
    mtime: s.mtime.toISOString(),
    isHidden: name.startsWith("."),
    mimeType: s.isDirectory() ? undefined : file.type,
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

    const result = await validatePath(path, true);
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
          .map((e) => getFileInfo(join(result.realPath, e.name), result.realPath).catch(() => null)),
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
    description: "Downloads a file directly or a directory as a TAR archive. Size limit applies to both.",
    ...requiresAuth,
    responses: {
      200: binaryResponse("application/octet-stream", "File content or TAR archive"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
      413: jsonResponse(ErrorSchema, "Content too large"),
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

    return new Response(file.stream(), {
      headers: {
        "Content-Type": file.type,
        "Content-Length": String(file.size),
        "X-File-Name": basename(result.realPath),
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
    const result = await validatePath(fullPath);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    // Build ownership from validated headers
    const ownership: import("../lib/ownership").Ownership | null =
      headers["x-owner-uid"] != null && headers["x-owner-gid"] != null && headers["x-file-mode"] != null
        ? {
            uid: headers["x-owner-uid"],
            gid: headers["x-owner-gid"],
            mode: parseInt(headers["x-file-mode"], 8),
          }
        : null;

    await mkdir(dirPath, { recursive: true });

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

// POST /move
app.post(
  "/move",
  describeRoute({
    tags: ["Files"],
    summary: "Move file or directory",
    description: "Move within same base path only",
    ...requiresAuth,
    responses: {
      200: jsonResponse(FileInfoSchema, "Moved"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("json", MoveBodySchema),
  async (c) => {
    const { from, to } = c.req.valid("json");

    const result = await validateSameBase(from, to);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    try {
      await stat(result.realPath);
    } catch {
      return c.json({ error: "source not found" }, 404);
    }

    await mkdir(join(result.realTo, ".."), { recursive: true });
    await rename(result.realPath, result.realTo);

    return c.json(await getFileInfo(result.realTo));
  },
);

// POST /copy
app.post(
  "/copy",
  describeRoute({
    tags: ["Files"],
    summary: "Copy file or directory",
    description: "Copy within same base path only",
    ...requiresAuth,
    responses: {
      200: jsonResponse(FileInfoSchema, "Copied"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("json", CopyBodySchema),
  async (c) => {
    const { from, to } = c.req.valid("json");

    const result = await validateSameBase(from, to);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    try {
      await stat(result.realPath);
    } catch {
      return c.json({ error: "source not found" }, 404);
    }

    await mkdir(join(result.realTo, ".."), { recursive: true });
    await cp(result.realPath, result.realTo, { recursive: true });

    return c.json(await getFileInfo(result.realTo));
  },
);

export default app;
