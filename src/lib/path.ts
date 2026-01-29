import { realpath, mkdir } from "node:fs/promises";
import { join, normalize, dirname, basename } from "node:path";
import { config } from "../config";
import { applyOwnershipToNewDirs, type Ownership } from "./ownership";

export type PathResult =
  | { ok: true; realPath: string; basePath: string }
  | { ok: false; error: string; status: 400 | 403 | 404 };

// Cache for resolved base paths (they don't change at runtime)
const realBaseCache = new Map<string, string>();

const getRealBase = async (basePath: string): Promise<string | null> => {
  const cached = realBaseCache.get(basePath);
  if (cached) return cached;

  try {
    const realBase = await realpath(basePath);
    realBaseCache.set(basePath, realBase);
    return realBase;
  } catch {
    return null;
  }
};

export type ValidatePathOptions = {
  /** If true, allows operating on the base path itself */
  allowBasePath?: boolean;
  /** If true, creates parent directories if they don't exist */
  createParents?: boolean;
  /** Ownership to apply to newly created directories */
  ownership?: Ownership | null;
};

/**
 * Validates path is within allowed base paths, resolves symlinks.
 * Optionally creates parent directories with ownership.
 */
export const validatePath = async (path: string, options: ValidatePathOptions = {}): Promise<PathResult> => {
  const { allowBasePath = false, createParents = false, ownership = null } = options;
  const cleaned = normalize(path);

  // Find matching base
  let basePath: string | null = null;
  for (const base of config.allowedPaths) {
    const cleanBase = normalize(base);
    if (cleaned === cleanBase) {
      if (!allowBasePath) {
        return { ok: false, error: "cannot operate on base path", status: 403 };
      }
      basePath = cleanBase;
      break;
    }
    if (cleaned.startsWith(cleanBase + "/")) {
      basePath = cleanBase;
      break;
    }
  }

  if (!basePath) {
    return { ok: false, error: "path not allowed", status: 403 };
  }

  // Create parent directories if requested (AFTER base validation)
  if (createParents) {
    const parentPath = dirname(cleaned);
    await mkdir(parentPath, { recursive: true });

    // Apply ownership to newly created directories
    if (ownership) {
      const realBase = await getRealBase(basePath);
      if (realBase) {
        await applyOwnershipToNewDirs(parentPath, realBase, ownership);
      }
    }
  }

  // Resolve symlinks
  let realPath: string;
  try {
    realPath = await realpath(cleaned);
  } catch (e: any) {
    if (e.code === "ENOENT") {
      // Path doesn't exist yet - validate parent
      try {
        const parentReal = await realpath(dirname(cleaned));
        realPath = join(parentReal, basename(cleaned));
      } catch {
        return { ok: false, error: "parent path not found", status: 404 };
      }
    } else {
      return { ok: false, error: "path resolution failed", status: 400 };
    }
  }

  // Verify still within base after symlink resolution (cached)
  const realBase = await getRealBase(basePath);
  if (!realBase) {
    return { ok: false, error: "base path invalid", status: 500 as any };
  }

  if (realPath !== realBase && !realPath.startsWith(realBase + "/")) {
    return { ok: false, error: "symlink escape not allowed", status: 403 };
  }

  return { ok: true, realPath, basePath: realBase };
};

export type SameBaseResult =
  | { ok: true; realPath: string; basePath: string; realTo: string }
  | { ok: false; error: string; status: 400 | 403 | 404 };

/**
 * Validates both paths are in the same base (for move/copy).
 */
export const validateSameBase = async (from: string, to: string): Promise<SameBaseResult> => {
  const fromResult = await validatePath(from);
  if (!fromResult.ok) return fromResult;

  const toResult = await validatePath(to);
  if (!toResult.ok) return toResult;

  if (fromResult.basePath !== toResult.basePath) {
    return { ok: false, error: "cross-basepath not allowed", status: 400 };
  }

  return { ...fromResult, realTo: toResult.realPath };
};
