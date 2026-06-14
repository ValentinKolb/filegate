import type { FileConflictMode, Node, NodeListResponse, StatsResponse } from "@valentinkolb/filegate";
import { Hono } from "hono";
import { login, logout, requireAuth } from "./lib/auth";
import { client, isList, parentPath, resolveDirectory } from "./lib/filegate";
import { env } from "./lib/env";
import { errorMessage, redirectFiles, selectedFiles } from "./lib/format";
import { config, routes, ssr } from "./config";
import { LoginPage } from "./components/Layout";
import { Files } from "./pages/Files";
import { Overview } from "./pages/Overview";
import { Search } from "./pages/Search";
import { System } from "./pages/System";

type Crumb = { name: string; path?: string };

const emptyStats: StatsResponse = {
  generatedAt: 0,
  index: { totalEntities: 0, totalFiles: 0, totalDirs: 0, dbSizeBytes: 0 },
  cache: { pathEntries: 0, pathCapacity: 0, pathUtilRatio: 0 },
  mounts: [],
  disks: [],
};

export const app = new Hono()
  .route("/_ssr", routes(config))
  .get("/styles.css", (c) => {
    c.header("Content-Type", "text/css; charset=utf-8");
    return new Response(Bun.file(new URL("./styles.css", import.meta.url)));
  })
  .get("/health", (c) => c.text("OK"))
  .get(
    "/login",
    ...ssr(async (c) => {
      c.get("page").title = "Sign in";
      const hasError = c.req.query("error") === "invalid";
      return () => <LoginPage error={hasError ? "Invalid admin token" : undefined} />;
    }),
  )
  .post("/login", login)
  .use("*", requireAuth())
  .post("/logout", logout)
  .get(
    "/",
    ...ssr(async (c) => {
      c.get("page").title = "Overview";
      const data = await loadBase();
      return () => <Overview stats={data.stats} roots={data.roots} error={queryError(c.req.query("error"), data.error)} />;
    }),
  )
  .get(
    "/files",
    ...ssr(async (c) => {
      c.get("page").title = "Files";
      const data = await loadFiles(c.req.query("path") || "", c.req.query("id") || "");
      const loadError = "error" in data ? data.error : undefined;
      return () => <Files {...data} error={queryError(c.req.query("error"), loadError)} notice={c.req.query("notice")} />;
    }),
  )
  .post("/files/mkdir", async (c) => {
    const body = await c.req.parseBody();
    const path = field(body, "parentPath");
    try {
      const parent = await resolveDirectory(path);
      await client().nodes.mkdir(parent.id, { path: field(body, "name"), recursive: true, onConflict: "error" });
      return c.redirect(redirectFiles(path), 303);
    } catch (err) {
      return c.redirect(redirectFiles(path, errorMessage(err)), 303);
    }
  })
  .post("/files/delete", async (c) => {
    const body = await c.req.parseBody();
    try {
      const node = await client().nodes.get(field(body, "id"));
      await client().nodes.delete(node.id);
      return c.redirect(redirectFiles(parentPath(node.path)), 303);
    } catch (err) {
      return c.redirect(redirectFiles("", errorMessage(err)), 303);
    }
  })
  .post("/files/rename", async (c) => {
    const body = await c.req.parseBody();
    try {
      const updated = await client().nodes.patch(field(body, "id"), { name: field(body, "name") }, true);
      return c.redirect(selectedFiles(parentPath(updated.path), updated.id), 303);
    } catch (err) {
      return c.redirect(redirectFiles("", errorMessage(err)), 303);
    }
  })
  .post("/files/transfer", async (c) => {
    const body = await c.req.parseBody();
    try {
      const targetParent = await resolveDirectory(field(body, "targetParentPath"));
      const out = await client().transfers.create({
        op: field(body, "op") === "copy" ? "copy" : "move",
        sourceId: field(body, "id"),
        targetParentId: targetParent.id,
        targetName: field(body, "targetName"),
        onConflict: conflictMode(field(body, "onConflict")),
      });
      return c.redirect(selectedFiles(parentPath(out.node.path), out.node.id), 303);
    } catch (err) {
      return c.redirect(redirectFiles("", errorMessage(err)), 303);
    }
  })
  .get(
    "/search",
    ...ssr(async (c) => {
      c.get("page").title = "Search";
      const stats = await loadStats();
      const pattern = c.req.query("pattern") || "";
      const hidden = c.req.query("hidden") === "true";
      const results = pattern
        ? await client().search.glob({ pattern, limit: 100, showHidden: hidden, files: true, directories: true })
        : undefined;
      return () => <Search stats={stats} pattern={pattern} hidden={hidden} results={results} />;
    }),
  )
  .get(
    "/system",
    ...ssr(async (c) => {
      c.get("page").title = "System";
      const stats = await loadStats();
      return () => <System stats={stats} error={c.req.query("error")} notice={c.req.query("notice")} />;
    }),
  )
  .post("/system/rescan", async (c) => {
    try {
      await client().index.rescan();
      return c.redirect("/system?notice=rescan+queued", 303);
    } catch (err) {
      return c.redirect(`/system?error=${encodeURIComponent(errorMessage(err))}`, 303);
    }
  });

async function loadBase(): Promise<{ stats: StatsResponse; roots: Node[]; error?: string }> {
  try {
    const [stats, roots] = await Promise.all([loadStats(), loadRoots()]);
    return { stats, roots };
  } catch (err) {
    return { stats: emptyStats, roots: [], error: errorMessage(err) };
  }
}

async function loadFiles(path: string, selectedId: string) {
  const base = await loadBase();
  if (base.error) return { ...base, crumbs: buildCrumbs(""), children: [] as Node[] };
  try {
    if (!path.trim()) {
      return { stats: base.stats, crumbs: buildCrumbs(""), children: base.roots };
    }

    let current = await getNodeByPath(path);
    let selected: Node | undefined;
    if (current.type === "file") {
      selected = current;
      current = await getNodeByPath(parentPath(current.path));
    }
    if (selectedId) selected = await client().nodes.get(selectedId);
    return {
      stats: base.stats,
      crumbs: buildCrumbs(current.path),
      current,
      selected: selected ?? current,
      children: current.children ?? [],
    };
  } catch (err) {
    return { ...base, crumbs: buildCrumbs(""), children: base.roots, error: errorMessage(err) };
  }
}

async function loadStats(): Promise<StatsResponse> {
  return client().stats.get();
}

async function loadRoots(): Promise<Node[]> {
  const roots = await client().paths.get("");
  return isList(roots) ? roots.items : [];
}

async function getNodeByPath(path: string): Promise<Node> {
  const out: Node | NodeListResponse = await client().paths.get(path, { pageSize: 100 });
  if (isList(out)) throw new Error("path required");
  return out;
}

function buildCrumbs(path: string): Crumb[] {
  const clean = path.trim().replace(/^\/+|\/+$/g, "");
  const crumbs: Crumb[] = [{ name: "Mount roots" }];
  if (!clean) return crumbs;
  let acc = "";
  for (const part of clean.split("/")) {
    acc = acc ? `${acc}/${part}` : part;
    crumbs.push({ name: part, path: acc });
  }
  return crumbs;
}

function field(body: Record<string, string | File>, key: string): string {
  const value = body[key];
  return typeof value === "string" ? value.trim() : "";
}

function conflictMode(value: string): FileConflictMode {
  return value === "rename" || value === "overwrite" ? value : "error";
}

function queryError(query?: string, fallback?: string): string | undefined {
  return query || fallback || undefined;
}

export function runtimeEnv() {
  return env();
}
