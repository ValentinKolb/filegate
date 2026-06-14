import type { JSX } from "solid-js";

type LayoutProps = {
  active: "overview" | "files" | "search" | "system";
  title: string;
  description: string;
  mounts: number;
  notice?: string;
  error?: string;
  children: JSX.Element;
};

const nav = [
  ["overview", "Overview", "/"],
  ["files", "Files", "/files"],
  ["search", "Search", "/search"],
  ["system", "System", "/system"],
] as const;

export function Layout(props: LayoutProps) {
  return (
    <>
      <header class="topbar">
        <div class="service">Filegate Admin</div>
        <div class="top-meta">
          <span class="status">
            <span class="dot" />
            Healthy
          </span>
          <span>
            {props.mounts} mount{props.mounts === 1 ? "" : "s"}
          </span>
          <form method="post" action="/logout">
            <button class="btn" type="submit">
              Log out
            </button>
          </form>
        </div>
      </header>
      <div class="shell">
        <aside class="sidebar">
          <div class="side-title">Resources</div>
          <nav class="nav">
            {nav.map(([key, label, href]) => (
              <a class={props.active === key ? "active" : ""} href={href}>
                {label}
              </a>
            ))}
          </nav>
        </aside>
        <main class="main">
          <nav class="breadcrumbs">
            <span>Filegate</span>
            <span>/</span>
            <strong>{props.title}</strong>
          </nav>
          {props.notice && <div class="notice">{props.notice}</div>}
          {props.error && <div class="error">{props.error}</div>}
          <section class="head">
            <div>
              <h1>{props.title}</h1>
              <div class="desc">{props.description}</div>
            </div>
            {props.active === "system" && (
              <form method="post" action="/system/rescan">
                <button class="btn primary">Rescan index</button>
              </form>
            )}
          </section>
          {props.children}
        </main>
      </div>
    </>
  );
}

export function LoginPage(props: { error?: string }) {
  return (
    <main class="login">
      <div class="panel">
        <div class="panel-head">
          <h2>Filegate Admin</h2>
        </div>
        <div class="panel-body">
          {props.error && <div class="error">{props.error}</div>}
          <form method="post" action="/login" class="form-stack">
            <div class="field">
              <label for="admin-token">Admin token</label>
              <input
                id="admin-token"
                class="input"
                name="token"
                type="password"
                autocomplete="current-password"
                autofocus
              />
            </div>
            <button class="btn primary" type="submit">
              Sign in
            </button>
          </form>
        </div>
      </div>
    </main>
  );
}
