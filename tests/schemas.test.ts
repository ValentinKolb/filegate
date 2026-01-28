import { describe, test, expect } from "bun:test";
import {
  ErrorSchema,
  FileTypeSchema,
  FileInfoSchema,
  DirInfoSchema,
  PathQuerySchema,
  InfoQuerySchema,
  SearchQuerySchema,
  MkdirBodySchema,
  MoveBodySchema,
  CopyBodySchema,
  UploadStartBodySchema,
  SearchResultSchema,
  SearchResponseSchema,
  UploadStartResponseSchema,
  UploadChunkProgressSchema,
  UploadChunkCompleteSchema,
  UploadChunkResponseSchema,
  countRecursiveWildcards,
} from "../src/schemas";

describe("schemas", () => {
  describe("ErrorSchema", () => {
    test("should validate valid error object", () => {
      const result = ErrorSchema.safeParse({ error: "something went wrong" });
      expect(result.success).toBe(true);
    });

    test("should reject missing error field", () => {
      const result = ErrorSchema.safeParse({});
      expect(result.success).toBe(false);
    });
  });

  describe("FileTypeSchema", () => {
    test("should accept 'file'", () => {
      const result = FileTypeSchema.safeParse("file");
      expect(result.success).toBe(true);
    });

    test("should accept 'directory'", () => {
      const result = FileTypeSchema.safeParse("directory");
      expect(result.success).toBe(true);
    });

    test("should reject invalid type", () => {
      const result = FileTypeSchema.safeParse("symlink");
      expect(result.success).toBe(false);
    });
  });

  describe("FileInfoSchema", () => {
    const validFileInfo = {
      name: "test.txt",
      path: "/data/test.txt",
      type: "file",
      size: 1024,
      mtime: "2024-01-15T10:30:00.000Z",
      isHidden: false,
    };

    test("should validate valid file info", () => {
      const result = FileInfoSchema.safeParse(validFileInfo);
      expect(result.success).toBe(true);
    });

    test("should accept optional mimeType", () => {
      const result = FileInfoSchema.safeParse({
        ...validFileInfo,
        mimeType: "text/plain",
      });
      expect(result.success).toBe(true);
    });

    test("should reject missing name", () => {
      const { name, ...rest } = validFileInfo;
      const result = FileInfoSchema.safeParse(rest);
      expect(result.success).toBe(false);
    });

    test("should reject invalid datetime format", () => {
      const result = FileInfoSchema.safeParse({
        ...validFileInfo,
        mtime: "not-a-date",
      });
      expect(result.success).toBe(false);
    });

    test("should reject invalid type", () => {
      const result = FileInfoSchema.safeParse({
        ...validFileInfo,
        type: "link",
      });
      expect(result.success).toBe(false);
    });
  });

  describe("DirInfoSchema", () => {
    const validDirInfo = {
      name: "subdir",
      path: "/data/subdir",
      type: "directory",
      size: 4096,
      mtime: "2024-01-15T10:30:00.000Z",
      isHidden: false,
      items: [
        {
          name: "file.txt",
          path: "/data/subdir/file.txt",
          type: "file",
          size: 100,
          mtime: "2024-01-15T10:30:00.000Z",
          isHidden: false,
        },
      ],
      total: 1,
    };

    test("should validate valid directory info", () => {
      const result = DirInfoSchema.safeParse(validDirInfo);
      expect(result.success).toBe(true);
    });

    test("should accept empty items array", () => {
      const result = DirInfoSchema.safeParse({
        ...validDirInfo,
        items: [],
        total: 0,
      });
      expect(result.success).toBe(true);
    });

    test("should reject missing items array", () => {
      const { items, ...rest } = validDirInfo;
      const result = DirInfoSchema.safeParse(rest);
      expect(result.success).toBe(false);
    });
  });

  describe("PathQuerySchema", () => {
    test("should validate valid path", () => {
      const result = PathQuerySchema.safeParse({ path: "/data/file.txt" });
      expect(result.success).toBe(true);
    });

    test("should reject empty path", () => {
      const result = PathQuerySchema.safeParse({ path: "" });
      expect(result.success).toBe(false);
    });

    test("should reject missing path", () => {
      const result = PathQuerySchema.safeParse({});
      expect(result.success).toBe(false);
    });
  });

  describe("InfoQuerySchema", () => {
    test("should validate with path only", () => {
      const result = InfoQuerySchema.safeParse({ path: "/data" });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.showHidden).toBe(false);
      }
    });

    test("should transform showHidden 'true' to boolean", () => {
      const result = InfoQuerySchema.safeParse({
        path: "/data",
        showHidden: "true",
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.showHidden).toBe(true);
      }
    });

    test("should transform other values to false", () => {
      const result = InfoQuerySchema.safeParse({
        path: "/data",
        showHidden: "false",
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.showHidden).toBe(false);
      }
    });
  });

  describe("SearchQuerySchema", () => {
    test("should validate valid search query", () => {
      const result = SearchQuerySchema.safeParse({
        paths: "/data,/backup",
        pattern: "*.txt",
      });
      expect(result.success).toBe(true);
    });

    test("should transform limit string to number", () => {
      const result = SearchQuerySchema.safeParse({
        paths: "/data",
        pattern: "*.txt",
        limit: "50",
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.limit).toBe(50);
      }
    });

    test("should handle missing limit", () => {
      const result = SearchQuerySchema.safeParse({
        paths: "/data",
        pattern: "*.txt",
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.limit).toBeUndefined();
      }
    });

    test("should reject empty pattern", () => {
      const result = SearchQuerySchema.safeParse({
        paths: "/data",
        pattern: "",
      });
      expect(result.success).toBe(false);
    });

    test("should reject pattern exceeding max length (500)", () => {
      const result = SearchQuerySchema.safeParse({
        paths: "/data",
        pattern: "a".repeat(501),
      });
      expect(result.success).toBe(false);
    });

    test("should accept pattern at max length (500)", () => {
      const result = SearchQuerySchema.safeParse({
        paths: "/data",
        pattern: "a".repeat(500),
      });
      expect(result.success).toBe(true);
    });
  });

  describe("countRecursiveWildcards", () => {
    test("should count zero wildcards in simple pattern", () => {
      expect(countRecursiveWildcards("*.txt")).toBe(0);
    });

    test("should count single recursive wildcard", () => {
      expect(countRecursiveWildcards("**/*.txt")).toBe(1);
    });

    test("should count multiple recursive wildcards", () => {
      expect(countRecursiveWildcards("**/**/*.txt")).toBe(2);
    });

    test("should count many recursive wildcards", () => {
      expect(countRecursiveWildcards("**/**/**/**/**")).toBe(5);
    });

    test("should not count single asterisks", () => {
      expect(countRecursiveWildcards("*/*/*.txt")).toBe(0);
    });

    test("should handle mixed patterns", () => {
      expect(countRecursiveWildcards("src/**/*.ts")).toBe(1);
      expect(countRecursiveWildcards("**/*.{ts,tsx}")).toBe(1);
      expect(countRecursiveWildcards("**/node_modules/**/*.js")).toBe(2);
    });

    test("should handle empty pattern", () => {
      expect(countRecursiveWildcards("")).toBe(0);
    });
  });

  describe("MkdirBodySchema", () => {
    test("should validate minimal mkdir body", () => {
      const result = MkdirBodySchema.safeParse({ path: "/data/newdir" });
      expect(result.success).toBe(true);
    });

    test("should validate with ownership fields", () => {
      const result = MkdirBodySchema.safeParse({
        path: "/data/newdir",
        ownerUid: 1000,
        ownerGid: 1000,
        mode: "755",
      });
      expect(result.success).toBe(true);
    });

    test("should accept 4-digit mode", () => {
      const result = MkdirBodySchema.safeParse({
        path: "/data/newdir",
        mode: "0755",
      });
      expect(result.success).toBe(true);
    });

    test("should reject invalid mode format", () => {
      const result = MkdirBodySchema.safeParse({
        path: "/data/newdir",
        mode: "999",
      });
      expect(result.success).toBe(false);
    });

    test("should reject mode with invalid characters", () => {
      const result = MkdirBodySchema.safeParse({
        path: "/data/newdir",
        mode: "abc",
      });
      expect(result.success).toBe(false);
    });
  });

  describe("MoveBodySchema", () => {
    test("should validate valid move body", () => {
      const result = MoveBodySchema.safeParse({
        from: "/data/old.txt",
        to: "/data/new.txt",
      });
      expect(result.success).toBe(true);
    });

    test("should reject empty from path", () => {
      const result = MoveBodySchema.safeParse({
        from: "",
        to: "/data/new.txt",
      });
      expect(result.success).toBe(false);
    });
  });

  describe("CopyBodySchema", () => {
    test("should validate valid copy body", () => {
      const result = CopyBodySchema.safeParse({
        from: "/data/source.txt",
        to: "/data/dest.txt",
      });
      expect(result.success).toBe(true);
    });
  });

  describe("UploadStartBodySchema", () => {
    const validUploadStart = {
      path: "/data",
      filename: "large-file.zip",
      size: 104857600, // 100MB
      checksum: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      chunkSize: 5242880, // 5MB
    };

    test("should validate valid upload start body", () => {
      const result = UploadStartBodySchema.safeParse(validUploadStart);
      expect(result.success).toBe(true);
    });

    test("should accept optional ownership fields", () => {
      const result = UploadStartBodySchema.safeParse({
        ...validUploadStart,
        ownerUid: 1000,
        ownerGid: 1000,
        mode: "644",
      });
      expect(result.success).toBe(true);
    });

    test("should reject invalid checksum format", () => {
      const result = UploadStartBodySchema.safeParse({
        ...validUploadStart,
        checksum: "md5:abc123",
      });
      expect(result.success).toBe(false);
    });

    test("should reject checksum without sha256 prefix", () => {
      const result = UploadStartBodySchema.safeParse({
        ...validUploadStart,
        checksum: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      });
      expect(result.success).toBe(false);
    });

    test("should reject non-positive size", () => {
      const result = UploadStartBodySchema.safeParse({
        ...validUploadStart,
        size: 0,
      });
      expect(result.success).toBe(false);
    });

    test("should reject negative size", () => {
      const result = UploadStartBodySchema.safeParse({
        ...validUploadStart,
        size: -100,
      });
      expect(result.success).toBe(false);
    });
  });

  describe("SearchResultSchema", () => {
    test("should validate valid search result", () => {
      const result = SearchResultSchema.safeParse({
        basePath: "/data",
        files: [
          {
            name: "test.txt",
            path: "/data/test.txt",
            type: "file",
            size: 100,
            mtime: "2024-01-15T10:30:00.000Z",
            isHidden: false,
          },
        ],
        total: 1,
        hasMore: false,
      });
      expect(result.success).toBe(true);
    });
  });

  describe("SearchResponseSchema", () => {
    test("should validate valid search response", () => {
      const result = SearchResponseSchema.safeParse({
        results: [
          {
            basePath: "/data",
            files: [],
            total: 0,
            hasMore: false,
          },
        ],
        totalFiles: 0,
      });
      expect(result.success).toBe(true);
    });
  });

  describe("UploadStartResponseSchema", () => {
    test("should validate valid upload start response", () => {
      const result = UploadStartResponseSchema.safeParse({
        uploadId: "a1b2c3d4e5f67890", // 16-char hex string
        totalChunks: 20,
        chunkSize: 5242880,
        uploadedChunks: [],
        completed: false,
      });
      expect(result.success).toBe(true);
    });

    test("should validate with already uploaded chunks (resume)", () => {
      const result = UploadStartResponseSchema.safeParse({
        uploadId: "a1b2c3d4e5f67890", // 16-char hex string
        totalChunks: 20,
        chunkSize: 5242880,
        uploadedChunks: [0, 1, 2, 3],
        completed: false,
      });
      expect(result.success).toBe(true);
    });

    test("should reject invalid uploadId format", () => {
      const result = UploadStartResponseSchema.safeParse({
        uploadId: "not-valid-hex!!",
        totalChunks: 20,
        chunkSize: 5242880,
        uploadedChunks: [],
        completed: false,
      });
      expect(result.success).toBe(false);
    });
  });

  describe("UploadChunkProgressSchema", () => {
    test("should validate valid chunk progress", () => {
      const result = UploadChunkProgressSchema.safeParse({
        chunkIndex: 5,
        uploadedChunks: [0, 1, 2, 3, 4, 5],
        completed: false,
      });
      expect(result.success).toBe(true);
    });
  });

  describe("UploadChunkCompleteSchema", () => {
    test("should validate valid chunk complete response", () => {
      const result = UploadChunkCompleteSchema.safeParse({
        completed: true,
        file: {
          name: "large-file.zip",
          path: "/data/large-file.zip",
          type: "file",
          size: 104857600,
          mtime: "2024-01-15T10:30:00.000Z",
          isHidden: false,
          checksum: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        },
      });
      expect(result.success).toBe(true);
    });
  });

  describe("UploadChunkResponseSchema", () => {
    test("should accept progress response", () => {
      const result = UploadChunkResponseSchema.safeParse({
        chunkIndex: 5,
        uploadedChunks: [0, 1, 2, 3, 4, 5],
        completed: false,
      });
      expect(result.success).toBe(true);
    });

    test("should accept complete response", () => {
      const result = UploadChunkResponseSchema.safeParse({
        completed: true,
        file: {
          name: "file.zip",
          path: "/data/file.zip",
          type: "file",
          size: 1000,
          mtime: "2024-01-15T10:30:00.000Z",
          isHidden: false,
          checksum: "sha256:abc123",
        },
      });
      expect(result.success).toBe(true);
    });
  });
});
