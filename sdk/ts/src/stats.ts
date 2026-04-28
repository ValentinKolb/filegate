import { ClientCore } from "./core.js";
import type { StatsResponse } from "./types.js";

export class StatsClient {
  constructor(private readonly core: ClientCore) {}

  /** Get runtime statistics. */
  async get(): Promise<StatsResponse> {
    return this.core.doJSON<StatsResponse>("GET", "/v1/stats");
  }
}
