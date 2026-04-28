import { ensureSuccess } from "./errors.js";

export type FetchImpl = typeof globalThis.fetch;

export interface CoreConfig {
  baseURL: string;
  token: string;
  fetchImpl: FetchImpl;
  userAgent?: string;
  defaultHeaders?: Record<string, string>;
}

export class ClientCore {
  readonly baseURL: string;
  private readonly token: string;
  private readonly fetchImpl: FetchImpl;
  private readonly userAgent: string;
  private readonly defaultHeaders: Record<string, string>;

  constructor(cfg: CoreConfig) {
    this.baseURL = cfg.baseURL.replace(/\/+$/, "");
    this.token = cfg.token;
    this.fetchImpl = cfg.fetchImpl;
    this.userAgent = cfg.userAgent ?? "filegate-ts-sdk/1";
    this.defaultHeaders = cfg.defaultHeaders ? { ...cfg.defaultHeaders } : {};
  }

  buildURL(endpoint: string, query?: Record<string, string>): string {
    const url = new URL(endpoint, this.baseURL + "/");
    url.pathname = this.baseURL.replace(/^https?:\/\/[^/]*/, "") + (endpoint.startsWith("/") ? endpoint : "/" + endpoint);
    if (query) {
      for (const [k, v] of Object.entries(query)) {
        if (v !== undefined && v !== "") url.searchParams.set(k, v);
      }
    }
    return url.toString();
  }

  private headers(contentType?: string): Record<string, string> {
    const h: Record<string, string> = { ...this.defaultHeaders };
    if (this.userAgent) h["User-Agent"] = this.userAgent;
    if (this.token) h["Authorization"] = `Bearer ${this.token}`;
    if (contentType) h["Content-Type"] = contentType;
    return h;
  }

  /** Issue an HTTP request and return the raw Response without throwing on
   * 4xx/5xx. This is the primitive every relay/passthrough caller wants —
   * upstream errors must reach the downstream client unchanged. Use
   * `doJSON` (or call `ensureSuccess` yourself) when you want the standard
   * "throw on non-2xx" behavior. */
  async doRaw(
    method: string,
    endpoint: string,
    query?: Record<string, string>,
    body?: BodyInit | null,
    contentType?: string,
    extraHeaders?: Record<string, string>,
  ): Promise<Response> {
    const url = this.buildURL(endpoint, query);
    const h = this.headers(contentType);
    if (extraHeaders) Object.assign(h, extraHeaders);
    return this.fetchImpl(url, {
      method,
      headers: h,
      body: body ?? null,
    });
  }

  async doJSON<T>(
    method: string,
    endpoint: string,
    query?: Record<string, string>,
    body?: BodyInit | null,
    contentType?: string,
  ): Promise<T> {
    const resp = await this.doRaw(method, endpoint, query, body, contentType);
    await ensureSuccess(resp, method, endpoint);
    return (await resp.json()) as T;
  }
}
