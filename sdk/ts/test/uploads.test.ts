import { describe, expect, test } from "bun:test";
import { FilegateError } from "../src/errors.js";
import { uploadDirect } from "../src/uploads.js";
import type { Node } from "../src/types.js";

const node: Node = {
  id: "node-1",
  type: "file",
  name: "hello.txt",
  path: "data/hello.txt",
  size: 5,
  mtime: 1,
  ownership: { uid: 1000, gid: 1000, mode: "644" },
  exif: {},
};

describe("uploadDirect", () => {
  test("uploads with a presigned URL and runs success/finish callbacks", async () => {
    const calls: string[] = [];
    const result = await uploadDirect("https://files.example.test/v1/uploads/direct/token", new Blob(["hello"], { type: "text/plain" }), {
      fetchImpl: async (input, init) => {
        expect(String(input)).toBe("https://files.example.test/v1/uploads/direct/token");
        expect(init?.method).toBe("PUT");
        expect((init?.headers as Record<string, string>)["Content-Type"]).toMatch(/^text\/plain/);
        return new Response(JSON.stringify(node), {
          status: 201,
          headers: {
            "Content-Type": "application/json",
            "X-Node-Id": "node-1",
            "X-Created-Id": "node-1",
          },
        });
      },
      onSuccess: async (res) => {
        calls.push(`success:${res.node.id}`);
      },
      onError: async () => {
        calls.push("error");
      },
      onFinish: async (outcome) => {
        calls.push(outcome.ok ? "finish:ok" : "finish:error");
      },
    });

    expect(result.nodeId).toBe("node-1");
    expect(result.statusCode).toBe(201);
    expect(calls).toEqual(["success:node-1", "finish:ok"]);
  });

  test("reports server errors to onError and onFinish", async () => {
    const calls: string[] = [];
    const err = await uploadDirect("https://files.example.test/v1/uploads/direct/token", "hello", {
      fetchImpl: async () =>
        new Response(JSON.stringify({ error: "path already exists", existingId: "old" }), {
          status: 409,
          statusText: "Conflict",
          headers: { "Content-Type": "application/json" },
        }),
      onError: async (error) => {
        calls.push(error instanceof FilegateError ? `error:${error.status}` : "error:unknown");
      },
      onFinish: async (outcome) => {
        calls.push(outcome.ok ? "finish:ok" : "finish:error");
      },
    }).catch((error) => error);

    expect(err).toBeInstanceOf(FilegateError);
    expect((err as FilegateError).status).toBe(409);
    expect(calls).toEqual(["error:409", "finish:error"]);
  });
});
