#!/usr/bin/env bash
set -euo pipefail

# Run the versioning real-btrfs tests inside a privileged Docker
# container with a loopback btrfs image. Mirrors
# run-detector-btrfs-real-docker.sh but targets the versioning subsystem
# (FICLONE reflink success path + end-to-end capture/restore/prune
# flows on a real btrfs mount).
#
# Required: Docker with --privileged support. The host kernel must
# support loopback btrfs mounts (true on every modern Linux distro).

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

DOCKER_IMAGE="${FILEGATE_BTRFS_DOCKER_IMAGE:-golang:1.25}"
BTRFS_IMG_SIZE="${FILEGATE_BTRFS_IMG_SIZE:-1G}"

docker run --rm --privileged \
  -v "$ROOT_DIR":/workspace \
  -w /workspace \
  "$DOCKER_IMAGE" bash -lc "
set -euo pipefail
apt-get update >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get install -y btrfs-progs util-linux attr e2fsprogs >/dev/null
mkdir -p /var/tmp/filegate-btrfs
truncate -s ${BTRFS_IMG_SIZE} /var/tmp/filegate-btrfs/disk.img
mkfs.btrfs -f /var/tmp/filegate-btrfs/disk.img >/dev/null
mkdir -p /var/tmp/filegate-btrfs/mnt
mount -o loop /var/tmp/filegate-btrfs/disk.img /var/tmp/filegate-btrfs/mnt
btrfs filesystem show /var/tmp/filegate-btrfs/mnt
export FILEGATE_BTRFS_REAL=1
export FILEGATE_BTRFS_REAL_ROOT=/var/tmp/filegate-btrfs/mnt

# Two distinct test packages:
#   * infra/filesystem: direct FICLONE ioctl success-path test.
#   * domain (external _test package): end-to-end versioning lifecycle
#     over the real reflink machinery — capture, list, restore in-place
#     and as-new, snapshot+pin lifecycle, pruner+orphan flow,
#     concurrent snapshot serialization.
/usr/local/go/bin/go test ./infra/filesystem -run 'TestCloneFileReflinkSucceedsOnBTRFS' -count=1 -v
/usr/local/go/bin/go test ./domain -run 'TestVersioning.*OverRealBTRFS' -count=1 -v

umount /var/tmp/filegate-btrfs/mnt
"
