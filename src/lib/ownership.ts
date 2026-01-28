import { chown, chmod } from "node:fs/promises";
import { config } from "../config";

export type Ownership = {
  uid: number;
  gid: number;
  mode: number; // octal, e.g. 0o600
};

export const parseOwnershipHeaders = (req: Request): Ownership | null => {
  const uid = req.headers.get("X-Owner-UID");
  const gid = req.headers.get("X-Owner-GID");
  const mode = req.headers.get("X-File-Mode");

  if (!uid || !gid || !mode) return null;

  const parsedMode = parseInt(mode, 8);
  if (isNaN(parsedMode)) return null;

  return {
    uid: parseInt(uid, 10),
    gid: parseInt(gid, 10),
    mode: parsedMode,
  };
};

export const parseOwnershipBody = (body: {
  ownerUid?: number;
  ownerGid?: number;
  mode?: string;
}): Ownership | null => {
  if (body.ownerUid == null || body.ownerGid == null || !body.mode) return null;

  const mode = parseInt(body.mode, 8);
  if (isNaN(mode)) return null;

  return { uid: body.ownerUid, gid: body.ownerGid, mode };
};

export const applyOwnership = async (
  path: string,
  ownership: Ownership | null
): Promise<string | null> => {
  if (!ownership) return null;

  const uid = config.devUid ?? ownership.uid;
  const gid = config.devGid ?? ownership.gid;

  if (config.isDev) {
    console.log(
      `[DEV] chown ${path}: ${ownership.uid}->${uid}, ${ownership.gid}->${gid}, mode=${ownership.mode.toString(8)}`
    );
  }

  try {
    await chown(path, uid, gid);
    await chmod(path, ownership.mode);
    return null;
  } catch (e: any) {
    if (e.code === "EPERM") return "permission denied (not root?)";
    if (e.code === "EINVAL") return `invalid uid=${uid} or gid=${gid}`;
    return `ownership failed: ${e.message}`;
  }
};
