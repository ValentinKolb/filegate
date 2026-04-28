# CLI Reference

Filegate CLI is intentionally local-ops focused.

## Core Commands

```bash
filegate serve [--config /etc/filegate/conf.yaml]
filegate health [--host <host-or-url>] [--config /etc/filegate/conf.yaml] [--timeout 10s]
filegate status [--host <host-or-url>] [--token <bearer>] [--config /etc/filegate/conf.yaml] [--timeout 10s]
filegate index stats [--config /etc/filegate/conf.yaml]
filegate index rescan [--config /etc/filegate/conf.yaml]
filegate index rescan --new [--skip-backup] [--config /etc/filegate/conf.yaml]
```

## Semantics

- `serve`:
  - Starts the HTTP server and detector.
  - Linux-only.
- `health`:
  - Calls `GET /health`.
  - No token required.
- `status`:
  - Calls `GET /v1/stats`.
  - Requires bearer token.
- `index stats`:
  - Reads local Pebble index and prints entity count.
- `index rescan`:
  - Rebuilds index state from filesystem by rescanning roots.
- `index rescan --new`:
  - Recreates index directory first, then rescans.
  - Expects daemon to be stopped.
  - Creates timestamped backup by default.
- `index rescan --new --skip-backup`:
  - Same as above, but without backup creation.

## Host/Token Resolution

For `health` and `status`:

- `--host` and `--token` are optional.
- If omitted, values are read from config.
- Config load order:
  1. `--config`
  2. `FILEGATE_CONFIG`
  3. default config candidates used by the daemon

Special host handling:

- `server.listen` values like `:8080`, `0.0.0.0:8080`, or `[::]:8080` are normalized to localhost for local CLI checks.

