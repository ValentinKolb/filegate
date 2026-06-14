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

## Uploads

The files page creates upload sessions through the admin server, then uploads
segments directly from the browser to Filegate with scoped direct session tokens.
This keeps large uploads out of the admin server request path while still hiding
the Filegate bearer token from the browser.

When Filegate is on a different browser origin, configure Filegate CORS for the
admin app origin. The default Filegate CORS headers include
`Filegate-Upload-Session` and `X-Segment-Checksum`.
