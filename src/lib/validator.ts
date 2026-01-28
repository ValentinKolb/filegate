import { validator } from "hono-openapi";
import type { ZodType } from "zod";
import type { ValidationTargets, Context } from "hono";

/**
 * Zod validator middleware with OpenAPI support.
 * Returns 400 with error details on validation failure.
 */
export const v = <Target extends keyof ValidationTargets, T extends ZodType>(target: Target, schema: T) =>
  validator(target, schema, (result, c: Context) => {
    if (!result.success) {
      const issues = result.error as readonly { message: string; path?: readonly unknown[] }[];
      const msg = issues
        ?.map((issue) => {
          const path = issue.path?.map((p) => String(p)).join(".");
          return path ? `${path}: ${issue.message}` : issue.message;
        })
        .join(", ");
      return c.json({ error: msg || "validation failed" }, 400);
    }
  });
