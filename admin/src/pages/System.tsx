import type { ActivityEvent, ActivityListResponse, StatsResponse } from "@valentinkolb/filegate";
import { Layout } from "../components/Layout";
import { env } from "../lib/env";
import { formatBytes, formatUnix } from "../lib/format";

type ActivityQuery = { q: string; operation: string; outcome: string; page: number; pageSize: number };

export function System(props: {
  stats: StatsResponse;
  activity?: ActivityListResponse;
  activityQuery: ActivityQuery;
  error?: string;
  notice?: string;
}) {
  const cfg = env();
  const totalPages = Math.max(1, Math.ceil((props.activity?.total ?? 0) / props.activityQuery.pageSize));
  const page = Math.min(props.activityQuery.page, totalPages);
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
      <section id="activity" class="panel activity-panel">
        <div class="panel-head">
          <div>
            <h2>Activity</h2>
            <p>{activitySummary(props.activity, props.activityQuery)}</p>
          </div>
          <a class="btn" href={activityURL(props.activityQuery, page)}>
            Reload activity
          </a>
        </div>
        <form class="toolbar activity-filters" method="get" action="/system#activity">
          <input class="input" name="q" value={props.activityQuery.q} placeholder="Search activity" aria-label="Search activity" />
          <select class="select" name="operation" aria-label="Filter by operation">
            <option value="">All operations</option>
            {(props.activity?.operations ?? []).map((operation) => (
              <option value={operation} selected={props.activityQuery.operation === operation}>
                {operation}
              </option>
            ))}
          </select>
          <select class="select" name="outcome" aria-label="Filter by outcome">
            <option value="">All outcomes</option>
            {["succeeded", "failed", "skipped"].map((outcome) => (
              <option value={outcome} selected={props.activityQuery.outcome === outcome}>
                {outcome}
              </option>
            ))}
          </select>
          <button class="btn primary" type="submit">
            Apply
          </button>
          <a class="btn" href="/system#activity">
            Reset
          </a>
        </form>
        <div class="table-wrap">
          <table class="activity-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>Operation</th>
                <th>Actor</th>
                <th>Target</th>
                <th>Duration</th>
                <th>Outcome</th>
              </tr>
            </thead>
            <tbody>
              {(props.activity?.items.length ?? 0) === 0 ? (
                <tr class="empty-row">
                  <td colSpan={6}>No activity recorded yet.</td>
                </tr>
              ) : (
                props.activity!.items.map((event) => (
                  <tr>
                    <td class="mono">{formatUnix(event.at)}</td>
                    <td class="activity-op">{event.operation}</td>
                    <td>{actorName(event)}</td>
                    <td class="mono">{targetName(event)}</td>
                    <td>{formatDuration(event.durationMs)}</td>
                    <td>
                      <span class={`outcome outcome-${event.outcome}`}>{event.outcome}</span>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        <div class="activity-pager">
          <a class={`btn${page <= 1 ? " disabled" : ""}`} href={page <= 1 ? activityURL(props.activityQuery, 1) : activityURL(props.activityQuery, page - 1)}>
            Previous
          </a>
          <span>
            Page {page} of {totalPages}
          </span>
          <a
            class={`btn${page >= totalPages ? " disabled" : ""}`}
            href={page >= totalPages ? activityURL(props.activityQuery, totalPages) : activityURL(props.activityQuery, page + 1)}
          >
            Next
          </a>
        </div>
      </section>
    </Layout>
  );
}

function actorName(event: ActivityEvent): string {
  return event.actor.delegatedActor || event.actor.label || event.actor.id || event.actor.kind;
}

function targetName(event: ActivityEvent): string {
  return event.target?.path || event.target?.id || event.target?.kind || "-";
}

function formatDuration(value?: number): string {
  if (!value) return "-";
  return value < 1000 ? `${value} ms` : `${(value / 1000).toFixed(1)} s`;
}

function activitySummary(activity: ActivityListResponse | undefined, query: ActivityQuery): string {
  if (!activity) return "Recent activity is unavailable.";
  const start = activity.total === 0 ? 0 : activity.offset + 1;
  const end = Math.min(activity.offset + activity.items.length, activity.total);
  const filtered = query.q || query.operation || query.outcome ? `${activity.total} matching · ` : "";
  return `${filtered}${start}-${end} shown · ${activity.retained} / ${activity.capacity} retained`;
}

function activityURL(query: ActivityQuery, page = query.page): string {
  const q = new URLSearchParams();
  if (query.q) q.set("q", query.q);
  if (query.operation) q.set("operation", query.operation);
  if (query.outcome) q.set("outcome", query.outcome);
  if (page > 1) q.set("page", String(page));
  const suffix = q.toString();
  return `/system${suffix ? `?${suffix}` : ""}#activity`;
}
