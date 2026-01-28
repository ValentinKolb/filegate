import { describe, test, expect, beforeAll, afterAll, mock } from "bun:test";
import { mkdir, writeFile, rm, stat } from "node:fs/promises";
import { join } from "node:path";
import { parseOwnershipHeaders, parseOwnershipBody, applyOwnership, type Ownership } from "../../src/lib/ownership";

const TEST_BASE = "/tmp/filegate-test-ownership";
const TEST_FILE = join(TEST_BASE, "test-file.txt");

describe("ownership", () => {
  beforeAll(async () => {
    await mkdir(TEST_BASE, { recursive: true });
    await writeFile(TEST_FILE, "test content");
  });

  afterAll(async () => {
    await rm(TEST_BASE, { recursive: true, force: true });
  });

  describe("parseOwnershipHeaders", () => {
    test("should parse valid ownership headers", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-Owner-GID": "1000",
        "X-File-Mode": "644",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).not.toBeNull();
      expect(result?.uid).toBe(1000);
      expect(result?.gid).toBe(1000);
      expect(result?.mode).toBe(0o644);
    });

    test("should parse mode with leading zero", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-Owner-GID": "1000",
        "X-File-Mode": "0755",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).not.toBeNull();
      expect(result?.mode).toBe(0o755);
    });

    test("should return null if UID is missing", () => {
      const headers = new Headers({
        "X-Owner-GID": "1000",
        "X-File-Mode": "644",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).toBeNull();
    });

    test("should return null if GID is missing", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-File-Mode": "644",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).toBeNull();
    });

    test("should return null if mode is missing", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-Owner-GID": "1000",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).toBeNull();
    });

    test("should return null for invalid mode", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-Owner-GID": "1000",
        "X-File-Mode": "invalid",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).toBeNull();
    });
  });

  describe("parseOwnershipBody", () => {
    test("should parse valid ownership body", () => {
      const body = {
        ownerUid: 1000,
        ownerGid: 1000,
        mode: "644",
      };

      const result = parseOwnershipBody(body);
      expect(result).not.toBeNull();
      expect(result?.uid).toBe(1000);
      expect(result?.gid).toBe(1000);
      expect(result?.mode).toBe(0o644);
    });

    test("should parse mode with leading zero", () => {
      const body = {
        ownerUid: 0,
        ownerGid: 0,
        mode: "0755",
      };

      const result = parseOwnershipBody(body);
      expect(result).not.toBeNull();
      expect(result?.uid).toBe(0);
      expect(result?.gid).toBe(0);
      expect(result?.mode).toBe(0o755);
    });

    test("should return null if ownerUid is missing", () => {
      const body = {
        ownerGid: 1000,
        mode: "644",
      };

      const result = parseOwnershipBody(body);
      expect(result).toBeNull();
    });

    test("should return null if ownerGid is missing", () => {
      const body = {
        ownerUid: 1000,
        mode: "644",
      };

      const result = parseOwnershipBody(body);
      expect(result).toBeNull();
    });

    test("should return null if mode is missing", () => {
      const body = {
        ownerUid: 1000,
        ownerGid: 1000,
      };

      const result = parseOwnershipBody(body);
      expect(result).toBeNull();
    });

    test("should return null for invalid mode string", () => {
      const body = {
        ownerUid: 1000,
        ownerGid: 1000,
        mode: "invalid",
      };

      const result = parseOwnershipBody(body);
      expect(result).toBeNull();
    });

    test("should handle uid/gid of 0 (root)", () => {
      const body = {
        ownerUid: 0,
        ownerGid: 0,
        mode: "600",
      };

      const result = parseOwnershipBody(body);
      expect(result).not.toBeNull();
      expect(result?.uid).toBe(0);
      expect(result?.gid).toBe(0);
    });
  });

  describe("applyOwnership", () => {
    test("should return null when ownership is null", async () => {
      const result = await applyOwnership(TEST_FILE, null);
      expect(result).toBeNull();
    });

    test("should apply ownership to file (may fail without root)", async () => {
      const ownership: Ownership = {
        uid: process.getuid?.() ?? 1000,
        gid: process.getgid?.() ?? 1000,
        mode: 0o644,
      };

      const result = await applyOwnership(TEST_FILE, ownership);

      // This might return an error if not running as root
      // But if it succeeds, it should return null
      if (result === null) {
        const stats = await stat(TEST_FILE);
        // Check that mode was applied (mask with 0o777 to get permission bits)
        expect(stats.mode & 0o777).toBe(0o644);
      } else {
        // If not root, we expect a permission error
        expect(result).toContain("permission");
      }
    });

    test("should return error message for non-existent file", async () => {
      const ownership: Ownership = {
        uid: 1000,
        gid: 1000,
        mode: 0o644,
      };

      const result = await applyOwnership("/nonexistent/file.txt", ownership);
      expect(result).not.toBeNull();
    });
  });
});
