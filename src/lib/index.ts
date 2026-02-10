import { SQL } from "bun";
import type { FileInfo } from "../schemas";

type StatInfo = {
  dev: number;
  ino: number;
  size: number;
  mtimeMs: number;
  isDirectory: boolean;
};

export type IndexOutcome = {
  id: string;
  action: "existing" | "moved" | "added";
};

export type IndexStats = {
  totalFiles: number;
  totalDirs: number;
  dbSizeBytes: number;
  lastScanAt: number | null;
};

export type ScanState = {
  mtimeMs: number;
  scannedAt: number;
};

let sql: InstanceType<typeof SQL> | null = null;

const getSql = (): InstanceType<typeof SQL> => {
  if (!sql) throw new Error("index not initialized");
  return sql;
};

const isSqliteUrl = (url: string): boolean => {
  return url === ":memory:" || url.startsWith("sqlite:") || url.startsWith("file:");
};

const escapeLike = (value: string): string => value.replace(/[\\%_]/g, "\\$&");

// --- Init ---
export const initIndex = async (databaseUrl: string): Promise<void> => {
  sql = new SQL(databaseUrl);

  if (isSqliteUrl(databaseUrl)) {
    await sql`PRAGMA journal_mode = WAL`.catch(() => {});
    await sql`PRAGMA synchronous = NORMAL`.catch(() => {});
  }

  await sql`
    CREATE TABLE IF NOT EXISTS file_index (
      id TEXT PRIMARY KEY,
      base_path TEXT NOT NULL,
      rel_path TEXT NOT NULL,
      dev INTEGER NOT NULL,
      ino INTEGER NOT NULL,
      size INTEGER NOT NULL,
      mtime_ms INTEGER NOT NULL,
      is_dir INTEGER NOT NULL DEFAULT 0,
      indexed_at INTEGER NOT NULL,
      UNIQUE(base_path, rel_path)
    )
  `;

  await sql`CREATE INDEX IF NOT EXISTS idx_file_dev_ino ON file_index(dev, ino)`;
  await sql`CREATE INDEX IF NOT EXISTS idx_file_base ON file_index(base_path)`;

  await sql`
    CREATE TABLE IF NOT EXISTS scan_state (
      base_path TEXT NOT NULL,
      dir_path TEXT NOT NULL,
      mtime_ms INTEGER NOT NULL,
      scanned_at INTEGER NOT NULL,
      PRIMARY KEY(base_path, dir_path)
    )
  `;
};

export const closeIndex = async (): Promise<void> => {
  await sql?.close();
  sql = null;
};

// --- UUID v7 Generator ---
export const generateId = (): string => {
  const bytes = new Uint8Array(16);
  const now = Date.now();

  let time = now;
  for (let i = 5; i >= 0; i--) {
    bytes[i] = time & 0xff;
    time = Math.floor(time / 256);
  }

  globalThis.crypto.getRandomValues(bytes.subarray(6));

  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x70;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;

  const hex = Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");

  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
};

// --- Core CRUD ---
export const indexFile = async (
  basePath: string,
  relPath: string,
  s: StatInfo,
  indexedAt: number = Date.now(),
): Promise<IndexOutcome> => {
  const db = getSql();

  const [existingByPath] = await db`
    SELECT id FROM file_index WHERE base_path = ${basePath} AND rel_path = ${relPath}
  `;

  if (existingByPath) {
    await db`
      UPDATE file_index
      SET dev = ${s.dev}, ino = ${s.ino}, size = ${s.size}, mtime_ms = ${s.mtimeMs},
          is_dir = ${s.isDirectory ? 1 : 0}, indexed_at = ${indexedAt}
      WHERE id = ${existingByPath.id}
    `;
    return { id: existingByPath.id, action: "existing" };
  }

  const [existingByInode] = await db`
    SELECT id FROM file_index WHERE dev = ${s.dev} AND ino = ${s.ino}
  `;

  if (existingByInode) {
    await db`
      UPDATE file_index
      SET base_path = ${basePath}, rel_path = ${relPath}, size = ${s.size}, mtime_ms = ${s.mtimeMs},
          is_dir = ${s.isDirectory ? 1 : 0}, indexed_at = ${indexedAt}
      WHERE id = ${existingByInode.id}
    `;
    return { id: existingByInode.id, action: "moved" };
  }

  const id = generateId();
  await db`
    INSERT INTO file_index (id, base_path, rel_path, dev, ino, size, mtime_ms, is_dir, indexed_at)
    VALUES (${id}, ${basePath}, ${relPath}, ${s.dev}, ${s.ino}, ${s.size}, ${s.mtimeMs},
            ${s.isDirectory ? 1 : 0}, ${indexedAt})
  `;

  return { id, action: "added" };
};

export const resolveId = async (id: string): Promise<{ basePath: string; relPath: string } | null> => {
  const db = getSql();
  const [row] = await db`SELECT base_path, rel_path FROM file_index WHERE id = ${id}`;
  return row ? { basePath: row.base_path, relPath: row.rel_path } : null;
};

export const identifyPath = async (basePath: string, relPath: string): Promise<string | null> => {
  const db = getSql();
  const [row] = await db`SELECT id FROM file_index WHERE base_path = ${basePath} AND rel_path = ${relPath}`;
  return row?.id ?? null;
};

export const updateIndexPath = async (id: string, newBasePath: string, newRelPath: string): Promise<void> => {
  const db = getSql();
  await db`UPDATE file_index SET base_path = ${newBasePath}, rel_path = ${newRelPath} WHERE id = ${id}`;
};

export const removeFromIndex = async (basePath: string, relPath: string): Promise<void> => {
  const db = getSql();
  await db`DELETE FROM file_index WHERE base_path = ${basePath} AND rel_path = ${relPath}`;
};

export const removeFromIndexRecursive = async (basePath: string, relPath: string): Promise<void> => {
  const db = getSql();

  if (!relPath) {
    await db`DELETE FROM file_index WHERE base_path = ${basePath}`;
    return;
  }

  const prefix = `${escapeLike(relPath)}/`;
  const likePattern = `${prefix}%`;

  await db`
    DELETE FROM file_index
    WHERE base_path = ${basePath}
      AND (rel_path = ${relPath} OR rel_path LIKE ${likePattern} ESCAPE '\\')
  `;
};

// --- Scan State ---
export const getScanState = async (basePath: string, dirPath: string): Promise<ScanState | null> => {
  const db = getSql();
  const [row] = await db`
    SELECT mtime_ms, scanned_at FROM scan_state WHERE base_path = ${basePath} AND dir_path = ${dirPath}
  `;
  return row ? { mtimeMs: row.mtime_ms, scannedAt: row.scanned_at } : null;
};

export const setScanState = async (basePath: string, dirPath: string, mtimeMs: number, scannedAt: number): Promise<void> => {
  const db = getSql();
  const [row] = await db`
    SELECT 1 as present FROM scan_state WHERE base_path = ${basePath} AND dir_path = ${dirPath}
  `;
  if (row) {
    await db`
      UPDATE scan_state SET mtime_ms = ${mtimeMs}, scanned_at = ${scannedAt}
      WHERE base_path = ${basePath} AND dir_path = ${dirPath}
    `;
    return;
  }

  await db`
    INSERT INTO scan_state (base_path, dir_path, mtime_ms, scanned_at)
    VALUES (${basePath}, ${dirPath}, ${mtimeMs}, ${scannedAt})
  `;
};

export const touchIndexedAtUnderDir = async (
  basePath: string,
  dirPath: string,
  indexedAt: number,
): Promise<void> => {
  const db = getSql();

  if (!dirPath) {
    await db`UPDATE file_index SET indexed_at = ${indexedAt} WHERE base_path = ${basePath}`;
    return;
  }

  const prefix = `${escapeLike(dirPath)}/`;
  const likePattern = `${prefix}%`;

  await db`
    UPDATE file_index
    SET indexed_at = ${indexedAt}
    WHERE base_path = ${basePath}
      AND (rel_path = ${dirPath} OR rel_path LIKE ${likePattern} ESCAPE '\\')
  `;
};

export const removeStaleEntries = async (basePath: string, beforeMs: number): Promise<number> => {
  const db = getSql();
  const [countRow] = await db`
    SELECT COUNT(*) as count FROM file_index WHERE base_path = ${basePath} AND indexed_at < ${beforeMs}
  `;
  const count = Number(countRow?.count ?? 0);
  await db`DELETE FROM file_index WHERE base_path = ${basePath} AND indexed_at < ${beforeMs}`;
  return count;
};

// --- Batch ---
export const bulkResolve = async (
  ids: string[],
): Promise<Record<string, { basePath: string; relPath: string } | null>> => {
  if (ids.length === 0) return {};

  const db = getSql();
  const rows = await db`SELECT id, base_path, rel_path FROM file_index WHERE id IN ${db(ids)}`;
  const map = new Map<string, { basePath: string; relPath: string }>();

  for (const row of rows) {
    map.set(row.id, { basePath: row.base_path, relPath: row.rel_path });
  }

  const result: Record<string, { basePath: string; relPath: string } | null> = {};
  for (const id of ids) {
    result[id] = map.get(id) ?? null;
  }

  return result;
};

export const enrichFileInfo = async (info: FileInfo, basePath: string): Promise<FileInfo> => {
  const fileId = await identifyPath(basePath, info.path);
  return fileId ? { ...info, fileId } : info;
};

export const enrichFileInfoBatch = async (infos: FileInfo[], basePath: string): Promise<FileInfo[]> => {
  if (infos.length === 0) return infos;
  const db = getSql();
  const paths = infos.map((i) => i.path);
  const rows = await db`
    SELECT rel_path, id FROM file_index WHERE base_path = ${basePath} AND rel_path IN ${db(paths)}
  `;
  const pathToId = new Map<string, string>();
  for (const row of rows as { rel_path: string; id: string }[]) {
    pathToId.set(row.rel_path, row.id);
  }
  return infos.map((info) => {
    const fileId = pathToId.get(info.path);
    return fileId ? { ...info, fileId } : info;
  });
};

// --- Stats ---
export const getIndexStats = async (): Promise<IndexStats> => {
  const db = getSql();

  const [fileRow] = await db`SELECT COUNT(*) as count FROM file_index WHERE is_dir = 0`;
  const [dirRow] = await db`SELECT COUNT(*) as count FROM file_index WHERE is_dir = 1`;
  const [scanRow] = await db`SELECT MAX(scanned_at) as last_scan_at FROM scan_state`;

  let dbSizeBytes = 0;
  try {
    const [pageCountRow] = await db`PRAGMA page_count`;
    const [pageSizeRow] = await db`PRAGMA page_size`;
    const pageCount = Number(pageCountRow?.page_count ?? 0);
    const pageSize = Number(pageSizeRow?.page_size ?? 0);
    dbSizeBytes = pageCount * pageSize;
  } catch {
    dbSizeBytes = 0;
  }

  return {
    totalFiles: Number(fileRow?.count ?? 0),
    totalDirs: Number(dirRow?.count ?? 0),
    dbSizeBytes,
    lastScanAt: scanRow?.last_scan_at ?? null,
  };
};
