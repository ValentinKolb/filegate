import type { StatsResponse } from "@valentinkolb/filegate";
import { Layout } from "../components/Layout";
import { env } from "../lib/env";
import { formatBytes, formatUnix } from "../lib/format";

export function System(props: { stats: StatsResponse; error?: string; notice?: string }) {
  const cfg = env();
  const sections = [
    {
      title: "Admin app",
      rows: [
        ["filegate_url", cfg.filegateUrl],
        ["admin_token", "configured; secret redacted"],
        ["session_secret", "configured; secret redacted"],
      ],
    },
    {
      title: "Filegate index",
      rows: [
        ["generated_at", formatUnix(props.stats.generatedAt)],
        ["db_size", formatBytes(props.stats.index.dbSizeBytes)],
        ["entities", String(props.stats.index.totalEntities)],
      ],
    },
    {
      title: "Cache",
      rows: [
        ["path_entries", String(props.stats.cache.pathEntries)],
        ["path_capacity", String(props.stats.cache.pathCapacity)],
        ["path_usage", `${Math.round(props.stats.cache.pathUtilRatio * 100)}%`],
      ],
    },
  ];
  return (
    <Layout
      active="system"
      title="System"
      description="Read-only admin app configuration and Filegate runtime state."
      mounts={props.stats.mounts.length}
      error={props.error}
      notice={props.notice}
    >
      <section class="grid">
        <div class="config-grid">
          {sections.map((section) => (
            <section class="panel">
              <div class="panel-head">
                <h2>{section.title}</h2>
              </div>
              <div class="panel-body">
                <dl class="cfg">
                  {section.rows.map(([key, value]) => (
                    <div class="cfg-row">
                      <dt>{key}</dt>
                      <dd class="mono">{value || "-"}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            </section>
          ))}
        </div>
        <aside class="panel">
          <div class="panel-head">
            <h2>Mounts</h2>
          </div>
          <div class="panel-body">
            <dl class="cfg">
              {props.stats.mounts.map((mount) => (
                <div class="cfg-row">
                  <dt>{mount.name}</dt>
                  <dd>
                    {mount.files} files · {mount.dirs} dirs
                  </dd>
                </div>
              ))}
            </dl>
          </div>
        </aside>
      </section>
    </Layout>
  );
}
