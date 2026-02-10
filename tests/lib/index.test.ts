import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import {
  initIndex,
  closeIndex,
  generateId,
  indexFile,
  identifyPath,
  resolveId,
  bulkResolve,
  removeFromIndexRecursive,
} from "../../src/lib/index";

const BASE = "/tmp/filegate-test";

describe("index core", () => {
  beforeEach(async () => {
    await initIndex("sqlite://:memory:");
  });

  afterEach(async () => {
    await closeIndex();
  });

  test("generateId returns UUID v7 format", () => {
    const id = generateId();
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  test("indexFile returns same id for existing path", async () => {
    const first = await indexFile(BASE, "a.txt", {
      dev: 1,
      ino: 100,
      size: 10,
      mtimeMs: 1,
      isDirectory: false,
    });

    const second = await indexFile(BASE, "a.txt", {
      dev: 1,
      ino: 200,
      size: 20,
      mtimeMs: 2,
      isDirectory: false,
    });

    expect(first.action).toBe("added");
    expect(second.action).toBe("existing");
    expect(second.id).toBe(first.id);
  });

  test("indexFile detects move by dev/ino", async () => {
    const first = await indexFile(BASE, "old/name.txt", {
      dev: 2,
      ino: 300,
      size: 10,
      mtimeMs: 1,
      isDirectory: false,
    });

    const second = await indexFile(BASE, "new/name.txt", {
      dev: 2,
      ino: 300,
      size: 12,
      mtimeMs: 2,
      isDirectory: false,
    });

    expect(second.action).toBe("moved");
    expect(second.id).toBe(first.id);

    const resolved = await resolveId(first.id);
    expect(resolved).toEqual({ basePath: BASE, relPath: "new/name.txt" });
  });

  test("bulkResolve returns null for missing ids", async () => {
    const first = await indexFile(BASE, "a.txt", {
      dev: 3,
      ino: 400,
      size: 1,
      mtimeMs: 1,
      isDirectory: false,
    });

    const result = await bulkResolve([first.id, "missing-id"]);
    expect(result[first.id]).toEqual({ basePath: BASE, relPath: "a.txt" });
    expect(result["missing-id"]).toBeNull();
  });

  test("removeFromIndexRecursive escapes like patterns", async () => {
    await indexFile(BASE, "dir%1/file.txt", {
      dev: 4,
      ino: 500,
      size: 1,
      mtimeMs: 1,
      isDirectory: false,
    });

    await indexFile(BASE, "dir_1/file.txt", {
      dev: 4,
      ino: 501,
      size: 1,
      mtimeMs: 1,
      isDirectory: false,
    });

    await indexFile(BASE, "dirX1/file.txt", {
      dev: 4,
      ino: 502,
      size: 1,
      mtimeMs: 1,
      isDirectory: false,
    });

    await removeFromIndexRecursive(BASE, "dir%1");

    expect(await identifyPath(BASE, "dir%1/file.txt")).toBeNull();
    expect(await identifyPath(BASE, "dir_1/file.txt")).not.toBeNull();
    expect(await identifyPath(BASE, "dirX1/file.txt")).not.toBeNull();
  });
});
