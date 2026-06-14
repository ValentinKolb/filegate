export function formatBytes(value: number): string {
  if (!value) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let n = value;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return i === 0 ? `${value} B` : `${n.toFixed(1)} ${units[i]}`;
}

export function formatUnix(value: number): string {
  if (!value) return "-";
  const millis = value > 100_000_000_000 ? value : value * 1000;
  return new Date(millis).toISOString().slice(0, 16).replace("T", " ");
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return "Operation failed";
}

export function redirectFiles(path: string, error?: string): string {
  const q = new URLSearchParams();
  if (path.trim()) q.set("path", path);
  if (error) q.set("error", error);
  const suffix = q.toString();
  return `/files${suffix ? `?${suffix}` : ""}`;
}

export function selectedFiles(path: string, id: string, error?: string): string {
  const q = new URLSearchParams({ path, id });
  if (error) q.set("error", error);
  return `/files?${q}`;
}
