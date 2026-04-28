#!/bin/sh
set -eu

if ! getent group filegate >/dev/null 2>&1; then
  groupadd --system filegate >/dev/null 2>&1 || true
fi

if ! id -u filegate >/dev/null 2>&1; then
  NOLOGIN="$(command -v nologin || true)"
  if [ -z "${NOLOGIN}" ]; then
    NOLOGIN="/usr/sbin/nologin"
  fi
  useradd \
    --system \
    --gid filegate \
    --home /var/lib/filegate \
    --create-home \
    --shell "${NOLOGIN}" \
    --comment "Filegate service user" \
    filegate >/dev/null 2>&1 || true
fi

exit 0

