import type { Node, StatsResponse } from "@valentinkolb/filegate";
import { Layout } from "../components/Layout";
import { FileIcon, FolderIcon } from "../components/Icons";
import { NodeTable } from "../components/Table";
import { formatBytes, formatUnix } from "../lib/format";

type Crumb = { name: string; path?: string };
const folderPickerAttrs = { webkitdirectory: "", directory: "" } as Record<string, string>;

export function Files(props: {
  stats: StatsResponse;
  crumbs: Crumb[];
  current?: Node;
  children: Node[];
  selected?: Node;
  error?: string;
  notice?: string;
}) {
  return (
    <Layout
      active="files"
      title="Files"
      description="Browse mounts, manage files, and inspect node metadata."
      mounts={props.stats.mounts.length}
      error={props.error}
      notice={props.notice}
    >
      <section class="files-grid">
        <div class="panel">
          <div class="panel-head filelist-head">
            <nav class="crumbs" aria-label="Folder path">
              {props.crumbs.map((crumb, index) => (
                <>
                  {index > 0 && <span class="crumb-sep">/</span>}
                  <a href={crumb.path ? `/files?path=${encodeURIComponent(crumb.path)}` : "/files"}>{crumb.name}</a>
                </>
              ))}
            </nav>
            <span class="count">
              {props.children.length} item{props.children.length === 1 ? "" : "s"}
            </span>
          </div>
          {props.current?.type === "directory" && (
            <div class="toolbar">
              <form class="tb-group" method="post" action="/files/mkdir">
                <input type="hidden" name="parentPath" value={props.current.path} />
                <input class="input" name="name" placeholder="New folder name" aria-label="New folder name" />
                <button class="btn">Create folder</button>
              </form>
              <form class="tb-group tb-right" data-upload-form>
                <input type="hidden" name="parentPath" value={props.current.path} />
                <input id="admin-file-upload" type="file" multiple data-upload-input hidden />
                <input id="admin-folder-upload" type="file" multiple data-upload-input hidden {...folderPickerAttrs} />
                <button class="btn primary" type="button" data-upload-trigger="file">
                  Upload file
                </button>
                <button class="btn" type="button" data-upload-trigger="folder">
                  Upload folder
                </button>
              </form>
            </div>
          )}
          <NodeTable
            nodes={props.children}
            selectedId={props.selected?.id}
            emptyTitle={props.current ? "This folder is empty" : "No mount roots"}
            emptyText={props.current ? "Create a folder to get started." : "Configure storage base paths to browse files here."}
          />
        </div>
        <aside class="stack">
          {props.selected ? <Detail node={props.selected} /> : <EmptyDetail />}
        </aside>
      </section>
      <UploadPanel />
      <script src="/uploads.js" defer />
    </Layout>
  );
}

function UploadPanel() {
  return (
    <div id="fg-uploads" class="uploads" hidden>
      <div class="uploads-head">
        <span class="uploads-title">Uploading...</span>
        <button type="button" class="uploads-close" aria-label="Close">
          ×
        </button>
      </div>
      <div class="uploads-list" />
      <div class="uploads-stats" aria-label="Upload statistics">
        <div>
          <span>Throughput</span>
          <strong data-upload-rate>—</strong>
        </div>
        <div>
          <span>Remaining</span>
          <strong data-upload-eta>—</strong>
        </div>
        <div>
          <span>Elapsed</span>
          <strong data-upload-elapsed>0s</strong>
        </div>
        <div>
          <span>Transferred</span>
          <strong data-upload-bytes>0 B</strong>
        </div>
      </div>
    </div>
  );
}

function Detail(props: { node: Node }) {
  const node = props.node;
  return (
    <section class="panel detail">
      <div class="panel-head detail-head">
        <span class="name-cell">
          {node.type === "directory" ? <FolderIcon /> : <FileIcon />}
          <h2 title={node.name}>{node.name}</h2>
        </span>
        <span class="tag">{node.type === "directory" ? "Folder" : "File"}</span>
      </div>
      <div class="panel-body">
        <dl class="props">
          <div class="prop">
            <dt>Size</dt>
            <dd>{formatBytes(node.size)}</dd>
          </div>
          <div class="prop">
            <dt>Modified</dt>
            <dd>{formatUnix(node.mtime)}</dd>
          </div>
          {node.type === "file" && (
            <div class="prop">
              <dt>MIME type</dt>
              <dd class="mono">{node.mimeType || "-"}</dd>
            </div>
          )}
          <div class="prop">
            <dt>Owner</dt>
            <dd>
              {node.ownership.uid}:{node.ownership.gid} · mode {node.ownership.mode}
            </dd>
          </div>
          <div class="prop">
            <dt>Path</dt>
            <dd class="mono">{node.path}</dd>
          </div>
          <div class="prop">
            <dt>Node ID</dt>
            <dd class="mono">{node.id}</dd>
          </div>
        </dl>
        <div class="section-title">Rename</div>
        <form method="post" action="/files/rename" class="form-grid">
          <input type="hidden" name="id" value={node.id} />
          <input class="input" name="name" value={node.name} aria-label="New name" />
          <button class="btn">Rename</button>
        </form>
        <div class="section-title">Move or copy</div>
        <form method="post" action="/files/transfer" class="form-stack">
          <input type="hidden" name="id" value={node.id} />
          <div class="field-row">
            <div class="field">
              <label>Operation</label>
              <select class="select" name="op">
                <option>move</option>
                <option>copy</option>
              </select>
            </div>
            <div class="field">
              <label>On conflict</label>
              <select class="select" name="onConflict">
                <option>error</option>
                <option>rename</option>
                <option>overwrite</option>
              </select>
            </div>
          </div>
          <div class="field">
            <label>Target parent path</label>
            <input class="input" name="targetParentPath" placeholder="backups/archive" />
          </div>
          <div class="field">
            <label>Target name</label>
            <input class="input" name="targetName" value={node.name} />
          </div>
          <button class="btn">Apply transfer</button>
        </form>
        <div class="danger-zone">
          <div>
            <strong>Delete resource</strong>
            <span class="muted">This action cannot be undone.</span>
          </div>
          <form method="post" action="/files/delete" data-confirm-delete={node.path}>
            <input type="hidden" name="id" value={node.id} />
            <button class="btn danger">Delete</button>
          </form>
        </div>
      </div>
    </section>
  );
}

function EmptyDetail() {
  return (
    <section class="panel">
      <div class="panel-body empty detail-empty">
        <strong>No resource selected</strong>
        <span>Open a folder, then select a file or folder to inspect its properties and manage it.</span>
      </div>
    </section>
  );
}
