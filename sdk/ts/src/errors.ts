import type { ErrorResponse } from "./types.js";

/** Thrown when Filegate responds with a non-2xx status. */
export class FilegateError extends Error {
  readonly status: number;
  readonly method: string;
  readonly path: string;
  /** Raw response body text. Always present (may be empty). */
  readonly body: string;
  /** Parsed `{ error, existingId?, existingPath? }` envelope when the body
   * was valid JSON in the documented shape. Use this for diagnostic fields
   * on 409 — `existingId` and `existingPath` give you everything you need
   * to render a "what should we do?" prompt. `undefined` if the body was
   * empty or not parseable as that shape. */
  readonly errorResponse?: ErrorResponse;

  constructor(
    status: number,
    message: string,
    method: string,
    path: string,
    body: string,
    errorResponse?: ErrorResponse,
  ) {
    super(`filegate api error (${status}): ${message}`);
    this.name = "FilegateError";
    this.status = status;
    this.method = method;
    this.path = path;
    this.body = body;
    this.errorResponse = errorResponse;
  }
}

export async function ensureSuccess(resp: Response, method: string, path: string): Promise<void> {
  if (resp.ok) return;
  const body = await resp.text().catch(() => "");
  let message = resp.statusText || "request failed";
  let parsed: ErrorResponse | undefined;
  try {
    const obj = JSON.parse(body) as ErrorResponse;
    if (obj && typeof obj.error === "string") {
      parsed = obj;
      message = obj.error;
    }
  } catch {
    // body wasn't JSON; fall back to statusText
  }
  throw new FilegateError(resp.status, message, method, path, body, parsed);
}
