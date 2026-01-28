import { Hono } from "hono";
import { Scalar } from "@scalar/hono-api-reference";
import { generateSpecs } from "hono-openapi";
import { createMarkdownFromOpenApi } from "@scalar/openapi-to-markdown";
import { bearerAuth } from "hono/bearer-auth";
import { secureHeaders } from "hono/secure-headers";
import { config } from "./config";
import { openApiMeta } from "./lib/openapi";
import filesRoutes from "./handlers/files";
import searchRoutes from "./handlers/search";
import uploadRoutes, { cleanupOrphanedChunks } from "./handlers/upload";

// Dev mode warning
if (config.isDev) {
  console.log("╔════════════════════════════════════════════════════════════════╗");
  console.log("║  WARNING: DEV MODE - UID/GID OVERRIDES ENABLED                 ║");
  console.log("║  DO NOT USE IN PRODUCTION!                                     ║");
  console.log(`║  DEV_UID_OVERRIDE: ${String(config.devUid ?? "not set").padEnd(43)}║`);
  console.log(`║  DEV_GID_OVERRIDE: ${String(config.devGid ?? "not set").padEnd(43)}║`);
  console.log("╚════════════════════════════════════════════════════════════════╝");
}

console.log(`[Filegate] ALLOWED_BASE_PATHS: ${config.allowedPaths.join(", ")}`);
console.log(`[Filegate] MAX_UPLOAD_MB: ${config.maxUploadBytes / 1024 / 1024}`);
console.log(`[Filegate] REDIS_URL: ${process.env.REDIS_URL ?? "redis://localhost:6379 (default)"}`);
console.log(`[Filegate] PORT: ${config.port}`);

// Periodic disk cleanup for orphaned chunks (every 6h by default)
setInterval(cleanupOrphanedChunks, config.diskCleanupIntervalMs);
setTimeout(cleanupOrphanedChunks, 10_000); // Run 10s after startup

// Main app
const app = new Hono();

// Security headers
app.use(
  "*",
  secureHeaders({
    xFrameOptions: "DENY",
    xContentTypeOptions: "nosniff",
    referrerPolicy: "strict-origin-when-cross-origin",
    crossOriginOpenerPolicy: "same-origin",
    crossOriginResourcePolicy: "same-origin",
  }),
);

// Health check (public)
app.get("/health", (c) => c.text("OK"));

// Protected routes
const api = new Hono()
  .use("/*", bearerAuth({ token: config.token }))
  .route("/", filesRoutes)
  .route("/", searchRoutes)
  .route("/upload", uploadRoutes);

app.route("/files", api);

// Generate OpenAPI spec
const spec = await generateSpecs(app, openApiMeta);
const llmsTxt = await createMarkdownFromOpenApi(JSON.stringify(spec));

// Documentation endpoints (public)
app.get("/openapi.json", (c) => c.json(spec));
app.get("/docs", Scalar({ theme: "saturn", url: "/openapi.json" }));
app.get("/llms.txt", (c) => c.text(llmsTxt));

// 404 fallback
app.notFound((c) => c.json({ error: "not found" }, 404));

// Error handler
app.onError((err, c) => {
  console.error("[Filegate] Error:", err);
  return c.json({ error: "internal error" }, 500);
});

export default {
  port: config.port,
  fetch: app.fetch,
};

console.log(`[Filegate] Listening on http://localhost:${config.port}`);
console.log(`[Filegate] Docs: http://localhost:${config.port}/docs`);
