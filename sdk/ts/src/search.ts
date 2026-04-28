import { ClientCore } from "./core.js";
import type { GlobSearchResponse } from "./types.js";

export interface GlobOptions {
  pattern: string;
  paths?: string[];
  limit?: number;
  showHidden?: boolean;
  files?: boolean;
  directories?: boolean;
}

export class SearchClient {
  constructor(private readonly core: ClientCore) {}

  /** Search for nodes matching a glob pattern. */
  async glob(opts: GlobOptions): Promise<GlobSearchResponse> {
    const pattern = opts.pattern.trim();
    if (!pattern) throw new Error("pattern is required");
    const q: Record<string, string> = { pattern };
    if (opts.limit) q["limit"] = String(opts.limit);
    if (opts.paths?.length) q["paths"] = opts.paths.join(",");
    if (opts.showHidden) q["showHidden"] = "true";
    if (opts.files !== undefined) q["files"] = String(opts.files);
    if (opts.directories !== undefined) q["directories"] = String(opts.directories);
    return this.core.doJSON<GlobSearchResponse>("GET", "/v1/search/glob", q);
  }
}
