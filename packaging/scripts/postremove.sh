#!/bin/sh
set -eu

BINDIR="${FILEGATE_BINDIR:-/usr/bin}"
ACTION="${1:-}"

if [ "${ACTION}" != "upgrade" ] && [ "${ACTION}" != "1" ] && [ -L "${BINDIR}/fg" ] && [ "$(readlink "${BINDIR}/fg")" = "filegate" ]; then
  rm -f "${BINDIR}/fg"
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0
