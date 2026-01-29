import { z } from "zod";

// ============================================================================
// Common
// ============================================================================

export const ErrorSchema = z
  .object({
    error: z.string().describe("Error message describing what went wrong"),
  })
  .describe("Error response returned when a request fails");

export const FileTypeSchema = z.enum(["file", "directory"]).describe("Type of filesystem entry");

export const FileInfoSchema = z
  .object({
    name: z.string().describe("Filename or directory name"),
    path: z.string().describe("Relative path from the base directory"),
    type: FileTypeSchema,
    size: z.number().describe("File size in bytes, or total directory size for directories"),
    mtime: z.iso.datetime().describe("Last modification time in ISO 8601 format"),
    isHidden: z.boolean().describe("True if the name starts with a dot"),
    mimeType: z.string().optional().describe("MIME type of the file (only for files)"),
  })
  .describe("Information about a file or directory");

export const DirInfoSchema = FileInfoSchema.extend({
  items: z.array(FileInfoSchema).describe("List of files and directories in this directory"),
  total: z.number().describe("Total number of items in the directory"),
}).describe("Directory information including its contents");

// ============================================================================
// Query Params
// ============================================================================

export const PathQuerySchema = z
  .object({
    path: z.string().min(1).describe("Absolute path to the file or directory"),
  })
  .describe("Query parameters for path-based operations");

export const ContentQuerySchema = z
  .object({
    path: z.string().min(1).describe("Absolute path to the file or directory to download"),
    inline: z
      .string()
      .optional()
      .transform((v) => v === "true")
      .describe("If 'true', display in browser instead of downloading (Content-Disposition: inline)"),
  })
  .describe("Query parameters for content download");

export const InfoQuerySchema = z
  .object({
    path: z.string().min(1).describe("Absolute path to the file or directory"),
    showHidden: z
      .string()
      .optional()
      .transform((v) => v === "true")
      .describe("If 'true', include hidden files (starting with dot) in directory listings"),
  })
  .describe("Query parameters for file/directory info");

export const SearchQuerySchema = z
  .object({
    paths: z.string().min(1).describe("Comma-separated list of base paths to search in"),
    pattern: z.string().min(1).max(500).describe("Glob pattern to match files (e.g., '*.txt', '**/*.pdf')"),
    showHidden: z
      .string()
      .optional()
      .transform((v) => v === "true")
      .describe("If 'true', include hidden files in search results"),
    limit: z
      .string()
      .optional()
      .transform((v) => (v ? parseInt(v, 10) : undefined))
      .describe("Maximum number of results to return"),
    files: z
      .string()
      .optional()
      .transform((v) => v !== "false")
      .describe("If 'false', exclude files from results (default: true)"),
    directories: z
      .string()
      .optional()
      .transform((v) => v === "true")
      .describe("If 'true', include directories in results (default: false)"),
  })
  .describe("Query parameters for glob-based file search");

/** Count recursive wildcards (**) in a glob pattern */
export const countRecursiveWildcards = (pattern: string): number => {
  return (pattern.match(/\*\*/g) || []).length;
};

// ============================================================================
// Request Bodies
// ============================================================================

export const MkdirBodySchema = z
  .object({
    path: z.string().min(1).describe("Absolute path of the directory to create"),
    ownerUid: z.number().int().optional().describe("Unix user ID to set as owner"),
    ownerGid: z.number().int().optional().describe("Unix group ID to set as owner"),
    mode: z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode (e.g., '755' or '0755')"),
  })
  .describe("Request body for creating a directory");

export const TransferModeSchema = z
  .enum(["move", "copy"])
  .describe("Transfer operation type: 'move' (rename) or 'copy' (duplicate)");

export const TransferBodySchema = z
  .object({
    from: z.string().min(1).describe("Source path of the file or directory"),
    to: z.string().min(1).describe("Destination path for the file or directory"),
    mode: TransferModeSchema,
    ensureUniqueName: z
      .boolean()
      .default(true)
      .describe("If true, append -01, -02, etc. to avoid overwriting existing files (default: true)"),
    ownerUid: z.number().int().optional().describe("Unix user ID for ownership (required for cross-base copy)"),
    ownerGid: z.number().int().optional().describe("Unix group ID for ownership (required for cross-base copy)"),
    fileMode: z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode for files (e.g., '644', required for cross-base copy)"),
    dirMode: z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode for directories (e.g., '755', defaults to fileMode if not set)"),
  })
  .describe("Request body for moving or copying files/directories");

export const UploadStartBodySchema = z
  .object({
    path: z.string().min(1).describe("Directory path where the file will be uploaded"),
    filename: z.string().min(1).describe("Name of the file to upload"),
    size: z.number().int().positive().describe("Total size of the file in bytes"),
    checksum: z
      .string()
      .regex(/^sha256:[a-f0-9]{64}$/)
      .describe("SHA-256 checksum of the entire file (format: 'sha256:<64 hex chars>')"),
    chunkSize: z.number().int().positive().describe("Size of each chunk in bytes"),
    ownerUid: z.number().int().optional().describe("Unix user ID to set as owner"),
    ownerGid: z.number().int().optional().describe("Unix group ID to set as owner"),
    mode: z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode for the uploaded file (e.g., '644')"),
    dirMode: z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode for auto-created parent directories (e.g., '755')"),
  })
  .describe("Request body to start or resume a chunked upload");

// ============================================================================
// Response Schemas
// ============================================================================

export const SearchResultSchema = z
  .object({
    basePath: z.string().describe("Base path that was searched"),
    files: z.array(FileInfoSchema).describe("List of matching files and directories"),
    total: z.number().describe("Number of matches found in this base path"),
    hasMore: z.boolean().describe("True if there are more results beyond the limit"),
  })
  .describe("Search results for a single base path");

export const SearchResponseSchema = z
  .object({
    results: z.array(SearchResultSchema).describe("Search results grouped by base path"),
    totalFiles: z.number().describe("Total number of matches across all base paths"),
  })
  .describe("Complete search response with results from all searched paths");

export const UploadStartResponseSchema = z
  .object({
    uploadId: z
      .string()
      .regex(/^[a-f0-9]{16}$/)
      .describe("Unique identifier for this upload session"),
    totalChunks: z.number().describe("Total number of chunks expected"),
    chunkSize: z.number().describe("Size of each chunk in bytes"),
    uploadedChunks: z.array(z.number()).describe("Indices of chunks already uploaded (for resume)"),
    completed: z.literal(false).describe("Always false for start response"),
  })
  .describe("Response when starting or resuming a chunked upload");

export const UploadChunkProgressSchema = z
  .object({
    chunkIndex: z.number().describe("Index of the chunk that was just uploaded"),
    uploadedChunks: z.array(z.number()).describe("All chunk indices uploaded so far"),
    completed: z.literal(false).describe("False while upload is still in progress"),
  })
  .describe("Response after uploading a chunk (upload not yet complete)");

export const UploadChunkCompleteSchema = z
  .object({
    completed: z.literal(true).describe("True when all chunks have been uploaded"),
    file: FileInfoSchema.extend({
      checksum: z.string().describe("SHA-256 checksum of the assembled file"),
    }).describe("Information about the completed file"),
  })
  .describe("Response after uploading the final chunk");

export const UploadChunkResponseSchema = z
  .union([UploadChunkProgressSchema, UploadChunkCompleteSchema])
  .describe("Response after uploading a chunk (either progress or completion)");

// ============================================================================
// Header Schemas
// ============================================================================

export const UploadFileHeadersSchema = z
  .object({
    "x-file-path": z.string().min(1).describe("Directory path where the file will be uploaded"),
    "x-file-name": z.string().min(1).describe("Name of the file to upload"),
    "x-owner-uid": z.string().regex(/^\d+$/).transform(Number).optional().describe("Unix user ID to set as owner"),
    "x-owner-gid": z.string().regex(/^\d+$/).transform(Number).optional().describe("Unix group ID to set as owner"),
    "x-file-mode": z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode for the file (e.g., '644')"),
    "x-dir-mode": z
      .string()
      .regex(/^[0-7]{3,4}$/)
      .optional()
      .describe("Unix permission mode for auto-created directories (e.g., '755')"),
  })
  .describe("Headers for simple file upload");

export const UploadChunkHeadersSchema = z
  .object({
    "x-upload-id": z
      .string()
      .regex(/^[a-f0-9]{16}$/)
      .describe("Upload session ID from the start response"),
    "x-chunk-index": z.string().regex(/^\d+$/).transform(Number).describe("Zero-based index of this chunk"),
    "x-chunk-checksum": z
      .string()
      .regex(/^sha256:[a-f0-9]{64}$/)
      .optional()
      .describe("SHA-256 checksum of this chunk for verification (format: 'sha256:<64 hex chars>')"),
  })
  .describe("Headers for uploading a chunk");

// ============================================================================
// Types
// ============================================================================

export type FileInfo = z.infer<typeof FileInfoSchema>;
export type DirInfo = z.infer<typeof DirInfoSchema>;
export type SearchResult = z.infer<typeof SearchResultSchema>;
export type UploadStartBody = z.infer<typeof UploadStartBodySchema>;
export type UploadFileHeaders = z.infer<typeof UploadFileHeadersSchema>;
export type UploadChunkHeaders = z.infer<typeof UploadChunkHeadersSchema>;
