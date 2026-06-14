import { describe, expect, test } from "bun:test";
import { Filegate } from "../src/client.js";

describe("capabilities", () => {
  test("gets server capabilities", async () => {
    const client = new Filegate({
      baseUrl: "https://files.example.test",
      token: "test-token",
      fetchImpl: async (input, init) => {
        expect(String(input)).toBe("https://files.example.test/v1/capabilities");
        expect(init?.method).toBe("GET");
        expect((init?.headers as Record<string, string>)["Authorization"]).toBe("Bearer test-token");
        return new Response(JSON.stringify({
          uploads: {
            maxChunkBytes: 52428800,
            maxUploadBytes: 524288000,
            maxSessionUploadBytes: 53687091200,
            maxConcurrentSegmentWrites: 64,
          },
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      },
    });

    const out = await client.capabilities.get();
    expect(out.uploads.maxChunkBytes).toBe(52428800);
  });
});
