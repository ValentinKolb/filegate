import { ClientCore } from "./core.js";
import type { DirectDownloadURLRequest, DirectDownloadURLResponse } from "./types.js";

export class DownloadsClient {
  constructor(private readonly core: ClientCore) {}

  /** Mint a short-lived direct GET URL for a file or directory download. */
  async createDirectURL(req: DirectDownloadURLRequest): Promise<DirectDownloadURLResponse> {
    return this.core.doJSON<DirectDownloadURLResponse>(
      "POST",
      "/v1/downloads/direct",
      undefined,
      JSON.stringify(req),
      "application/json",
    );
  }
}
