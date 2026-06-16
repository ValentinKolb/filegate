---
title: Configuration
navTitle: Configuration
section: Use Filegate
order: 40
description: Configure Filegate through YAML, environment variables, and config CLI flags.
tags: [configuration, yaml, cli]
---

# Configuration

This page is for operators configuring Filegate service behavior, storage mounts, upload limits, metrics, activity, versioning, and S3 access.

## Resolution order

| Source | Scope | Meaning |
|---|---:|---|
| `--config` | CLI invocation | Explicit YAML config path. |
| `FILEGATE_CONFIG` | Process environment | YAML config path used when `--config` is omitted. |
| Default candidates | Host filesystem | `/etc/filegate/conf.yaml`, `/etc/filegate/conf.yml`, `./conf.yaml`, `./conf.yml`, `/etc/filegate/config.yaml`, `/etc/filegate/config.yml`, `./config.yaml`, `./config.yml`. |
| `FILEGATE_*` variables | Process environment | Runtime overrides derived from config paths, for example `FILEGATE_SERVER_LISTEN`. |
| `fg serve` config flags | CLI invocation | One-shot runtime overrides for supported config fields. |

## Minimal config

```yaml
server:
  listen: ":8080"

auth:
  bearer_token: "dev-token"

storage:
  base_paths:
    - /srv/filegate/data
  index_path: /var/lib/filegate/index
```

## Edit config offline

Use `fg config` for offline edits. Mutating commands require `--config`, write a timestamped backup by default, validate the resulting YAML, and print a restart reminder.

```sh
sudo fg config set --config /etc/filegate/conf.yaml \
  --auth-bearer-token '<strong-token>' \
  --server-public-url 'https://files.example.com'

sudo fg config mount add --config /etc/filegate/conf.yaml /srv/filegate/photos

fg config validate --config /etc/filegate/conf.yaml
```

## Runtime overrides

Use config flags on `fg serve` for process-local overrides:

```sh
fg serve --config ./conf.yaml --server-listen ':9090'
```

Use environment variables in container deployments:

```sh
FILEGATE_AUTH_BEARER_TOKEN=dev-token \
FILEGATE_STORAGE_BASE_PATHS=/data \
FILEGATE_STORAGE_INDEX_PATH=/var/lib/filegate/index \
fg serve
```

Lists passed through environment variables can use comma or semicolon separators.

## Complete reference

See [Config reference](reference/config) for every config key, type, default, scope, and matching CLI flag.
