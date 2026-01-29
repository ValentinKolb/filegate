import { z } from "zod";

// ============================================================================
// Common
// ============================================================================

export const ErrorSchema = z.object({
  error: z.string(),
});

export const FileTypeSchema = z.enum(["file", "directory"]);

export const FileInfoSchema = z.object({
  name: z.string(),
  path: z.string(),
  type: FileTypeSchema,
  size: z.number(),
  mtime: z.iso.datetime(),
  isHidden: z.boolean(),
  mimeType: z.string().optional(),
});

export const DirInfoSchema = FileInfoSchema.extend({
  items: z.array(FileInfoSchema),
  total: z.number(),
});

// ============================================================================
// Query Params
// ============================================================================

export const PathQuerySchema = z.object({
  path: z.string().min(1),
});

export const ContentQuerySchema = z.object({
  path: z.string().min(1),
  inline: z
    .string()
    .optional()
    .transform((v) => v === "true"), // default: false (attachment)
});

export const InfoQuerySchema = z.object({
  path: z.string().min(1),
  showHidden: z
    .string()
    .optional()
    .transform((v) => v === "true"),
});

export const SearchQuerySchema = z.object({
  paths: z.string().min(1),
  pattern: z.string().min(1).max(500),
  showHidden: z
    .string()
    .optional()
    .transform((v) => v === "true"),
  limit: z
    .string()
    .optional()
    .transform((v) => (v ? parseInt(v, 10) : undefined)),
  files: z
    .string()
    .optional()
    .transform((v) => v !== "false"), // default: true
  directories: z
    .string()
    .optional()
    .transform((v) => v === "true"), // default: false
});

/** Count recursive wildcards (**) in a glob pattern */
export const countRecursiveWildcards = (pattern: string): number => {
  return (pattern.match(/\*\*/g) || []).length;
};

// ============================================================================
// Request Bodies
// ============================================================================

export const MkdirBodySchema = z.object({
  path: z.string().min(1),
  ownerUid: z.number().int().optional(),
  ownerGid: z.number().int().optional(),
  mode: z
    .string()
    .regex(/^[0-7]{3,4}$/)
    .optional(),
});

export const MoveBodySchema = z.object({
  from: z.string().min(1),
  to: z.string().min(1),
});

export const CopyBodySchema = z.object({
  from: z.string().min(1),
  to: z.string().min(1),
});

export const UploadStartBodySchema = z.object({
  path: z.string().min(1),
  filename: z.string().min(1),
  size: z.number().int().positive(),
  checksum: z.string().regex(/^sha256:[a-f0-9]{64}$/),
  chunkSize: z.number().int().positive(),
  ownerUid: z.number().int().optional(),
  ownerGid: z.number().int().optional(),
  mode: z
    .string()
    .regex(/^[0-7]{3,4}$/)
    .optional(),
});

// ============================================================================
// Response Schemas
// ============================================================================

export const SearchResultSchema = z.object({
  basePath: z.string(),
  files: z.array(FileInfoSchema),
  total: z.number(),
  hasMore: z.boolean(),
});

export const SearchResponseSchema = z.object({
  results: z.array(SearchResultSchema),
  totalFiles: z.number(),
});

export const UploadStartResponseSchema = z.object({
  uploadId: z.string().regex(/^[a-f0-9]{16}$/),
  totalChunks: z.number(),
  chunkSize: z.number(),
  uploadedChunks: z.array(z.number()),
  completed: z.literal(false),
});

export const UploadChunkProgressSchema = z.object({
  chunkIndex: z.number(),
  uploadedChunks: z.array(z.number()),
  completed: z.literal(false),
});

export const UploadChunkCompleteSchema = z.object({
  completed: z.literal(true),
  file: FileInfoSchema.extend({ checksum: z.string() }),
});

export const UploadChunkResponseSchema = z.union([UploadChunkProgressSchema, UploadChunkCompleteSchema]);

// ============================================================================
// Header Schemas
// ============================================================================

export const UploadFileHeadersSchema = z.object({
  "x-file-path": z.string().min(1),
  "x-file-name": z.string().min(1),
  "x-owner-uid": z.string().regex(/^\d+$/).transform(Number).optional(),
  "x-owner-gid": z.string().regex(/^\d+$/).transform(Number).optional(),
  "x-file-mode": z
    .string()
    .regex(/^[0-7]{3,4}$/)
    .optional(),
});

export const UploadChunkHeadersSchema = z.object({
  "x-upload-id": z.string().regex(/^[a-f0-9]{16}$/),
  "x-chunk-index": z.string().regex(/^\d+$/).transform(Number),
  "x-chunk-checksum": z
    .string()
    .regex(/^sha256:[a-f0-9]{64}$/)
    .optional(),
});

// ============================================================================
// Types
// ============================================================================

export type FileInfo = z.infer<typeof FileInfoSchema>;
export type DirInfo = z.infer<typeof DirInfoSchema>;
export type SearchResult = z.infer<typeof SearchResultSchema>;
export type UploadStartBody = z.infer<typeof UploadStartBodySchema>;
export type UploadFileHeaders = z.infer<typeof UploadFileHeadersSchema>;
export type UploadChunkHeaders = z.infer<typeof UploadChunkHeadersSchema>;
