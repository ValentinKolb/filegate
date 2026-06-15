import { ClientCore } from "./core.js";
import type { ActivityListResponse } from "./types.js";

export type ActivityListConfig = {
  limit?: number;
  offset?: number;
  q?: string;
  operation?: string;
  outcome?: string;
};

export class ActivityClient {
  constructor(private readonly core: ClientCore) {}

  async list(config: ActivityListConfig = {}): Promise<ActivityListResponse> {
    const query: Record<string, string> = {};
    if (config.limit !== undefined) query.limit = String(config.limit);
    if (config.offset !== undefined) query.offset = String(config.offset);
    if (config.q) query.q = config.q;
    if (config.operation) query.operation = config.operation;
    if (config.outcome) query.outcome = config.outcome;
    return this.core.doJSON<ActivityListResponse>("GET", "/v1/activity", query);
  }
}
