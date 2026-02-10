import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdir, writeFile, rm, realpath, rename } from "node:fs/promises";
import { join } from "node:path";
import { initIndex, closeIndex, identifyPath } from "../../src/lib/index";
import { scanBasePath } from "../../src/lib/scanner";

let basePath: string;
let filePath: string;

describe("scanner", () => {
  beforeEach(async () => {
    await initIndex("sqlite://:memory:");
    const baseInput = "/tmp/filegate-test/scanner";
    await mkdir(baseInput, { recursive: true });
    basePath = await realpath(baseInput);
    filePath = join(basePath, "file.txt");
    await writeFile(filePath, "hello");
  });

  afterEach(async () => {
    await closeIndex();
    if (basePath) {
      await rm(basePath, { recursive: true, force: true });
    }
  });

  test("initial scan indexes files", async () => {
    const result = await scanBasePath(basePath);
    expect(result.added).toBeGreaterThan(0);
    const id = await identifyPath(basePath, "file.txt");
    expect(id).not.toBeNull();
  });

  test("unchanged scan skips directories", async () => {
    await scanBasePath(basePath);
    const result = await scanBasePath(basePath);
    expect(result.skipped).toBeGreaterThan(0);
    expect(result.added).toBe(0);
    expect(result.moved).toBe(0);
  });

  test("deleted file is removed as stale", async () => {
    await scanBasePath(basePath);
    await new Promise((r) => setTimeout(r, 1100));
    await rm(filePath);
    const result = await scanBasePath(basePath);
    expect(result.removed).toBe(1);
    const id = await identifyPath(basePath, "file.txt");
    expect(id).toBeNull();
  });

  test("move within base preserves id", async () => {
    await scanBasePath(basePath);
    const originalId = await identifyPath(basePath, "file.txt");
    expect(originalId).not.toBeNull();

    await new Promise((r) => setTimeout(r, 1100));
    const newPath = join(basePath, "renamed.txt");
    await rename(filePath, newPath);

    const result = await scanBasePath(basePath);
    expect(result.moved).toBeGreaterThan(0);

    const movedId = await identifyPath(basePath, "renamed.txt");
    expect(movedId).toBe(originalId);
  });
});
