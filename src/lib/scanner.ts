import { readdir, stat } from "node:fs/promises";
import { join, relative } from "node:path";
import { config } from "../config";
import { validatePath } from "./path";
import {
  indexFile,
  getScanState,
  setScanState,
  touchIndexedAtUnderDir,
  removeStaleEntries,
} from "./index";

export type ScanResult = {
  scanned: number;
  skipped: number;
  added: number;
  moved: number;
  removed: number;
  durationMs: number;
};

const emptyResult = (): ScanResult => ({
  scanned: 0,
  skipped: 0,
  added: 0,
  moved: 0,
  removed: 0,
  durationMs: 0,
});

export const scanBasePath = async (basePath: string): Promise<ScanResult> => {
  const start = performance.now();
  const scanStart = Date.now();
  const result = emptyResult();

  const queue: string[] = [basePath];
  const concurrency = Math.max(1, config.indexScanConcurrency);

  const processDir = async (dirPath: string) => {
    const dirStat = await stat(dirPath).catch(() => null);
    if (!dirStat || !dirStat.isDirectory()) return;

    const relDir = relative(basePath, dirPath);
    const scanState = await getScanState(basePath, relDir);

    if (scanState && scanState.mtimeMs === dirStat.mtimeMs) {
      result.skipped++;
      await touchIndexedAtUnderDir(basePath, relDir, scanStart);
      await setScanState(basePath, relDir, dirStat.mtimeMs, scanStart);
      return;
    }

    result.scanned++;

    const entries = await readdir(dirPath, { withFileTypes: true });
    for (const entry of entries) {
      const entryPath = join(dirPath, entry.name);
      const entryStat = await stat(entryPath).catch(() => null);
      if (!entryStat) continue;

      const relPath = relative(basePath, entryPath);
      const outcome = await indexFile(
        basePath,
        relPath,
        {
          dev: entryStat.dev,
          ino: entryStat.ino,
          size: entryStat.size,
          mtimeMs: entryStat.mtimeMs,
          isDirectory: entryStat.isDirectory(),
        },
        scanStart,
      );

      if (outcome.action === "added") result.added++;
      if (outcome.action === "moved") result.moved++;

      if (entry.isDirectory()) {
        queue.push(entryPath);
      }
    }

    await setScanState(basePath, relDir, dirStat.mtimeMs, scanStart);
  };

  const workers = Array.from({ length: concurrency }, async () => {
    while (true) {
      const dir = queue.shift();
      if (!dir) break;
      await processDir(dir);
    }
  });

  await Promise.all(workers);

  result.removed = await removeStaleEntries(basePath, scanStart);
  result.durationMs = Math.round(performance.now() - start);

  return result;
};

export const scanAll = async (): Promise<ScanResult> => {
  const start = performance.now();
  const aggregate = emptyResult();

  for (const base of config.allowedPaths) {
    const baseResult = await validatePath(base, { allowBasePath: true });
    if (!baseResult.ok) {
      throw new Error(`invalid base path: ${baseResult.error}`);
    }
    const result = await scanBasePath(baseResult.basePath);
    aggregate.scanned += result.scanned;
    aggregate.skipped += result.skipped;
    aggregate.added += result.added;
    aggregate.moved += result.moved;
    aggregate.removed += result.removed;
  }

  aggregate.durationMs = Math.round(performance.now() - start);
  return aggregate;
};
