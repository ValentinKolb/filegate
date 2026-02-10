import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { Glob } from "bun";
import { stat } from "node:fs/promises";
import { join, basename, relative } from "node:path";
import { validatePath } from "../lib/path";
import { jsonResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
import {
  SearchResponseSchema,
  ErrorSchema,
  SearchQuerySchema,
  countRecursiveWildcards,
  type FileInfo,
  type SearchResult,
} from "../schemas";
import { config } from "../config";
import { enrichFileInfoBatch } from "../lib/index";

const app = new Hono();

const getFileInfo = async (fullPath: string, basePath: string): Promise<FileInfo | null> => {
  try {
    const s = await stat(fullPath);
    const name = basename(fullPath);
    const file = Bun.file(fullPath);

    return {
      name,
      path: relative(basePath, fullPath),
      type: s.isDirectory() ? "directory" : "file",
      size: s.isDirectory() ? 0 : s.size,
      mtime: s.mtime.toISOString(),
      isHidden: name.startsWith("."),
      mimeType: s.isDirectory() ? undefined : file.type,
    };
  } catch {
    return null;
  }
};

const searchInPath = async (
  basePath: string,
  pattern: string,
  showHidden: boolean,
  limit: number,
  includeFiles: boolean,
  includeDirectories: boolean,
): Promise<SearchResult> => {
  const glob = new Glob(pattern);
  const files: FileInfo[] = [];
  let hasMore = false;

  // If we need directories, we must set onlyFiles: false
  const onlyFiles = includeFiles && !includeDirectories;

  for await (const match of glob.scan({ cwd: basePath, dot: showHidden, onlyFiles })) {
    if (!showHidden && basename(match).startsWith(".")) continue;

    if (files.length >= limit) {
      hasMore = true;
      break;
    }

    const info = await getFileInfo(join(basePath, match), basePath);
    if (!info) continue;

    // Filter based on type
    if (info.type === "file" && !includeFiles) continue;
    if (info.type === "directory" && !includeDirectories) continue;

    files.push(info);
  }

  return { basePath, files, total: files.length, hasMore };
};

app.get(
  "/search",
  describeRoute({
    tags: ["Search"],
    summary: "Search files with glob pattern",
    description: "Search multiple paths in parallel using glob patterns",
    ...requiresAuth,
    responses: {
      200: jsonResponse(SearchResponseSchema, "Search results"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("query", SearchQuerySchema),
  async (c) => {
    const { paths: pathsParam, pattern, showHidden, limit: limitParam, files, directories } = c.req.valid("query");

    // At least one of files or directories must be true
    if (!files && !directories) {
      return c.json({ error: "at least one of 'files' or 'directories' must be true" }, 400);
    }

    // Validate recursive wildcard count
    const wildcardCount = countRecursiveWildcards(pattern);
    if (wildcardCount > config.searchMaxRecursiveWildcards) {
      return c.json(
        { error: `too many recursive wildcards: ${wildcardCount} (max ${config.searchMaxRecursiveWildcards})` },
        400,
      );
    }

    const limit = Math.min(limitParam ?? config.searchMaxResults, config.searchMaxResults);
    const paths = pathsParam
      .split(",")
      .map((p) => p.trim())
      .filter(Boolean);
    const validPaths: string[] = [];

    for (const p of paths) {
      const result = await validatePath(p, { allowBasePath: true });
      if (!result.ok) return c.json({ error: result.error }, result.status);

      const s = await stat(result.realPath).catch(() => null);
      if (!s) return c.json({ error: `path not found: ${p}` }, 404);
      if (!s.isDirectory()) return c.json({ error: `not a directory: ${p}` }, 400);

      validPaths.push(result.realPath);
    }

    const results = await Promise.all(
      validPaths.map((p) => searchInPath(p, pattern, showHidden, limit, files, directories)),
    );

    if (config.indexEnabled) {
      for (const result of results) {
        result.files = await enrichFileInfoBatch(result.files, result.basePath);
      }
    }

    const totalFiles = results.reduce((sum, r) => sum + r.total, 0);

    return c.json({ results, totalFiles });
  },
);

export default app;
