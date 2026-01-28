import { describe, test, expect } from "bun:test";
import { chunks, formatBytes, ChunkedUpload } from "../src/utils";

// ============================================================================
// formatBytes
// ============================================================================

describe("formatBytes", () => {
  test("should format 0 bytes", () => {
    expect(formatBytes({ bytes: 0 })).toBe("0 Bytes");
  });

  test("should format bytes", () => {
    expect(formatBytes({ bytes: 500 })).toBe("500 Bytes");
  });

  test("should format kilobytes", () => {
    expect(formatBytes({ bytes: 1024 })).toBe("1 KB");
    expect(formatBytes({ bytes: 1536 })).toBe("1.5 KB");
  });

  test("should format megabytes", () => {
    expect(formatBytes({ bytes: 1024 * 1024 })).toBe("1 MB");
    expect(formatBytes({ bytes: 1024 * 1024 * 5.5 })).toBe("5.5 MB");
  });

  test("should format gigabytes", () => {
    expect(formatBytes({ bytes: 1024 * 1024 * 1024 })).toBe("1 GB");
  });

  test("should respect decimals option", () => {
    expect(formatBytes({ bytes: 1536, decimals: 0 })).toBe("2 KB");
    expect(formatBytes({ bytes: 1536, decimals: 3 })).toBe("1.5 KB");
  });
});

// ============================================================================
// chunks.prepare
// ============================================================================

describe("chunks.prepare", () => {
  test("should prepare a small file", async () => {
    const file = new Blob(["Hello, World!"], { type: "text/plain" });
    const upload = await chunks.prepare({ file });

    expect(upload.fileSize).toBe(13);
    expect(upload.totalChunks).toBe(1);
    expect(upload.chunkSize).toBe(5 * 1024 * 1024); // default 5MB
    expect(upload.checksum).toMatch(/^sha256:[a-f0-9]{64}$/);
  });

  test("should use custom chunk size", async () => {
    const data = "x".repeat(1000);
    const file = new Blob([data]);
    const upload = await chunks.prepare({ file, chunkSize: 100 });

    expect(upload.fileSize).toBe(1000);
    expect(upload.chunkSize).toBe(100);
    expect(upload.totalChunks).toBe(10);
  });

  test("should calculate correct checksum", async () => {
    const file = new Blob(["test data"]);
    const upload = await chunks.prepare({ file });

    // SHA-256 of "test data"
    expect(upload.checksum).toBe("sha256:916f0027a575074ce72a331777c3478d6513f786a591bd892da1a577bf2335f9");
  });

  test("should handle File objects", async () => {
    const file = new File(["content"], "test.txt", { type: "text/plain" });
    const upload = await chunks.prepare({ file });

    expect(upload.file).toBe(file);
    expect(upload.fileSize).toBe(7);
  });
});

// ============================================================================
// ChunkedUpload.get
// ============================================================================

describe("ChunkedUpload.get", () => {
  test("should get a specific chunk", async () => {
    const data = "0123456789";
    const file = new Blob([data]);
    const upload = await chunks.prepare({ file, chunkSize: 3 });

    expect(upload.totalChunks).toBe(4); // 3+3+3+1

    const chunk0 = upload.get({ index: 0 });
    expect(await chunk0.text()).toBe("012");

    const chunk1 = upload.get({ index: 1 });
    expect(await chunk1.text()).toBe("345");

    const chunk2 = upload.get({ index: 2 });
    expect(await chunk2.text()).toBe("678");

    const chunk3 = upload.get({ index: 3 });
    expect(await chunk3.text()).toBe("9");
  });
});

// ============================================================================
// ChunkedUpload.hash
// ============================================================================

describe("ChunkedUpload.hash", () => {
  test("should hash a Blob", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file });

    const blob = new Blob(["chunk data"]);
    const hash = await upload.hash({ data: blob });

    expect(hash).toMatch(/^sha256:[a-f0-9]{64}$/);
  });

  test("should hash an ArrayBuffer", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file });

    const buffer = new TextEncoder().encode("chunk data").buffer;
    const hash = await upload.hash({ data: buffer });

    expect(hash).toMatch(/^sha256:[a-f0-9]{64}$/);
  });

  test("should produce consistent hashes", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file });

    const blob1 = new Blob(["same"]);
    const blob2 = new Blob(["same"]);

    const hash1 = await upload.hash({ data: blob1 });
    const hash2 = await upload.hash({ data: blob2 });

    expect(hash1).toBe(hash2);
  });
});

// ============================================================================
// ChunkedUpload state management
// ============================================================================

describe("ChunkedUpload state", () => {
  test("should have initial state", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    expect(upload.state).toEqual({
      uploaded: 0,
      total: 4,
      percent: 0,
      status: "pending",
    });
  });

  test("should update state on complete", async () => {
    const data = "1234567890";
    const file = new Blob([data]);
    const upload = await chunks.prepare({ file, chunkSize: 2 });

    expect(upload.totalChunks).toBe(5);

    upload.complete({ index: 0 });
    expect(upload.state.uploaded).toBe(1);
    expect(upload.state.percent).toBe(20);
    expect(upload.state.status).toBe("uploading");

    upload.complete({ index: 1 });
    expect(upload.state.uploaded).toBe(2);
    expect(upload.state.percent).toBe(40);

    // Complete remaining
    upload.complete({ index: 2 });
    upload.complete({ index: 3 });
    upload.complete({ index: 4 });

    expect(upload.state.uploaded).toBe(5);
    expect(upload.state.percent).toBe(100);
    expect(upload.state.status).toBe("completed");
  });

  test("should ignore duplicate complete calls", async () => {
    const file = new Blob(["12345"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    upload.complete({ index: 0 });
    upload.complete({ index: 0 });
    upload.complete({ index: 0 });

    expect(upload.state.uploaded).toBe(1);
  });

  test("should reset state", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    upload.complete({ index: 0 });
    upload.complete({ index: 1 });
    expect(upload.state.uploaded).toBe(2);

    upload.reset();
    expect(upload.state).toEqual({
      uploaded: 0,
      total: 4,
      percent: 0,
      status: "pending",
    });
  });
});

// ============================================================================
// ChunkedUpload.subscribe
// ============================================================================

describe("ChunkedUpload.subscribe", () => {
  test("should emit current state immediately", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const states: (typeof upload.state)[] = [];
    upload.subscribe((state) => states.push(state));

    expect(states.length).toBe(1);
    expect(states[0]!.uploaded).toBe(0);
  });

  test("should emit on state changes", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const states: (typeof upload.state)[] = [];
    upload.subscribe((state) => states.push(state));

    upload.complete({ index: 0 });
    upload.complete({ index: 1 });

    expect(states.length).toBe(3); // initial + 2 updates
    expect(states[1]!.uploaded).toBe(1);
    expect(states[2]!.uploaded).toBe(2);
  });

  test("should allow unsubscribe", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const states: (typeof upload.state)[] = [];
    const unsubscribe = upload.subscribe((state) => states.push(state));

    upload.complete({ index: 0 });
    unsubscribe();
    upload.complete({ index: 1 });

    expect(states.length).toBe(2); // initial + 1 update (before unsubscribe)
  });

  test("should support multiple subscribers", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const states1: (typeof upload.state)[] = [];
    const states2: (typeof upload.state)[] = [];

    upload.subscribe((state) => states1.push(state));
    upload.subscribe((state) => states2.push(state));

    upload.complete({ index: 0 });

    expect(states1.length).toBe(2);
    expect(states2.length).toBe(2);
  });
});

// ============================================================================
// ChunkedUpload async iterator
// ============================================================================

describe("ChunkedUpload async iterator", () => {
  test("should iterate over all chunks", async () => {
    const data = "0123456789";
    const file = new Blob([data]);
    const upload = await chunks.prepare({ file, chunkSize: 3 });

    const results: { index: number; text: string; total: number }[] = [];

    for await (const chunk of upload) {
      results.push({
        index: chunk.index,
        text: await chunk.data.text(),
        total: chunk.total,
      });
    }

    expect(results).toEqual([
      { index: 0, text: "012", total: 4 },
      { index: 1, text: "345", total: 4 },
      { index: 2, text: "678", total: 4 },
      { index: 3, text: "9", total: 4 },
    ]);
  });
});

// ============================================================================
// ChunkedUpload.send
// ============================================================================

describe("ChunkedUpload.send", () => {
  test("should call fn and complete chunk", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const calls: { index: number; data: string }[] = [];

    await upload.send({
      index: 0,
      fn: async ({ index, data }) => {
        calls.push({ index, data: await data.text() });
      },
    });

    expect(calls).toEqual([{ index: 0, data: "t" }]);
    expect(upload.state.uploaded).toBe(1);
  });

  test("should retry on failure", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    let attempts = 0;

    await upload.send({
      index: 0,
      retries: 2,
      fn: async () => {
        attempts++;
        if (attempts < 3) {
          throw new Error("fail");
        }
      },
    });

    expect(attempts).toBe(3); // 1 initial + 2 retries
    expect(upload.state.uploaded).toBe(1);
  });

  test("should throw after max retries", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    let attempts = 0;

    await expect(
      upload.send({
        index: 0,
        retries: 2,
        fn: async () => {
          attempts++;
          throw new Error("always fail");
        },
      }),
    ).rejects.toThrow("always fail");

    expect(attempts).toBe(3);
    expect(upload.state.status).toBe("error");
  });
});

// ============================================================================
// ChunkedUpload.sendAll
// ============================================================================

describe("ChunkedUpload.sendAll", () => {
  test("should send all chunks", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const calls: number[] = [];

    await upload.sendAll({
      fn: async ({ index }) => {
        calls.push(index);
      },
    });

    expect(calls).toEqual([0, 1, 2, 3]);
    expect(upload.state.uploaded).toBe(4);
    expect(upload.state.status).toBe("completed");
  });

  test("should skip specified chunks", async () => {
    const file = new Blob(["test"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const calls: number[] = [];

    await upload.sendAll({
      skip: [0, 2],
      fn: async ({ index }) => {
        calls.push(index);
      },
    });

    expect(calls).toEqual([1, 3]);
    expect(upload.state.uploaded).toBe(4); // All marked complete (2 skipped + 2 sent)
  });

  test("should handle concurrent uploads", async () => {
    const file = new Blob(["123456"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const order: number[] = [];
    const delays = [50, 10, 30, 20, 40, 5]; // Different delays for each chunk

    await upload.sendAll({
      concurrency: 3,
      fn: async ({ index }) => {
        await new Promise((r) => setTimeout(r, delays[index]));
        order.push(index);
      },
    });

    expect(upload.state.uploaded).toBe(6);
    // Order won't be sequential due to concurrency
    expect(order.sort()).toEqual([0, 1, 2, 3, 4, 5]);
  });

  test("should handle retries in sendAll", async () => {
    const file = new Blob(["ab"]);
    const upload = await chunks.prepare({ file, chunkSize: 1 });

    const attempts: Record<number, number> = { 0: 0, 1: 0 };

    await upload.sendAll({
      retries: 2,
      fn: async ({ index }) => {
        attempts[index] = (attempts[index] ?? 0) + 1;
        if (index === 1 && (attempts[1] ?? 0) < 2) {
          throw new Error("fail once");
        }
      },
    });

    expect(attempts[0]).toBe(1);
    expect(attempts[1]).toBe(2); // Failed once, then succeeded
    expect(upload.state.uploaded).toBe(2);
  });
});
