import { createHmac, timingSafeEqual } from "node:crypto";
import type { Context, MiddlewareHandler } from "hono";
import { deleteCookie, getCookie, setCookie } from "hono/cookie";
import { env } from "./env";

const cookieName = "filegate_admin";

function sessionValue(): string {
  const cfg = env();
  return createHmac("sha256", cfg.sessionSecret)
    .update("filegate-admin-session-v1:")
    .update(cfg.adminToken)
    .digest("hex");
}

function equal(a: string, b: string): boolean {
  const ab = Buffer.from(a);
  const bb = Buffer.from(b);
  return ab.length === bb.length && timingSafeEqual(ab, bb);
}

export function authorized(c: Context): boolean {
  const cookie = getCookie(c, cookieName);
  return !!cookie && equal(cookie, sessionValue());
}

export function requireAuth(): MiddlewareHandler {
  return async (c, next) => {
    if (authorized(c)) return next();
    return c.redirect("/login", 303);
  };
}

export async function login(c: Context): Promise<Response> {
  const body = await c.req.parseBody();
  const token = String(body.token || "");
  if (!equal(token, env().adminToken)) {
    return c.redirect("/login?error=invalid", 303);
  }
  setCookie(c, cookieName, sessionValue(), {
    httpOnly: true,
    sameSite: "Strict",
    secure: new URL(c.req.url).protocol === "https:",
    path: "/",
    maxAge: 60 * 60 * 12,
  });
  return c.redirect("/", 303);
}

export function logout(c: Context): Response {
  deleteCookie(c, cookieName, { path: "/" });
  return c.redirect("/login", 303);
}
