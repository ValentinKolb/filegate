import type { Node } from "@valentinkolb/filegate";
import { FileIcon, FolderIcon } from "./Icons";
import { formatBytes, formatUnix } from "../lib/format";

export function NodeTable(props: { nodes: Node[]; selectedId?: string; emptyTitle: string; emptyText: string; viewTransitionName?: string }) {
  return (
    <div class="table-wrap" style={props.viewTransitionName ? `view-transition-name: ${props.viewTransitionName}` : undefined}>
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Type</th>
            <th class="num">Size</th>
            <th>Modified</th>
          </tr>
        </thead>
        <tbody>
          {props.nodes.map((node) => (
            <tr data-node-kind={node.type} class={props.selectedId === node.id ? "is-selected" : ""} style={nodeTransitionName(node.id)}>
              <td>
                <span class="name-cell">
                  {node.type === "directory" ? <FolderIcon /> : <FileIcon />}
                  {node.type === "directory" ? (
                    <a data-node-open href={`/files?path=${encodeURIComponent(node.path)}`}>
                      {node.name}
                    </a>
                  ) : (
                    <a data-node-select href={`/files?path=${encodeURIComponent(parentPath(node.path))}&id=${encodeURIComponent(node.id)}`}>
                      {node.name}
                    </a>
                  )}
                </span>
              </td>
              <td class="muted">{node.type === "directory" ? "Folder" : "File"}</td>
              <td class="num">{formatBytes(node.size)}</td>
              <td class="muted">{formatUnix(node.mtime)}</td>
            </tr>
          ))}
          {props.nodes.length === 0 && (
            <tr class="empty-row">
              <td colspan="4">
                <div class="empty">
                  <strong>{props.emptyTitle}</strong>
                  <span>{props.emptyText}</span>
                </div>
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function parentPath(path: string): string {
  const clean = path.replace(/^\/+|\/+$/g, "");
  const idx = clean.lastIndexOf("/");
  return idx < 0 ? "" : clean.slice(0, idx);
}

function nodeTransitionName(id: string): string {
  return `view-transition-name: fg-node-${id.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}
