#!/bin/sh
set -eu

mkdir -p /etc/filegate /var/lib/filegate /var/lib/filegate/data /var/log/filegate
chmod 0750 /var/lib/filegate /var/lib/filegate/data /var/log/filegate

if id -u filegate >/dev/null 2>&1; then
  chown -R filegate:filegate /var/lib/filegate /var/log/filegate
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0
