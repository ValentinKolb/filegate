import { plugin } from "./config";

await Bun.build({
  entrypoints: ["src/server.tsx"],
  outdir: "dist",
  target: "bun",
  plugins: [plugin()],
});

await Bun.write("dist/styles.css", Bun.file("src/styles.css"));
