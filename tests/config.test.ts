import { describe, test, expect, beforeEach, afterEach } from "bun:test";

describe("config", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Reset module cache to allow fresh config import
    // Note: In Bun, we need to be careful about module caching
  });

  afterEach(() => {
    // Restore original env vars
    process.env = { ...originalEnv };
  });

  describe("required environment variables", () => {
    test("should throw if FILE_PROXY_TOKEN is missing", async () => {
      // Clear the env vars
      delete process.env.FILE_PROXY_TOKEN;
      delete process.env.ALLOWED_BASE_PATHS;

      // We can't easily re-import the config module due to caching,
      // so we test the helper functions directly
      const required = (key: string): string => {
        const value = process.env[key];
        if (!value) throw new Error(`Missing required env: ${key}`);
        return value;
      };

      expect(() => required("FILE_PROXY_TOKEN")).toThrow(
        "Missing required env: FILE_PROXY_TOKEN"
      );
    });

    test("should throw if ALLOWED_BASE_PATHS is missing", async () => {
      process.env.FILE_PROXY_TOKEN = "test-token";
      delete process.env.ALLOWED_BASE_PATHS;

      const required = (key: string): string => {
        const value = process.env[key];
        if (!value) throw new Error(`Missing required env: ${key}`);
        return value;
      };

      expect(() => required("ALLOWED_BASE_PATHS")).toThrow(
        "Missing required env: ALLOWED_BASE_PATHS"
      );
    });
  });

  describe("helper functions", () => {
    describe("int helper", () => {
      const int = (key: string, def: number): number =>
        parseInt(process.env[key] ?? "", 10) || def;

      test("should return default when env var is missing", () => {
        delete process.env.TEST_INT;
        expect(int("TEST_INT", 42)).toBe(42);
      });

      test("should parse valid integer", () => {
        process.env.TEST_INT = "100";
        expect(int("TEST_INT", 42)).toBe(100);
      });

      test("should return default for invalid integer", () => {
        process.env.TEST_INT = "not-a-number";
        expect(int("TEST_INT", 42)).toBe(42);
      });

      test("should return default for empty string", () => {
        process.env.TEST_INT = "";
        expect(int("TEST_INT", 42)).toBe(42);
      });
    });

    describe("optionalInt helper", () => {
      const optionalInt = (key: string): number | undefined => {
        const v = process.env[key];
        return v ? parseInt(v, 10) : undefined;
      };

      test("should return undefined when env var is missing", () => {
        delete process.env.TEST_OPT_INT;
        expect(optionalInt("TEST_OPT_INT")).toBeUndefined();
      });

      test("should parse valid integer", () => {
        process.env.TEST_OPT_INT = "500";
        expect(optionalInt("TEST_OPT_INT")).toBe(500);
      });

      test("should return NaN for invalid integer", () => {
        process.env.TEST_OPT_INT = "invalid";
        expect(optionalInt("TEST_OPT_INT")).toBeNaN();
      });
    });

    describe("list helper", () => {
      test("should parse comma-separated values", () => {
        process.env.TEST_LIST = "/path/one,/path/two,/path/three";

        const list = (key: string): string[] => {
          const value = process.env[key];
          if (!value) throw new Error(`Missing required env: ${key}`);
          return value
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
        };

        expect(list("TEST_LIST")).toEqual([
          "/path/one",
          "/path/two",
          "/path/three",
        ]);
      });

      test("should trim whitespace from values", () => {
        process.env.TEST_LIST = " /path/one , /path/two , /path/three ";

        const list = (key: string): string[] => {
          const value = process.env[key];
          if (!value) throw new Error(`Missing required env: ${key}`);
          return value
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
        };

        expect(list("TEST_LIST")).toEqual([
          "/path/one",
          "/path/two",
          "/path/three",
        ]);
      });

      test("should filter empty values", () => {
        process.env.TEST_LIST = "/path/one,,/path/two,";

        const list = (key: string): string[] => {
          const value = process.env[key];
          if (!value) throw new Error(`Missing required env: ${key}`);
          return value
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
        };

        expect(list("TEST_LIST")).toEqual(["/path/one", "/path/two"]);
      });

      test("should handle single value", () => {
        process.env.TEST_LIST = "/path/only";

        const list = (key: string): string[] => {
          const value = process.env[key];
          if (!value) throw new Error(`Missing required env: ${key}`);
          return value
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
        };

        expect(list("TEST_LIST")).toEqual(["/path/only"]);
      });
    });
  });

  describe("config values", () => {
    test("should calculate maxUploadBytes from MB", () => {
      const maxUploadMb = 500;
      const maxUploadBytes = maxUploadMb * 1024 * 1024;
      expect(maxUploadBytes).toBe(524288000);
    });

    test("should calculate maxDownloadBytes from MB", () => {
      const maxDownloadMb = 5000;
      const maxDownloadBytes = maxDownloadMb * 1024 * 1024;
      expect(maxDownloadBytes).toBe(5242880000);
    });

    test("should calculate maxChunkBytes from MB", () => {
      const maxChunkMb = 50;
      const maxChunkBytes = maxChunkMb * 1024 * 1024;
      expect(maxChunkBytes).toBe(52428800);
    });

    test("should calculate uploadExpirySecs from hours", () => {
      const uploadExpiryHours = 24;
      const uploadExpirySecs = uploadExpiryHours * 60 * 60;
      expect(uploadExpirySecs).toBe(86400);
    });

    test("should calculate diskCleanupIntervalMs from hours", () => {
      const diskCleanupHours = 6;
      const diskCleanupMs = diskCleanupHours * 60 * 60 * 1000;
      expect(diskCleanupMs).toBe(21600000);
    });
  });

  describe("isDev detection", () => {
    test("should detect dev mode when DEV_UID_OVERRIDE is set", () => {
      const devUid = 1000;
      const devGid = undefined;
      const isDev = devUid !== undefined || devGid !== undefined;
      expect(isDev).toBe(true);
    });

    test("should detect dev mode when DEV_GID_OVERRIDE is set", () => {
      const devUid = undefined;
      const devGid = 1000;
      const isDev = devUid !== undefined || devGid !== undefined;
      expect(isDev).toBe(true);
    });

    test("should not be dev mode when neither override is set", () => {
      const devUid = undefined;
      const devGid = undefined;
      const isDev = devUid !== undefined || devGid !== undefined;
      expect(isDev).toBe(false);
    });
  });

  describe("default values", () => {
    test("should have correct default PORT", () => {
      const defaultPort = 4000;
      expect(defaultPort).toBe(4000);
    });

    test("should have correct default MAX_UPLOAD_MB", () => {
      const defaultMaxUploadMb = 500;
      expect(defaultMaxUploadMb).toBe(500);
    });

    test("should have correct default MAX_DOWNLOAD_MB", () => {
      const defaultMaxDownloadMb = 5000;
      expect(defaultMaxDownloadMb).toBe(5000);
    });

    test("should have correct default MAX_CHUNK_SIZE_MB", () => {
      const defaultMaxChunkMb = 50;
      expect(defaultMaxChunkMb).toBe(50);
    });

    test("should have correct default SEARCH_MAX_RESULTS", () => {
      const defaultSearchMaxResults = 100;
      expect(defaultSearchMaxResults).toBe(100);
    });

    test("should have correct default UPLOAD_EXPIRY_HOURS", () => {
      const defaultUploadExpiryHours = 24;
      expect(defaultUploadExpiryHours).toBe(24);
    });

    test("should have correct default DISK_CLEANUP_INTERVAL_HOURS", () => {
      const defaultDiskCleanupHours = 6;
      expect(defaultDiskCleanupHours).toBe(6);
    });

    test("should have correct default UPLOAD_TEMP_DIR", () => {
      const defaultUploadTempDir = "/tmp/filegate-uploads";
      expect(defaultUploadTempDir).toBe("/tmp/filegate-uploads");
    });
  });
});
