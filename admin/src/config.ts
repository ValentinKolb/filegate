import { createConfig } from "@valentinkolb/ssr";
import { createSSRHandler, routes } from "@valentinkolb/ssr/hono";

type PageOptions = {
  title?: string;
};

export const { config, plugin, html } = createConfig<PageOptions>({
  dev: process.env.NODE_ENV === "development",
  rootDir: import.meta.dir,
  template: ({ body, scripts, title }) => `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Filegate Admin${title ? ` - ${title}` : ""}</title>
<link rel="stylesheet" href="/styles.css">
</head>
<body>${body}${scripts}</body>
</html>`,
});

export const ssr = createSSRHandler(html);
export { routes };
