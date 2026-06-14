# Filegate Admin

Standalone SSR admin app for Filegate.

The app keeps the Filegate bearer token server-side and talks to Filegate through
the TypeScript client. The browser only receives an admin session cookie.

## Run locally

```bash
cd admin
bun install
FILEGATE_URL=http://127.0.0.1:18080 FILEGATE_TOKEN=dev-token bun run dev
```

Open `http://127.0.0.1:3000` and sign in with `ADMIN_TOKEN` when set, otherwise
with `FILEGATE_TOKEN`.

Uploads are intentionally not part of this alpha.
