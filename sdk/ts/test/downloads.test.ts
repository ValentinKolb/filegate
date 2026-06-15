import { describe, expect, test } from "bun:test";
import { Filegate } from "../src/client.js";
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

describe("downloads", () => {
  test("creates a direct download URL", async () => {
    const client = new Filegate({
      baseUrl: "https://filegate.example.test",
      token: "secret",
      fetchImpl: async (input, init) => {
        expect(String(input)).toBe("https://filegate.example.test/v1/downloads/direct");
        expect(init?.method).toBe("POST");
        expect((init?.headers as Record<string, string>)["Authorization"]).toBe("Bearer secret");
        expect(await new Response(init?.body as BodyInit).json()).toEqual({
          nodeId: "node-1",
          expiresInSeconds: 60,
        });
        return new Response(JSON.stringify({
          downloadUrl: "https://filegate.example.test/v1/downloads/direct/token",
          method: "GET",
          expiresAt: 42,
          node,
        }), { status: 201, headers: { "Content-Type": "application/json" } });
      },
    });

    const out = await client.downloads.createDirectURL({ nodeId: "node-1", expiresInSeconds: 60 });
    expect(out.method).toBe("GET");
    expect(out.node.id).toBe("node-1");
    expect(out.downloadUrl).toContain("/v1/downloads/direct/");
  });
});
