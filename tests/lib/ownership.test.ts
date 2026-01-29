import { describe, test, expect, beforeAll, afterAll, mock } from "bun:test";
import { mkdir, writeFile, rm, stat } from "node:fs/promises";
import { join } from "node:path";
import {
  parseOwnershipHeaders,
  parseOwnershipBody,
  applyOwnership,
  fileModeToDirectoryMode,
  getEffectiveDirMode,
  type Ownership,
} from "../../src/lib/ownership";

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

  describe("fileModeToDirectoryMode", () => {
    test("should add execute bit where read bit is set (644 → 755)", () => {
      expect(fileModeToDirectoryMode(0o644)).toBe(0o755);
    });

    test("should add execute bit where read bit is set (600 → 700)", () => {
      expect(fileModeToDirectoryMode(0o600)).toBe(0o700);
    });

    test("should add execute bit where read bit is set (664 → 775)", () => {
      expect(fileModeToDirectoryMode(0o664)).toBe(0o775);
    });

    test("should add execute bit where read bit is set (640 → 750)", () => {
      expect(fileModeToDirectoryMode(0o640)).toBe(0o750);
    });

    test("should not add execute bit where read bit is not set (000 → 000)", () => {
      expect(fileModeToDirectoryMode(0o000)).toBe(0o000);
    });

    test("should preserve existing execute bits (755 → 755)", () => {
      expect(fileModeToDirectoryMode(0o755)).toBe(0o755);
    });
  });

  describe("getEffectiveDirMode", () => {
    test("should return explicit dirMode if provided", () => {
      const ownership: Ownership = { uid: 1000, gid: 1000, mode: 0o644, dirMode: 0o700 };
      expect(getEffectiveDirMode(ownership)).toBe(0o700);
    });

    test("should derive dirMode from fileMode if not provided", () => {
      const ownership: Ownership = { uid: 1000, gid: 1000, mode: 0o644 };
      expect(getEffectiveDirMode(ownership)).toBe(0o755);
    });

    test("should derive dirMode from fileMode if dirMode is undefined", () => {
      const ownership: Ownership = { uid: 1000, gid: 1000, mode: 0o600, dirMode: undefined };
      expect(getEffectiveDirMode(ownership)).toBe(0o700);
    });
  });

  describe("parseOwnershipHeaders with dirMode", () => {
    test("should parse dirMode header", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-Owner-GID": "1000",
        "X-File-Mode": "644",
        "X-Dir-Mode": "700",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).not.toBeNull();
      expect(result?.dirMode).toBe(0o700);
    });

    test("should have undefined dirMode if header not provided", () => {
      const headers = new Headers({
        "X-Owner-UID": "1000",
        "X-Owner-GID": "1000",
        "X-File-Mode": "644",
      });
      const req = new Request("http://localhost", { headers });

      const result = parseOwnershipHeaders(req);
      expect(result).not.toBeNull();
      expect(result?.dirMode).toBeUndefined();
    });
  });

  describe("parseOwnershipBody with dirMode", () => {
    test("should parse dirMode from body", () => {
      const body = {
        ownerUid: 1000,
        ownerGid: 1000,
        mode: "644",
        dirMode: "700",
      };

      const result = parseOwnershipBody(body);
      expect(result).not.toBeNull();
      expect(result?.dirMode).toBe(0o700);
    });

    test("should have undefined dirMode if not provided in body", () => {
      const body = {
        ownerUid: 1000,
        ownerGid: 1000,
        mode: "644",
      };

      const result = parseOwnershipBody(body);
      expect(result).not.toBeNull();
      expect(result?.dirMode).toBeUndefined();
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
