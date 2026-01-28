// Test setup - Set required env vars before any modules are imported
// Use /private/tmp on macOS since /tmp is a symlink to /private/tmp
const ismacos = process.platform === "darwin";
const tmpBase = ismacos ? "/private/tmp" : "/tmp";

process.env.FILE_PROXY_TOKEN = "test-token";
process.env.ALLOWED_BASE_PATHS = `${tmpBase}/filegate-test,${tmpBase}/filegate-test-ownership`;
