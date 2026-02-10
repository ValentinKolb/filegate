import { resolver, type GenerateSpecOptions } from "hono-openapi";
import type { ZodType } from "zod";
import { config } from "../config";

/** JSON response helper for OpenAPI docs */
export const jsonResponse = <T extends ZodType>(schema: T, description: string) => ({
  description,
  content: { "application/json": { schema: resolver(schema) } },
});

/** Binary/stream response helper */
export const binaryResponse = (mimeType: string, description: string) => ({
  description,
  content: { [mimeType]: { schema: { type: "string" as const, format: "binary" } } },
});

/** OpenAPI spec metadata */
export const openApiMeta: Partial<GenerateSpecOptions> = {
  documentation: {
    info: {
      title: "File Proxy API",
      version: "1.0.0",
      description: "Secure file proxy with streaming uploads/downloads and resumable chunked uploads",
    },
    servers: [{ url: `http://localhost:${config.port}`, description: "File Proxy Server" }],
    tags: [
      { name: "Health", description: "Health check endpoint" },
      { name: "Files", description: "File operations (info, download, upload, mkdir, move, copy, delete)" },
      { name: "Search", description: "File search with glob patterns" },
      { name: "Upload", description: "Resumable chunked uploads" },
      { name: "Index", description: "File index management" },
    ],
    components: {
      securitySchemes: {
        bearerAuth: {
          type: "http",
          scheme: "bearer",
          description: "Bearer token authentication",
        },
      },
    },
  },
};

/** Security requirement for authenticated routes */
export const requiresAuth = {
  security: [{ bearerAuth: [] as string[] }],
};
