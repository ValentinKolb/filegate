import { describe, test, expect, beforeAll, afterAll } from "bun:test";
import { mkdir, writeFile, symlink, rm, realpath } from "node:fs/promises";
import { join } from "node:path";
import { validatePath, validateSameBase } from "../../src/lib/path";

// Use /private/tmp on macOS since /tmp is a symlink
const TEST_BASE_INPUT = "/tmp/filegate-test";
let TEST_BASE: string;
let TEST_SUBDIR: string;
let TEST_FILE: string;
let TEST_SYMLINK_INTERNAL: string;
let TEST_SYMLINK_ESCAPE: string;

describe("path validation", () => {
  beforeAll(async () => {
    // Create test directory structure
    await mkdir(join(TEST_BASE_INPUT, "subdir"), { recursive: true });

    // Resolve the real path (handles /tmp -> /private/tmp on macOS)
    TEST_BASE = await realpath(TEST_BASE_INPUT);
    TEST_SUBDIR = join(TEST_BASE, "subdir");
    TEST_FILE = join(TEST_SUBDIR, "file.txt");
    TEST_SYMLINK_INTERNAL = join(TEST_BASE, "link-internal");
    TEST_SYMLINK_ESCAPE = join(TEST_BASE, "link-escape");

    await writeFile(TEST_FILE, "test content");

    // Create symlink pointing within allowed base
    try {
      await symlink(TEST_SUBDIR, TEST_SYMLINK_INTERNAL);
    } catch {
      // Symlink might already exist
    }

    // Create symlink pointing outside allowed base (escape attempt)
    // Points to /var which should be outside our allowed base
    try {
      await symlink("/var", TEST_SYMLINK_ESCAPE);
    } catch {
      // Symlink might already exist
    }
  });

  afterAll(async () => {
    // Cleanup
    await rm(TEST_BASE, { recursive: true, force: true });
  });

  describe("validatePath", () => {
    test("should allow path within allowed base", async () => {
      const result = await validatePath(TEST_FILE);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.realPath).toBe(TEST_FILE);
        expect(result.basePath).toBe(TEST_BASE);
      }
    });

    test("should allow subdirectory within allowed base", async () => {
      const result = await validatePath(TEST_SUBDIR);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.realPath).toBe(TEST_SUBDIR);
      }
    });

    test("should reject path outside allowed base", async () => {
      const result = await validatePath("/etc/passwd");
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("path not allowed");
        expect(result.status).toBe(403);
      }
    });

    test("should reject operating on base path directly by default", async () => {
      const result = await validatePath(TEST_BASE);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("cannot operate on base path");
        expect(result.status).toBe(403);
      }
    });

    test("should allow operating on base path when allowBasePath=true", async () => {
      const result = await validatePath(TEST_BASE, true);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.realPath).toBe(TEST_BASE);
      }
    });

    test("should allow symlink pointing within allowed base", async () => {
      const result = await validatePath(TEST_SYMLINK_INTERNAL);
      expect(result.ok).toBe(true);
      if (result.ok) {
        // realPath should be the resolved symlink target
        expect(result.realPath).toBe(TEST_SUBDIR);
      }
    });

    test("should reject symlink escape attempt", async () => {
      const result = await validatePath(TEST_SYMLINK_ESCAPE);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("symlink escape not allowed");
        expect(result.status).toBe(403);
      }
    });

    test("should allow non-existent file if parent exists", async () => {
      const nonExistent = join(TEST_SUBDIR, "new-file.txt");
      const result = await validatePath(nonExistent);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.realPath).toBe(nonExistent);
      }
    });

    test("should reject non-existent file with non-existent parent", async () => {
      const nonExistent = join(TEST_BASE, "nonexistent", "file.txt");
      const result = await validatePath(nonExistent);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("parent path not found");
        expect(result.status).toBe(404);
      }
    });

    test("should normalize paths with .. segments", async () => {
      const pathWithDots = join(TEST_SUBDIR, "..", "subdir", "file.txt");
      const result = await validatePath(pathWithDots);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.realPath).toBe(TEST_FILE);
      }
    });

    test("should reject path traversal escaping base via ..", async () => {
      // This attempts to escape via ..
      const escapePath = join(TEST_BASE, "..", "passwd");
      const result = await validatePath(escapePath);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("path not allowed");
        expect(result.status).toBe(403);
      }
    });
  });

  describe("validateSameBase", () => {
    test("should allow move within same base", async () => {
      const from = TEST_FILE;
      const to = join(TEST_SUBDIR, "renamed.txt");
      const result = await validateSameBase(from, to);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.realPath).toBe(from);
        expect(result.realTo).toBe(to);
        expect(result.basePath).toBe(TEST_BASE);
      }
    });

    test("should reject if source path is invalid", async () => {
      const result = await validateSameBase("/etc/passwd", TEST_FILE);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("path not allowed");
      }
    });

    test("should reject if destination path is invalid", async () => {
      const result = await validateSameBase(TEST_FILE, "/etc/passwd");
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.error).toBe("path not allowed");
      }
    });
  });
});
