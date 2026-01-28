/**
 * Integration tests for Filegate client against a running Docker container.
 *
 * Prerequisites:
 *   docker compose -f compose.test.yml up -d --build --wait
 *
 * Run tests:
 *   bun test tests/integration
 *
 * Cleanup:
 *   docker compose -f compose.test.yml down -v
 */

import { describe, test, expect, beforeAll, afterAll } from "bun:test";
import { Filegate } from "../../src/client";

const BASE_URL = process.env.FILEGATE_TEST_URL || "http://localhost:4111";
const TOKEN = process.env.FILEGATE_TEST_TOKEN || "test-integration-token";

const client = new Filegate({ url: BASE_URL, token: TOKEN });
const badTokenClient = new Filegate({ url: BASE_URL, token: "wrong-token" });

// Helper to generate random data
const randomBytes = (size: number): Uint8Array => {
  const arr = new Uint8Array(size);
  crypto.getRandomValues(arr);
  return arr;
};

// Helper to generate random string
const randomString = (length: number): string => {
  return Math.random()
    .toString(36)
    .substring(2, 2 + length);
};

// Helper to wait for server
const waitForServer = async (maxAttempts = 30, delay = 1000): Promise<boolean> => {
  for (let i = 0; i < maxAttempts; i++) {
    try {
      const res = await fetch(`${BASE_URL}/health`);
      if (res.ok) return true;
    } catch {
      // Server not ready yet
    }
    await new Promise((r) => setTimeout(r, delay));
  }
  return false;
};

describe("integration tests", () => {
  beforeAll(async () => {
    const ready = await waitForServer();
    if (!ready) {
      throw new Error(
        `Server not available at ${BASE_URL}. ` +
          "Please start the test containers: docker compose -f compose.test.yml up -d --build --wait",
      );
    }
  });

  describe("health check", () => {
    test("should return healthy", async () => {
      const res = await fetch(`${BASE_URL}/health`);
      expect(res.ok).toBe(true);
      const body = await res.text();
      expect(body).toBe("OK");
    });
  });

  describe("authentication", () => {
    test("should reject requests without token", async () => {
      const res = await fetch(`${BASE_URL}/files/info?path=/data`);
      expect(res.ok).toBe(false);
    });

    test("should reject requests with wrong token", async () => {
      const result = await badTokenClient.info({ path: "/data" });
      expect(result.ok).toBe(false);
    });

    test("should accept requests with valid token", async () => {
      const result = await client.info({ path: "/data", showHidden: true });
      expect(result.ok).toBe(true);
    });
  });

  describe("directory operations", () => {
    const testDir = `/data/test-dir-${randomString(8)}`;

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should create directory", async () => {
      const result = await client.mkdir({ path: testDir });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.type).toBe("directory");
        expect(result.data.name).toBe(testDir.split("/").pop()!);
      }
    });

    test("should list directory contents", async () => {
      const result = await client.info({ path: testDir });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.type).toBe("directory");
        expect("items" in result.data).toBe(true);
      }
    });

    test("should create nested directory with recursive mkdir", async () => {
      const parentDir = `${testDir}/nested`;
      await client.mkdir({ path: parentDir });

      const nestedDir = `${parentDir}/deep`;
      const result = await client.mkdir({ path: nestedDir });
      expect(result.ok).toBe(true);
    });

    test("should fail to create directory outside allowed path", async () => {
      const result = await client.mkdir({ path: "/etc/test" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
      }
    });
  });

  describe("file upload and download", () => {
    const testDir = `/data/test-files-${randomString(8)}`;
    const testFile = `${testDir}/test.txt`;
    const testContent = "Hello, Filegate!";

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should upload a text file", async () => {
      const data = new TextEncoder().encode(testContent);
      const result = await client.upload.single({
        path: testDir,
        filename: "test.txt",
        data,
      });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.name).toBe("test.txt");
        expect(result.data.size).toBe(data.length);
        expect(result.data.type).toBe("file");
      }
    });

    test("should download the uploaded file", async () => {
      const result = await client.download({ path: testFile });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const text = await result.data.text();
        expect(text).toBe(testContent);
      }
    });

    test("should get file info", async () => {
      const result = await client.info({ path: testFile });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.name).toBe("test.txt");
        expect(result.data.type).toBe("file");
        expect(result.data.size).toBe(new TextEncoder().encode(testContent).length);
      }
    });

    test("should download file as buffer via response", async () => {
      const result = await client.download({ path: testFile });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const buffer = await result.data.arrayBuffer();
        const text = new TextDecoder().decode(buffer);
        expect(text).toBe(testContent);
      }
    });

    test("should upload binary file", async () => {
      const binaryData = randomBytes(1024);
      const result = await client.upload.single({
        path: testDir,
        filename: "binary.bin",
        data: binaryData,
      });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.size).toBe(1024);
      }

      // Verify download
      const downloadResult = await client.download({ path: `${testDir}/binary.bin` });
      expect(downloadResult.ok).toBe(true);
      if (downloadResult.ok) {
        const downloaded = new Uint8Array(await downloadResult.data.arrayBuffer());
        expect(downloaded.length).toBe(binaryData.length);
        expect(Array.from(downloaded)).toEqual(Array.from(binaryData));
      }
    });

    test("should fail to download non-existent file", async () => {
      const result = await client.download({ path: `${testDir}/nonexistent.txt` });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(404);
      }
    });
  });

  describe("move and copy operations", () => {
    const testDir = `/data/test-move-copy-${randomString(8)}`;
    const sourceFile = `${testDir}/source.txt`;
    const content = "Move and copy test content";

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
      await client.upload.single({
        path: testDir,
        filename: "source.txt",
        data: new TextEncoder().encode(content),
      });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should copy file", async () => {
      const destFile = `${testDir}/copied.txt`;
      const result = await client.copy({ from: sourceFile, to: destFile });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.name).toBe("copied.txt");
      }

      // Verify both files exist
      const sourceInfo = await client.info({ path: sourceFile });
      const destInfo = await client.info({ path: destFile });
      expect(sourceInfo.ok).toBe(true);
      expect(destInfo.ok).toBe(true);

      // Verify content
      const destContent = await client.download({ path: destFile });
      expect(destContent.ok).toBe(true);
      if (destContent.ok) {
        expect(await destContent.data.text()).toBe(content);
      }
    });

    test("should move file", async () => {
      // First create a file to move
      await client.upload.single({
        path: testDir,
        filename: "to-move.txt",
        data: new TextEncoder().encode("to be moved"),
      });

      const movedFile = `${testDir}/moved.txt`;
      const result = await client.move({ from: `${testDir}/to-move.txt`, to: movedFile });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.name).toBe("moved.txt");
      }

      // Verify source no longer exists
      const sourceInfo = await client.info({ path: `${testDir}/to-move.txt` });
      expect(sourceInfo.ok).toBe(false);

      // Verify destination exists
      const destInfo = await client.info({ path: movedFile });
      expect(destInfo.ok).toBe(true);
    });

    test("should fail to move file outside allowed path", async () => {
      const result = await client.move({ from: sourceFile, to: "/etc/passwd" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
      }
    });
  });

  describe("delete operations", () => {
    const testDir = `/data/test-delete-${randomString(8)}`;

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
    });

    test("should delete file", async () => {
      const testFile = `${testDir}/to-delete.txt`;
      await client.upload.single({
        path: testDir,
        filename: "to-delete.txt",
        data: new TextEncoder().encode("delete me"),
      });

      const result = await client.delete({ path: testFile });
      expect(result.ok).toBe(true);

      // Verify file is gone
      const info = await client.info({ path: testFile });
      expect(info.ok).toBe(false);
    });

    test("should delete directory recursively", async () => {
      const subDir = `${testDir}/subdir`;
      await client.mkdir({ path: subDir });
      await client.upload.single({
        path: subDir,
        filename: "nested.txt",
        data: new TextEncoder().encode("nested"),
      });

      const result = await client.delete({ path: subDir });
      expect(result.ok).toBe(true);

      // Verify directory is gone
      const info = await client.info({ path: subDir });
      expect(info.ok).toBe(false);
    });

    test("should fail to delete non-existent file", async () => {
      const result = await client.delete({ path: `${testDir}/nonexistent.txt` });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(404);
      }
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });
  });

  describe("glob (search) operations", () => {
    const testDir = `/data/test-search-${randomString(8)}`;

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
      await client.mkdir({ path: `${testDir}/subdir` });

      // Create various files
      await client.upload.single({
        path: testDir,
        filename: "document1.txt",
        data: new TextEncoder().encode("doc1"),
      });
      await client.upload.single({
        path: testDir,
        filename: "document2.txt",
        data: new TextEncoder().encode("doc2"),
      });
      await client.upload.single({
        path: testDir,
        filename: "data.json",
        data: new TextEncoder().encode("data"),
      });
      await client.upload.single({
        path: `${testDir}/subdir`,
        filename: "nested.txt",
        data: new TextEncoder().encode("nested"),
      });
      await client.upload.single({
        path: testDir,
        filename: ".hidden",
        data: new TextEncoder().encode("hidden"),
      });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should search for files by pattern", async () => {
      const result = await client.glob({ paths: [testDir], pattern: "*.txt" });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.totalFiles).toBeGreaterThanOrEqual(2);
        const names = result.data.results.flatMap((r) => r.files.map((f) => f.name));
        expect(names).toContain("document1.txt");
        expect(names).toContain("document2.txt");
      }
    });

    test("should search with glob pattern", async () => {
      const result = await client.glob({ paths: [testDir], pattern: "*.json" });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.totalFiles).toBe(1);
        expect(result.data.results[0]?.files[0]?.name).toBe("data.json");
      }
    });

    test("should search with recursive glob", async () => {
      const result = await client.glob({ paths: [testDir], pattern: "**/*.txt" });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.totalFiles).toBeGreaterThanOrEqual(3);
      }
    });

    test("should not include hidden files by default", async () => {
      const result = await client.glob({ paths: [testDir], pattern: "*" });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const names = result.data.results.flatMap((r) => r.files.map((f) => f.name));
        expect(names).not.toContain(".hidden");
      }
    });

    test("should include hidden files when requested", async () => {
      const result = await client.glob({ paths: [testDir], pattern: ".*", showHidden: true });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const names = result.data.results.flatMap((r) => r.files.map((f) => f.name));
        expect(names).toContain(".hidden");
      }
    });

    test("should respect limit option", async () => {
      const result = await client.glob({ paths: [testDir], pattern: "**/*", limit: 2 });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const totalReturned = result.data.results.reduce((sum, r) => sum + r.files.length, 0);
        expect(totalReturned).toBeLessThanOrEqual(2);
      }
    });
  });

  describe("chunked upload", () => {
    const testDir = `/data/test-chunked-${randomString(8)}`;

    // Helper to compute SHA-256 checksum
    const computeChecksum = async (data: Uint8Array): Promise<string> => {
      const hashBuffer = await crypto.subtle.digest("SHA-256", data.buffer as ArrayBuffer);
      const hashArray = Array.from(new Uint8Array(hashBuffer));
      return `sha256:${hashArray.map((b) => b.toString(16).padStart(2, "0")).join("")}`;
    };

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should upload small file in chunks", async () => {
      const data = randomBytes(50 * 1024); // 50KB
      const chunkSize = 10 * 1024; // 10KB chunks
      const checksum = await computeChecksum(data);
      const totalChunks = Math.ceil(data.length / chunkSize);

      // Start upload
      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename: "small-chunked.bin",
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;

      // Upload all chunks
      for (let i = 0; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const sendResult = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(sendResult.ok).toBe(true);
        if (sendResult.ok && i === totalChunks - 1) {
          expect(sendResult.data.completed).toBe(true);
        }
      }

      // Verify content
      const downloadResult = await client.download({ path: `${testDir}/small-chunked.bin` });
      expect(downloadResult.ok).toBe(true);
      if (downloadResult.ok) {
        expect(Array.from(new Uint8Array(await downloadResult.data.arrayBuffer()))).toEqual(Array.from(data));
      }
    });

    test("should upload medium file in chunks (1MB)", async () => {
      const data = randomBytes(1024 * 1024); // 1MB
      const chunkSize = 256 * 1024; // 256KB chunks
      const checksum = await computeChecksum(data);
      const totalChunks = Math.ceil(data.length / chunkSize);

      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename: "medium-chunked.bin",
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;

      for (let i = 0; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const sendResult = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(sendResult.ok).toBe(true);
      }

      // Verify file exists
      const info = await client.info({ path: `${testDir}/medium-chunked.bin` });
      expect(info.ok).toBe(true);
      if (info.ok) {
        expect(info.data.size).toBe(data.length);
      }
    });

    test("should upload large file in chunks (10MB)", async () => {
      const data = randomBytes(10 * 1024 * 1024); // 10MB
      const chunkSize = 1024 * 1024; // 1MB chunks
      const checksum = await computeChecksum(data);
      const totalChunks = Math.ceil(data.length / chunkSize);

      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename: "large-chunked.bin",
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;

      for (let i = 0; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const sendResult = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(sendResult.ok).toBe(true);
      }

      // Verify download size matches
      const info = await client.info({ path: `${testDir}/large-chunked.bin` });
      expect(info.ok).toBe(true);
      if (info.ok) {
        expect(info.data.size).toBe(data.length);
      }
    }, 60000);

    test("should verify checksum on chunked upload", async () => {
      const content = "Checksum verification test data";
      const data = new TextEncoder().encode(content);
      const chunkSize = 10;
      const checksum = await computeChecksum(data);
      const totalChunks = Math.ceil(data.length / chunkSize);

      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename: "checksum-test.txt",
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;

      for (let i = 0; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });
      }

      // Verify content
      const downloadResult = await client.download({ path: `${testDir}/checksum-test.txt` });
      expect(downloadResult.ok).toBe(true);
      if (downloadResult.ok) {
        expect(await downloadResult.data.text()).toBe(content);
      }
    });
  });

  describe("error handling", () => {
    test("should return 404 for non-existent file info", async () => {
      const result = await client.info({ path: "/data/nonexistent-file-12345.txt" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(404);
      }
    });

    test("should return 403 for path outside allowed base", async () => {
      const result = await client.info({ path: "/etc/passwd" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(403);
      }
    });

    test("should return error for invalid request", async () => {
      const res = await fetch(`${BASE_URL}/files/info?path=`, {
        headers: { Authorization: `Bearer ${TOKEN}` },
      });
      expect(res.ok).toBe(false);
    });
  });

  describe("directory download as TAR", () => {
    const testDir = `/data/test-tar-${randomString(8)}`;

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
      await client.mkdir({ path: `${testDir}/subdir` });
      await client.upload.single({
        path: testDir,
        filename: "file1.txt",
        data: new TextEncoder().encode("file1 content"),
      });
      await client.upload.single({
        path: testDir,
        filename: "file2.txt",
        data: new TextEncoder().encode("file2 content"),
      });
      await client.upload.single({
        path: `${testDir}/subdir`,
        filename: "nested.txt",
        data: new TextEncoder().encode("nested content"),
      });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should download directory as TAR", async () => {
      const result = await client.download({ path: testDir });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const contentType = result.data.headers.get("content-type");
        expect(contentType).toBe("application/x-tar");

        const contentDisposition = result.data.headers.get("content-disposition");
        expect(contentDisposition).toContain("attachment");
        expect(contentDisposition).toContain(".tar");

        // Verify we got some data
        const buffer = await result.data.arrayBuffer();
        expect(buffer.byteLength).toBeGreaterThan(0);

        // TAR files start with "ustar" or null bytes (0x0000...)
        // ustar magic is at offset 257 (0x101)
        const view = new Uint8Array(buffer);
        // Check for ustar magic bytes at offset 257
        const ustar = "ustar";
        let isTar = true;
        for (let i = 0; i < ustar.length; i++) {
          if (view[257 + i] !== ustar.charCodeAt(i)) {
            isTar = false;
            break;
          }
        }
        expect(isTar).toBe(true);
      }
    });
  });

  describe("concurrent operations", () => {
    const testDir = `/data/test-concurrent-${randomString(8)}`;

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should handle concurrent uploads", async () => {
      const uploads = Array.from({ length: 10 }, (_, i) =>
        client.upload.single({
          path: testDir,
          filename: `concurrent-${i}.txt`,
          data: new TextEncoder().encode(`content ${i}`),
        }),
      );

      const results = await Promise.all(uploads);
      const successes = results.filter((r) => r.ok);
      expect(successes.length).toBe(10);
    });

    test("should handle concurrent downloads", async () => {
      const downloads = Array.from({ length: 10 }, (_, i) =>
        client.download({ path: `${testDir}/concurrent-${i}.txt` }),
      );

      const results = await Promise.all(downloads);
      const successes = results.filter((r) => r.ok);
      expect(successes.length).toBe(10);

      // Verify content
      for (let i = 0; i < results.length; i++) {
        const r = results[i];
        if (r && r.ok) {
          expect(await r.data.text()).toBe(`content ${i}`);
        }
      }
    });
  });

  describe("large file handling", () => {
    const testDir = `/data/test-large-${randomString(8)}`;

    // Helper to compute SHA-256 checksum
    const computeChecksum = async (data: Uint8Array): Promise<string> => {
      const hashBuffer = await crypto.subtle.digest("SHA-256", data.buffer as ArrayBuffer);
      const hashArray = Array.from(new Uint8Array(hashBuffer));
      return `sha256:${hashArray.map((b) => b.toString(16).padStart(2, "0")).join("")}`;
    };

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should upload and download 50MB file via chunked upload", async () => {
      const size = 50 * 1024 * 1024; // 50MB
      const data = randomBytes(size);
      const chunkSize = 5 * 1024 * 1024; // 5MB chunks
      const checksum = await computeChecksum(data);
      const totalChunks = Math.ceil(size / chunkSize);

      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename: "large-50mb.bin",
        size,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;

      for (let i = 0; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, size);
        const chunk = data.slice(start, end);

        await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });
      }

      // Verify file exists with correct size
      const info = await client.info({ path: `${testDir}/large-50mb.bin` });
      expect(info.ok).toBe(true);
      if (info.ok) {
        expect(info.data.size).toBe(size);
      }
    }, 120000);
  });

  describe("chunked upload - resume and auto-complete", () => {
    const testDir = `/data/test-resume-${randomString(8)}`;

    // Helper to compute SHA-256 checksum
    const computeChecksum = async (data: Uint8Array): Promise<string> => {
      const hashBuffer = await crypto.subtle.digest("SHA-256", data.buffer as ArrayBuffer);
      const hashArray = Array.from(new Uint8Array(hashBuffer));
      return `sha256:${hashArray.map((b) => b.toString(16).padStart(2, "0")).join("")}`;
    };

    beforeAll(async () => {
      await client.mkdir({ path: testDir });
    });

    afterAll(async () => {
      await client.delete({ path: testDir });
    });

    test("should resume upload after interruption", async () => {
      const data = randomBytes(100 * 1024);
      const chunkSize = 20 * 1024;
      const checksum = await computeChecksum(data);
      const filename = "resume-test.bin";
      const totalChunks = Math.ceil(data.length / chunkSize);

      // Start upload
      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename,
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;
      expect(startResult.data.totalChunks).toBe(totalChunks);
      expect(startResult.data.uploadedChunks).toEqual([]);

      // Upload only first 2 chunks
      for (let i = 0; i < 2; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const result = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(result.ok).toBe(true);
        if (result.ok) {
          expect(result.data.completed).toBe(false);
        }
      }

      // Resume - start again with same parameters
      const resumeResult = await client.upload.chunked.start({
        path: testDir,
        filename,
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(resumeResult.ok).toBe(true);
      if (!resumeResult.ok) return;

      // Should get the same upload ID and show uploaded chunks
      expect(resumeResult.data.uploadId).toBe(uploadId);
      expect(resumeResult.data.uploadedChunks).toContain(0);
      expect(resumeResult.data.uploadedChunks).toContain(1);

      // Upload remaining chunks
      for (let i = 2; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const result = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(result.ok).toBe(true);
        if (result.ok && i === totalChunks - 1) {
          expect(result.data.completed).toBe(true);
        }
      }

      // Verify file content
      const downloadResult = await client.download({ path: `${testDir}/${filename}` });
      expect(downloadResult.ok).toBe(true);
      if (downloadResult.ok) {
        expect(Array.from(new Uint8Array(await downloadResult.data.arrayBuffer()))).toEqual(Array.from(data));
      }
    });

    test("should auto-complete when all chunks uploaded", async () => {
      const content = "This is auto-complete test content that will be split into chunks";
      const data = new TextEncoder().encode(content);
      const chunkSize = 10;
      const checksum = await computeChecksum(data);
      const filename = "autocomplete-test.txt";
      const totalChunks = Math.ceil(data.length / chunkSize);

      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename,
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;

      // Upload all chunks
      for (let i = 0; i < totalChunks; i++) {
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const result = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(result.ok).toBe(true);
        if (result.ok && i === totalChunks - 1) {
          expect(result.data.completed).toBe(true);
          if (result.data.completed) {
            expect(result.data.file).toBeDefined();
            expect(result.data.file?.name).toBe(filename);
          }
        }
      }

      // Verify content
      const downloadResult = await client.download({ path: `${testDir}/${filename}` });
      expect(downloadResult.ok).toBe(true);
      if (downloadResult.ok) {
        expect(await downloadResult.data.text()).toBe(content);
      }
    });

    test("should handle out-of-order chunk uploads", async () => {
      const data = randomBytes(50 * 1024);
      const chunkSize = 10 * 1024;
      const checksum = await computeChecksum(data);
      const filename = "out-of-order.bin";
      const totalChunks = Math.ceil(data.length / chunkSize);

      const startResult = await client.upload.chunked.start({
        path: testDir,
        filename,
        size: data.length,
        checksum,
        chunkSize,
      });

      expect(startResult.ok).toBe(true);
      if (!startResult.ok) return;

      const { uploadId } = startResult.data;
      const chunkOrder = [3, 0, 4, 1, 2]; // Random order

      for (let idx = 0; idx < chunkOrder.length; idx++) {
        const i = chunkOrder[idx]!;
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, data.length);
        const chunk = data.slice(start, end);

        const result = await client.upload.chunked.send({
          uploadId,
          index: i,
          data: chunk,
        });

        expect(result.ok).toBe(true);
        if (result.ok && idx === chunkOrder.length - 1) {
          expect(result.data.completed).toBe(true);
        }
      }

      // Verify content
      const downloadResult = await client.download({ path: `${testDir}/${filename}` });
      expect(downloadResult.ok).toBe(true);
      if (downloadResult.ok) {
        expect(Array.from(new Uint8Array(await downloadResult.data.arrayBuffer()))).toEqual(Array.from(data));
      }
    });
  });
});
