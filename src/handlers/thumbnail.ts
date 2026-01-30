import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { stat } from "node:fs/promises";
import sharp from "sharp";
import { validatePath } from "../lib/path";
import { jsonResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
import { ImageThumbnailQuerySchema, ErrorSchema } from "../schemas";

const app = new Hono();

// Generate ETag from path, mtime, and thumbnail parameters
const generateETag = (path: string, mtime: Date, params: string): string => {
  const hasher = new Bun.CryptoHasher("sha256");
  hasher.update(`${path}:${mtime.getTime()}:${params}`);
  return `"${hasher.digest("hex").slice(0, 16)}"`;
};

// Supported image MIME types
const SUPPORTED_IMAGE_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/webp",
  "image/avif",
  "image/tiff",
  "image/gif",
  "image/svg+xml",
]);

// Format to MIME type mapping
const FORMAT_MIME: Record<string, string> = {
  webp: "image/webp",
  jpeg: "image/jpeg",
  png: "image/png",
  avif: "image/avif",
};

// GET /thumbnail/image - Generate image thumbnail
app.get(
  "/image",
  describeRoute({
    tags: ["Thumbnail"],
    summary: "Generate image thumbnail",
    description:
      "Generate a thumbnail from an image file on-the-fly using Sharp. " +
      "Supports JPEG, PNG, WebP, AVIF, TIFF, GIF, and SVG input formats.",
    ...requiresAuth,
    responses: {
      200: {
        description: "Thumbnail image",
        content: {
          "image/webp": { schema: { type: "string", format: "binary" } },
          "image/jpeg": { schema: { type: "string", format: "binary" } },
          "image/png": { schema: { type: "string", format: "binary" } },
          "image/avif": { schema: { type: "string", format: "binary" } },
        },
      },
      400: jsonResponse(ErrorSchema, "Invalid parameters or not an image"),
      403: jsonResponse(ErrorSchema, "Forbidden"),
      404: jsonResponse(ErrorSchema, "Not found"),
    },
  }),
  v("query", ImageThumbnailQuerySchema),
  async (c) => {
    const { path, width, height, fit, position, format, quality } = c.req.valid("query");

    // Validate path
    const result = await validatePath(path);
    if (!result.ok) return c.json({ error: result.error }, result.status);

    // Check if file exists and is a file
    const file = Bun.file(result.realPath);
    if (!(await file.exists())) {
      return c.json({ error: "file not found" }, 404);
    }

    // Check MIME type
    if (!SUPPORTED_IMAGE_TYPES.has(file.type)) {
      return c.json(
        { error: `unsupported image type: ${file.type}. Supported: ${[...SUPPORTED_IMAGE_TYPES].join(", ")}` },
        400,
      );
    }

    // Get file stats for Last-Modified and ETag
    const fileStat = await stat(result.realPath);
    const lastModified = fileStat.mtime;
    const paramsKey = `${width}x${height}:${fit}:${position}:${format}:${quality}`;
    const etag = generateETag(result.realPath, lastModified, paramsKey);

    // Check If-None-Match (ETag)
    const ifNoneMatch = c.req.header("If-None-Match");
    if (ifNoneMatch === etag) {
      return new Response(null, { status: 304 });
    }

    // Check If-Modified-Since
    const ifModifiedSince = c.req.header("If-Modified-Since");
    if (ifModifiedSince) {
      const clientDate = new Date(ifModifiedSince);
      if (lastModified <= clientDate) {
        return new Response(null, { status: 304 });
      }
    }

    try {
      // Read file and process with Sharp
      const buffer = await file.arrayBuffer();

      let pipeline = sharp(Buffer.from(buffer)).resize(width, height, {
        fit,
        position,
      });

      // Apply format and quality
      switch (format) {
        case "webp":
          pipeline = pipeline.webp({ quality });
          break;
        case "jpeg":
          pipeline = pipeline.jpeg({ quality });
          break;
        case "png":
          pipeline = pipeline.png({ quality });
          break;
        case "avif":
          pipeline = pipeline.avif({ quality });
          break;
      }

      const thumbnail = await pipeline.toBuffer();

      return new Response(thumbnail, {
        headers: {
          "Content-Type": FORMAT_MIME[format],
          "Cache-Control": "public, max-age=31536000, immutable",
          "Last-Modified": lastModified.toUTCString(),
          ETag: etag,
        },
      });
    } catch (err) {
      console.error("[Filegate] Thumbnail error:", err);
      return c.json({ error: "failed to generate thumbnail" }, 500);
    }
  },
);

export default app;
