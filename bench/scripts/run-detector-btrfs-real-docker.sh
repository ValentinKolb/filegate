#!/usr/bin/env bash
set -euo pipefail

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
DEBIAN_FRONTEND=noninteractive apt-get install -y btrfs-progs util-linux attr >/dev/null
mkdir -p /var/tmp/filegate-btrfs
truncate -s ${BTRFS_IMG_SIZE} /var/tmp/filegate-btrfs/disk.img
mkfs.btrfs -f /var/tmp/filegate-btrfs/disk.img >/dev/null
mkdir -p /var/tmp/filegate-btrfs/mnt
mount -o loop /var/tmp/filegate-btrfs/disk.img /var/tmp/filegate-btrfs/mnt
btrfs filesystem show /var/tmp/filegate-btrfs/mnt
export FILEGATE_BTRFS_REAL=1
export FILEGATE_BTRFS_REAL_ROOT=/var/tmp/filegate-btrfs/mnt
/usr/local/go/bin/go test ./cli -run 'TestConsumeDetectorEventsWithRealBTRFS|TestBTRFSReal' -count=1 -v
umount /var/tmp/filegate-btrfs/mnt
"
