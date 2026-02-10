const required = (key: string): string => {
  const value = process.env[key];
  if (!value) throw new Error(`Missing required env: ${key}`);
  return value;
};

const int = (key: string, def: number): number => parseInt(process.env[key] ?? "", 10) || def;

const optionalInt = (key: string): number | undefined => {
  const v = process.env[key];
  return v ? parseInt(v, 10) : undefined;
};

const list = (key: string): string[] =>
  required(key)
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

export const config = {
  token: required("FILE_PROXY_TOKEN"),
  allowedPaths: list("ALLOWED_BASE_PATHS"),
  port: int("PORT", 4000),
  maxUploadBytes: int("MAX_UPLOAD_MB", 500) * 1024 * 1024,
  maxDownloadBytes: int("MAX_DOWNLOAD_MB", 5000) * 1024 * 1024,
  maxChunkBytes: int("MAX_CHUNK_SIZE_MB", 50) * 1024 * 1024,
  searchMaxResults: int("SEARCH_MAX_RESULTS", 100),
  searchMaxRecursiveWildcards: int("SEARCH_MAX_RECURSIVE_WILDCARDS", 10),
  uploadExpirySecs: int("UPLOAD_EXPIRY_HOURS", 24) * 60 * 60,
  uploadTempDir: process.env.UPLOAD_TEMP_DIR ?? "/tmp/filegate-uploads",
  diskCleanupIntervalMs: int("DISK_CLEANUP_INTERVAL_HOURS", 6) * 60 * 60 * 1000,
  indexEnabled: process.env.ENABLE_INDEX !== "false",
  indexDatabaseUrl: process.env.INDEX_DATABASE_URL ?? "sqlite://:memory:",
  indexRescanIntervalMs: int("INDEX_RESCAN_INTERVAL_MINUTES", 30) * 60 * 1000,
  indexScanConcurrency: int("INDEX_SCAN_CONCURRENCY", 4),
  devUid: optionalInt("DEV_UID_OVERRIDE"),
  devGid: optionalInt("DEV_GID_OVERRIDE"),
  get isDev() {
    return this.devUid !== undefined || this.devGid !== undefined;
  },
} as const;
