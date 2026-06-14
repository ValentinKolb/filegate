import { ClientCore } from "./core.js";
import type { CapabilitiesResponse } from "./types.js";

export class CapabilitiesClient {
  constructor(private readonly core: ClientCore) {}

  /** Get server capability limits for adaptive clients. */
  async get(): Promise<CapabilitiesResponse> {
    return this.core.doJSON<CapabilitiesResponse>("GET", "/v1/capabilities");
  }
}
