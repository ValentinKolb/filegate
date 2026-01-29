import { chown, chmod } from "node:fs/promises";
import { dirname } from "node:path";
import { config } from "../config";

export type Ownership = {
  uid: number;
  gid: number;
  mode: number; // octal, e.g. 0o600
  dirMode?: number; // octal, optional - if not set, derived from mode
};

// Derive directory mode from file mode (add x where r is set)
// 644 → 755, 600 → 700, 664 → 775
export const fileModeToDirectoryMode = (fileMode: number): number => {
  let dirMode = fileMode;
  if (fileMode & 0o400) dirMode |= 0o100; // owner read → owner exec
  if (fileMode & 0o040) dirMode |= 0o010; // group read → group exec
  if (fileMode & 0o004) dirMode |= 0o001; // other read → other exec
  return dirMode;
};

// Get effective directory mode (explicit or derived from file mode)
export const getEffectiveDirMode = (ownership: Ownership): number => {
  return ownership.dirMode ?? fileModeToDirectoryMode(ownership.mode);
};

export const parseOwnershipHeaders = (req: Request): Ownership | null => {
  const uid = req.headers.get("X-Owner-UID");
  const gid = req.headers.get("X-Owner-GID");
  const mode = req.headers.get("X-File-Mode");
  const dirMode = req.headers.get("X-Dir-Mode");

  if (!uid || !gid || !mode) return null;

  const parsedMode = parseInt(mode, 8);
  if (isNaN(parsedMode)) return null;

  const parsedDirMode = dirMode ? parseInt(dirMode, 8) : undefined;
  if (dirMode && isNaN(parsedDirMode!)) return null;

  return {
    uid: parseInt(uid, 10),
    gid: parseInt(gid, 10),
    mode: parsedMode,
    dirMode: parsedDirMode,
  };
};

export const parseOwnershipBody = (body: {
  ownerUid?: number;
  ownerGid?: number;
  mode?: string;
  dirMode?: string;
}): Ownership | null => {
  if (body.ownerUid == null || body.ownerGid == null || !body.mode) return null;

  const mode = parseInt(body.mode, 8);
  if (isNaN(mode)) return null;

  const dirMode = body.dirMode ? parseInt(body.dirMode, 8) : undefined;
  if (body.dirMode && isNaN(dirMode!)) return null;

  return { uid: body.ownerUid, gid: body.ownerGid, mode, dirMode };
};

export const applyOwnership = async (path: string, ownership: Ownership | null): Promise<string | null> => {
  if (!ownership) return null;

  const uid = config.devUid ?? ownership.uid;
  const gid = config.devGid ?? ownership.gid;

  if (config.isDev) {
    console.log(
      `[DEV] chown ${path}: ${ownership.uid}->${uid}, ${ownership.gid}->${gid}, mode=${ownership.mode.toString(8)}`,
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

// Apply ownership to directory chain from targetDir up to (not including) basePath
export const applyOwnershipToNewDirs = async (
  targetDir: string,
  basePath: string,
  ownership: Ownership,
): Promise<void> => {
  const dirMode = getEffectiveDirMode(ownership);
  const dirOwnership: Ownership = { ...ownership, mode: dirMode };

  let current = targetDir;
  while (current !== basePath && current.startsWith(basePath + "/")) {
    await applyOwnership(current, dirOwnership);
    current = dirname(current);
  }
};
