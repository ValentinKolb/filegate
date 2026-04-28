import { ClientCore } from "./core.js";
import type { TransferRequest, TransferResponse } from "./types.js";

export class TransfersClient {
  constructor(private readonly core: ClientCore) {}

  /** Execute a move or copy operation. */
  async create(req: TransferRequest, recursiveOwnership?: boolean): Promise<TransferResponse> {
    const q: Record<string, string> = {};
    if (recursiveOwnership !== undefined) q["recursiveOwnership"] = String(recursiveOwnership);
    return this.core.doJSON<TransferResponse>(
      "POST",
      "/v1/transfers",
      q,
      JSON.stringify(req),
      "application/json",
    );
  }
}
