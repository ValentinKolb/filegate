import { ClientCore } from "./core.js";
import type {
  IndexResolveManyResponse,
  IndexResolveSingleResponse,
  Node,
  OKResponse,
} from "./types.js";

export class IndexClient {
  constructor(private readonly core: ClientCore) {}

  /** Trigger a full index rescan. */
  async rescan(): Promise<OKResponse> {
    return this.core.doJSON<OKResponse>("POST", "/v1/index/rescan");
  }

  /** Resolve a single virtual path to a node. */
  async resolvePath(path: string): Promise<Node | null> {
    const resp = await this.core.doJSON<IndexResolveSingleResponse>(
      "POST",
      "/v1/index/resolve",
      undefined,
      JSON.stringify({ path }),
      "application/json",
    );
    return resp.item;
  }

  /** Resolve multiple virtual paths to nodes. */
  async resolvePaths(paths: string[]): Promise<IndexResolveManyResponse> {
    return this.core.doJSON<IndexResolveManyResponse>(
      "POST",
      "/v1/index/resolve",
      undefined,
      JSON.stringify({ paths }),
      "application/json",
    );
  }

  /** Resolve a single node ID. */
  async resolveId(id: string): Promise<Node | null> {
    const resp = await this.core.doJSON<IndexResolveSingleResponse>(
      "POST",
      "/v1/index/resolve",
      undefined,
      JSON.stringify({ id }),
      "application/json",
    );
    return resp.item;
  }

  /** Resolve multiple node IDs. */
  async resolveIds(ids: string[]): Promise<IndexResolveManyResponse> {
    return this.core.doJSON<IndexResolveManyResponse>(
      "POST",
      "/v1/index/resolve",
      undefined,
      JSON.stringify({ ids }),
      "application/json",
    );
  }
}
