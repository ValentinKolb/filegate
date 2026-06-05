# S3 Listener Configuration

The S3-compatible HTTP listener is **off by default**. This page covers turning it on, the auth model, the mount→bucket mapping, and the TLS-termination requirement.

## Minimal config

```yaml
s3:
  enabled: true
  listen: ":9100"
  region: "us-east-1"
  access_key: "AKIAIOSFODNN7EXAMPLE"
  secret_key: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
```

This single-tenant form grants the configured key access to **every** mount. It's convenient for a personal deployment; multi-user setups should use the multi-tenant `keys` list below.

When `s3.enabled=true`, filegate validates every mount name as an S3 bucket name on startup. Invalid mount names — not lowercase, > 63 chars, IP-format, AWS-reserved prefix/suffix — fail startup loudly so you catch the misconfiguration before clients hit it.

The S3 listener binds on its own port (`s3.listen`), separate from the REST listener (`server.listen`). Bind it to an internal interface in production so only your reverse proxy can reach it.

---

## Multi-tenant key store

For deployments with multiple users / clients, replace the single-tenant fields with a `keys` list:

```yaml
s3:
  enabled: true
  listen: ":9100"
  region: "us-east-1"
  keys:
    - access_key: "AKIAALICEKEY00000001"
      secret_key: "alice-secret-keep-this-private-please"
      buckets: ["alice-photos", "alice-docs"]

    - access_key: "AKIABOBKEY0000000001"
      secret_key: "bob-secret-keep-this-private-please"
      buckets: ["bob-archive"]

    - access_key: "AKIABACKUPKEY0000001"
      secret_key: "backup-tool-secret-restic-or-similar"
      buckets: ["backups"]

    - access_key: "AKIAOPSKEY00000000001"
      secret_key: "ops-admin-secret-private-please"
      buckets: ["*"]    # wildcard — sees every configured mount
```

### Whitelist semantics

- `buckets: ["a", "b"]` — the key may operate on mounts `a` and `b`. ListBuckets returns exactly those two. Any access to other mounts returns `403 AccessDenied` (the bucket's existence is **not** revealed).
- `buckets: ["*"]` — the key may operate on every configured mount. ListBuckets returns the full mount list.
- `buckets: []` (empty list) — the key authenticates but every bucket-scoped op returns 403. Useful for staging revocation without removing the entry.

### Validation at startup

Filegate refuses to start when:

- `s3.enabled=true` but no credentials (neither legacy `access_key`/`secret_key` nor any `keys` entry) are configured.
- A `keys` entry has an empty `access_key` or `secret_key`.
- Two `keys` entries share the same `access_key` (paste-twice typo — silent override would be a security hazard).
- A `keys` entry's `buckets` list references a mount name that doesn't exist (catches typos like `"buckte"` instead of `"bucket"`).

These errors are loud and fail-fast — operators catch the misconfiguration up-front instead of debugging mysterious 403s later.

### Combining legacy + multi-tenant

The legacy single-tenant `access_key`/`secret_key` and the multi-tenant `keys` list **coexist**. Both are folded into one in-memory key store at startup. The legacy key is treated as a `"*"` wildcard entry. A duplicate access key between the legacy fields and a `keys` entry is rejected at startup.

This makes the migration path painless: keep the legacy fields, add `keys` entries for new tenants, then drop the legacy fields once the original key has been retired.

### Rotating keys

Filegate loads the key store **once at startup**. Config changes are offline edits: update the YAML, then restart the daemon with your service manager.

Generate a new credential pair:

```bash
filegate config s3 key generate
```

Add the new key to the YAML:

```bash
filegate config s3 key add --config /etc/filegate/conf.yaml \
  --bucket alice-photos \
  --access-key FGALICENEW \
  --secret-key '<new-secret>'
```

Omit `--access-key` and/or `--secret-key` to let the CLI generate the missing values. Use `--all-buckets` instead of `--bucket` for an admin key.

After clients have switched, stage revocation by disabling the old key:

```bash
filegate config s3 key disable --config /etc/filegate/conf.yaml FGALICEOLD
```

The disabled key still authenticates but has an empty bucket list, so every bucket operation returns `403 AccessDenied`. Remove it after the cutover window:

```bash
filegate config s3 key remove --config /etc/filegate/conf.yaml FGALICEOLD
```

Every mutating `filegate config` command validates the resulting YAML, creates a timestamped backup by default, and prints a restart reminder. It does not hot-reload a running filegate process.

---

## Mount → bucket mapping

Each entry in `storage.base_paths` becomes one bucket; the bucket name is the basename of the path:

```yaml
storage:
  base_paths:
    - /var/lib/filegate/photos     # → bucket "photos"
    - /var/lib/filegate/backups    # → bucket "backups"
    - /var/lib/filegate/archive    # → bucket "archive"
```

The path leaf is the bucket name. To rename a bucket, rename the directory and restart filegate (the index will rebuild references; existing object IDs are preserved).

### Bucket-name rules (S3 spec)

When `s3.enabled=true`, every mount basename must:

- Be 3-63 characters.
- Use only lowercase letters, digits, and hyphens.
- Not start or end with a hyphen.
- Not contain consecutive dots.
- Not match an IP-address shape (`192.168.1.1`).
- Not start with `xn-`, `sthree-`, or `amzn-s3-demo-` (AWS reservations).
- Not end with `-s3alias`, `--ol-s3`, `--x-s3`, or `--table-s3`.
- Not be `.fg-versions` or `.fg-uploads` (filegate-reserved internal namespaces).

Filegate fails startup with a clear error when any mount fails this check.

---

## TLS termination

The S3 listener speaks **plain HTTP**. Production deployments must put a reverse proxy (Traefik, Caddy, nginx) in front for TLS termination.

This is intentional: SigV4 already authenticates and integrity-protects the request body, so plain-HTTP between a trusted reverse proxy and filegate is safe **inside the same trusted network**. Adding TLS inside the daemon would duplicate work the proxy is already doing better.

### Traefik example

```yaml
# traefik labels on the filegate service
- "traefik.enable=true"
- "traefik.http.routers.filegate-s3.rule=Host(`s3.example.com`)"
- "traefik.http.routers.filegate-s3.entrypoints=websecure"
- "traefik.http.routers.filegate-s3.tls.certresolver=letsencrypt"
- "traefik.http.services.filegate-s3.loadbalancer.server.port=9100"
```

### Caddy example

```caddy
s3.example.com {
  reverse_proxy filegate:9100
}
```

### Why path-style hosts work

S3 path-style addressing means the bucket name is in the URL path (`https://s3.example.com/{bucket}/{key}`), not the hostname. A single hostname routes all bucket traffic — no per-bucket DNS records, no wildcard certs.

---

## Region

Set `s3.region` to whatever string you want clients to sign with. Filegate doesn't care about the value; it only matters that the client's signature scope matches.

```yaml
s3:
  region: "us-east-1"     # default — works with everything
```

Operators with one filegate instance can keep `us-east-1`. Operators running multiple filegate instances behind one client may set distinct region names so misrouted requests fail fast at signature verification.

---

## Access logs

The same `server.access_log_enabled` flag controls both the REST and S3 access logs. Per-request lines look like:

```
[filegate-s3] PutObject bucket=alice-photos key=2024/cat.jpg by=AKIAALICEKEY00000001 create
[filegate-s3] CompleteMultipartUpload bucket=backups key=archive.tar by=AKIABACKUPKEY0000001 uploadId=… parts=42 etag=… replayed=false
```

The `by=` field is the verified access key. No secrets ever appear in the log.

---

## Per-mount internal layout

The S3 listener uses two internal namespaces under each mount root:

- `<mount>/.fg-versions/<file-id>/<version-id>.bin` — the REST versioning feature's per-file blobs (only created on btrfs mounts and when versioning is enabled).
- `<mount>/.fg-uploads/s3-<uploadId>/` — multipart upload staging (`manifest.json`, `parts/00001.bin`, …, and ephemeral `complete.tmp`).

Both are filegate-private. Object keys can't reach them: the validator rejects any key whose first segment is `.fg-versions` or `.fg-uploads`.

---

## See also

- [s3-api.md](./s3-api.md) — supported operations + deviations.
- [s3-clients.md](./s3-clients.md) — config snippets for clients.
