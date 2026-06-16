---
title: Security model
navTitle: Security model
section: Operate
order: 130
description: Understand Filegate authentication, direct URL tokens, CORS, trusted proxies, and secret handling.
tags: [security, auth, cors]
---

# Security model

This page is for operators and developers who need to understand Filegate authentication boundaries and browser-safe transfer patterns.

## Authentication surfaces

| Surface | Scope | Credential |
|---|---:|---|
| REST API | `/v1/*` routes | Bearer token from `auth.bearer_token`. |
| Health | `/health` | No credential. |
| Metrics | Configured metrics path | `metrics.token`, REST bearer token fallback, or open in S3-only no-token deployments. |
| S3 API | S3 listener | SigV4 access key and secret key. |
| Direct upload URL | One upload target | Scoped URL token. |
| Direct download URL | One resolved node | Scoped URL token. |
| Direct upload session | One upload session | Scoped session token. |

## Actor logging

Filegate logs the authenticated credential kind as the actor. `X-Filegate-Actor` can add a delegated actor label for applications that want user-level attribution.

| Field | Scope | Meaning |
|---|---:|---|
| `actor.kind` | Activity event | `bearer_token`, `s3_key`, `signed_url`, or `system`. |
| `actor.id` | Activity event | Stable identifier for the authenticated credential. |
| `actor.label` | Activity event | Optional credential label. |
| `actor.delegatedActor` | Activity event | Optional label from `X-Filegate-Actor`. |

`X-Filegate-Actor` is not authorization. Treat it as log metadata supplied by a trusted application server.

## Browser uploads

Do not expose the Filegate bearer token to browsers. Use an application server to create direct upload sessions or direct one-shot URLs.

```txt
browser -> app server: request upload permission
app server -> Filegate: create scoped direct URL or session
browser -> Filegate: upload bytes with scoped token
```

## CORS

CORS is disabled when `server.cors.allowed_origins` is empty. Prefer configuring CORS at the reverse proxy. If Filegate must answer browser CORS directly, configure explicit origins.

```yaml
server:
  cors:
    allowed_origins:
      - "https://app.example.com"
    exposed_headers:
      - "X-Node-Id"
      - "X-Created-Id"
```

Wildcard origin with `allow_credentials: true` is rejected.

## Trusted proxies

`server.trusted_proxies` controls whether `X-Forwarded-For` and `X-Real-Ip` are honored for logged client addresses.

| Setting | Scope | Meaning |
|---|---:|---|
| Empty list | Service | Ignore forwarded client IP headers. Default. |
| IP or CIDR entry | Proxy peer | Honor forwarded headers only from matching peers. |

Behind Traefik, Caddy, or nginx, list the proxy address or CIDR. Do not trust forwarded headers from direct clients.

## Secret handling

| Secret | Storage scope | Handling |
|---|---:|---|
| REST bearer token | Config or environment | Keep server-side. |
| S3 secret keys | Config or environment | Store as secrets; key creation prints generated secrets once. |
| Metrics token | Config or environment | Use for scraper-only access. |
| Admin app session secret | Admin process | Keep server-side. |
