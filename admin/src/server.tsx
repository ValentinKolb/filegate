import { app, runtimeEnv } from "./app";

const { port } = runtimeEnv();

Bun.serve({
  port,
  fetch: app.fetch,
});

console.log(`filegate-admin listening on :${port}`);
