#!/usr/bin/env bash
set -euo pipefail

# Versioning soak running against a real btrfs filesystem inside a
# privileged Docker container. Same op-mix and assertions as the
# tmpfs soak — the difference is that capture/restore exercise the
# real FICLONE reflink path instead of the copy fallback.
#
# Tunables:
#   FILEGATE_VERSIONING_SOAK_DURATION   (default 5m)
#   FILEGATE_VERSIONING_SOAK_FILE_POOL  (default 16)
#   FILEGATE_VERSIONING_SOAK_TIMEOUT    (default 30m)
#   FILEGATE_BTRFS_IMG_SIZE             (default 2G)
#   FILEGATE_VERSIONING_DOCKER_IMAGE    (default golang:1.25)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

DURATION="${FILEGATE_VERSIONING_SOAK_DURATION:-5m}"
FILE_POOL="${FILEGATE_VERSIONING_SOAK_FILE_POOL:-16}"
TIMEOUT="${FILEGATE_VERSIONING_SOAK_TIMEOUT:-30m}"
IMG_SIZE="${FILEGATE_BTRFS_IMG_SIZE:-2G}"
DOCKER_IMAGE="${FILEGATE_VERSIONING_DOCKER_IMAGE:-golang:1.25}"

docker run --rm --privileged \
  -v "$ROOT_DIR":/workspace \
  -w /workspace \
  -e FILEGATE_VERSIONING_SOAK=1 \
  -e FILEGATE_VERSIONING_SOAK_DURATION="$DURATION" \
  -e FILEGATE_VERSIONING_SOAK_FILE_POOL="$FILE_POOL" \
  "$DOCKER_IMAGE" bash -lc "
set -euo pipefail
apt-get update >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get install -y btrfs-progs util-linux attr e2fsprogs >/dev/null

mkdir -p /var/tmp/filegate-btrfs
truncate -s ${IMG_SIZE} /var/tmp/filegate-btrfs/disk.img
mkfs.btrfs -f /var/tmp/filegate-btrfs/disk.img >/dev/null
mkdir -p /var/tmp/filegate-btrfs/mnt
mount -o loop /var/tmp/filegate-btrfs/disk.img /var/tmp/filegate-btrfs/mnt

# Carve out a per-run subvolume so RemoveAll-style cleanup at test
# end doesn't fight btrfs. The soak test uses
# FILEGATE_VERSIONING_SOAK_BASE_DIR to root storage on the btrfs
# mount; the index stays on tmpfs (where Pebble likes to be).
btrfs subvolume create /var/tmp/filegate-btrfs/mnt/soak >/dev/null
export FILEGATE_VERSIONING_SOAK_BASE_DIR=/var/tmp/filegate-btrfs/mnt/soak

/usr/local/go/bin/go test ./cli -run 'TestVersioningSoak' -count=1 -v -timeout $TIMEOUT

umount /var/tmp/filegate-btrfs/mnt
"
