---
title: Admin app
navTitle: Admin app
section: Use Filegate
order: 65
description: Run the standalone Filegate admin app for browser-based operations.
tags: [admin, ui]
---

# Admin app

The admin app is a standalone SSR web app for operators who need browser access to Filegate resources and service state.

Filegate itself serves REST and optional S3 APIs. The admin app runs as a separate process and talks to Filegate through the TypeScript client.

## Runtime model

```txt
browser <-> admin app <-> Filegate REST API
browser <-> Filegate direct upload/download URLs
```

The Filegate bearer token stays on the admin server. Browser uploads and downloads use scoped direct URLs.

## Environment

| Variable | Scope | Required | Meaning |
|---|---:|---:|---|
| `FILEGATE_URL` | Admin server process | Yes | REST API URL reachable from the admin app. |
| `FILEGATE_TOKEN` | Admin server process | Yes | Filegate bearer token kept server-side. |
| `ADMIN_TOKEN` | Browser login | No | Separate admin login token. Defaults to `FILEGATE_TOKEN`. |
| `ADMIN_SESSION_SECRET` | Browser session cookie | No | Session signing secret. Defaults to `FILEGATE_TOKEN`. |
| `PORT` | Admin server process | No | HTTP listen port. Defaults to `3000`. |

## Start the admin app

Run the admin app where it can reach the Filegate REST API. The Filegate bearer token stays in the admin server process.

```sh
cd admin
bun install
FILEGATE_URL=http://127.0.0.1:8080 \
FILEGATE_TOKEN=dev-token \
ADMIN_TOKEN=admin-token \
bun run dev
```

Open `http://127.0.0.1:3000` and sign in with `ADMIN_TOKEN`.

## Admin surfaces

| Page | Scope | Use for |
|---|---:|---|
| Overview | Service | Mount and storage summary. |
| Files | Mount and node | Browse, upload, download, create folders, transfer, rename, edit metadata, delete. |
| Search | Service index | Glob search over indexed paths. |
| System | Service | Metrics, index state, cache pressure, activity log, and index rescan. |

## Browser transfer behavior

| Operation | Data path | Meaning |
|---|---|---|
| Upload | Browser to Filegate | Admin app creates sessions; browser sends file bytes to scoped Filegate URLs. |
| Download | Browser from Filegate | Admin app mints a scoped URL and redirects browser to it. |
| Metadata mutation | Browser to admin to Filegate | Admin app submits REST calls with the server-side bearer token. |
