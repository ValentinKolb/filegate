import { Hono } from "hono";
import { describeRoute } from "hono-openapi";
import { join } from "node:path";
import { jsonResponse, requiresAuth } from "../lib/openapi";
import { v } from "../lib/validator";
import {
  RescanResponseSchema,
  IndexStatsSchema,
  BulkResolveBodySchema,
  BulkResolveResponseSchema,
  ErrorSchema,
} from "../schemas";
import { config } from "../config";
import { bulkResolve, getIndexStats } from "../lib/index";
import { scanAll } from "../lib/scanner";

const app = new Hono();

app.post(
  "/rescan",
  describeRoute({
    tags: ["Index"],
    summary: "Rescan file index",
    ...requiresAuth,
    responses: {
      200: jsonResponse(RescanResponseSchema, "Rescan completed"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      500: jsonResponse(ErrorSchema, "Internal error"),
    },
  }),
  async (c) => {
    if (!config.indexEnabled) {
      return c.json({ error: "index disabled" }, 400);
    }

    try {
      const result = await scanAll();
      return c.json(result);
    } catch (err) {
      console.error("[Filegate] Index rescan failed:", err);
      return c.json({ error: "rescan failed" }, 500);
    }
  },
);

app.get(
  "/stats",
  describeRoute({
    tags: ["Index"],
    summary: "Get index stats",
    ...requiresAuth,
    responses: {
      200: jsonResponse(IndexStatsSchema, "Index stats"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      500: jsonResponse(ErrorSchema, "Internal error"),
    },
  }),
  async (c) => {
    if (!config.indexEnabled) {
      return c.json({ error: "index disabled" }, 400);
    }

    try {
      const stats = await getIndexStats();
      return c.json(stats);
    } catch (err) {
      console.error("[Filegate] Index stats failed:", err);
      return c.json({ error: "stats failed" }, 500);
    }
  },
);

app.post(
  "/resolve",
  describeRoute({
    tags: ["Index"],
    summary: "Resolve file IDs to paths",
    ...requiresAuth,
    responses: {
      200: jsonResponse(BulkResolveResponseSchema, "Resolved paths"),
      400: jsonResponse(ErrorSchema, "Bad request"),
      500: jsonResponse(ErrorSchema, "Internal error"),
    },
  }),
  v("json", BulkResolveBodySchema),
  async (c) => {
    if (!config.indexEnabled) {
      return c.json({ error: "index disabled" }, 400);
    }

    try {
      const { ids } = c.req.valid("json");
      const resolved = await bulkResolve(ids);
      const response: Record<string, string | null> = {};
      for (const id of ids) {
        const entry = resolved[id];
        response[id] = entry ? join(entry.basePath, entry.relPath) : null;
      }
      return c.json(response);
    } catch (err) {
      console.error("[Filegate] Index resolve failed:", err);
      return c.json({ error: "resolve failed" }, 500);
    }
  },
);

export default app;
