# Filegate

A secure, high-performance file proxy server built with Bun + Hono + Zod.

Features:
- Streaming uploads/downloads
- Directory download as ZIP
- Resumable chunked uploads with SHA-256 verification
- Glob-based file search
- Type-safe client API with config objects
- Browser-compatible utils for chunked uploads
- OpenAPI documentation with Scalar UI
- LLM-friendly markdown docs at `/llms.txt`

## Quick Start

```bash
export FILE_PROXY_TOKEN=your-secret-token
export ALLOWED_BASE_PATHS=/export/homes,/export/groups

bun run src/index.ts
```

## Documentation

- **Scalar UI:** http://localhost:4000/docs
- **OpenAPI JSON:** http://localhost:4000/openapi.json
- **LLM Markdown:** http://localhost:4000/llms.txt

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `FILE_PROXY_TOKEN` | Yes | - | Bearer token for authentication |
| `ALLOWED_BASE_PATHS` | Yes | - | Comma-separated allowed paths |
| `REDIS_URL` | No | localhost:6379 | Redis connection URL (for chunked uploads) |
| `PORT` | No | 4000 | Server port |

**Size Limits:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_UPLOAD_MB` | 500 | Maximum file size for uploads (simple + chunked) |
| `MAX_DOWNLOAD_MB` | 5000 | Maximum file/directory size for downloads |
| `MAX_CHUNK_SIZE_MB` | 50 | Maximum chunk size (server rejects larger chunks) |

**Search:**

| Variable | Default | Description |
|----------|---------|-------------|
| `SEARCH_MAX_RESULTS` | 100 | Max files returned by search |
| `SEARCH_MAX_RECURSIVE_WILDCARDS` | 10 | Max `**` wildcards allowed in glob patterns (prevents DoS) |

**Other:**

| Variable | Default | Description |
|----------|---------|-------------|
| `UPLOAD_EXPIRY_HOURS` | 24 | Chunked upload expiry (resets on each chunk) |
| `DISK_CLEANUP_INTERVAL_HOURS` | 6 | Interval to clean orphaned chunk files |
| `DEV_UID_OVERRIDE` | - | Override file ownership UID (dev mode only) |
| `DEV_GID_OVERRIDE` | - | Override file ownership GID (dev mode only) |

## API Endpoints

All `/files/*` endpoints require `Authorization: Bearer <token>`.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check (public) |
| GET | `/docs` | Scalar API docs (public) |
| GET | `/openapi.json` | OpenAPI spec (public) |
| GET | `/llms.txt` | LLM-friendly docs (public) |
| GET | `/files/info` | File/directory info |
| GET | `/files/content` | Download file or directory (ZIP) |
| PUT | `/files/content` | Upload file |
| POST | `/files/mkdir` | Create directory |
| DELETE | `/files/delete` | Delete file/directory |
| POST | `/files/move` | Move (same basepath) |
| POST | `/files/copy` | Copy (same basepath) |
| GET | `/files/search` | Glob search |
| POST | `/files/upload/start` | Start/resume chunked upload |
| POST | `/files/upload/chunk` | Upload chunk |

## Examples

### Get Directory Info
```bash
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/files/info?path=/export/homes/alice&showHidden=false"
```

### Upload File
```bash
curl -X PUT \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-File-Path: /export/homes/alice" \
  -H "X-File-Name: report.pdf" \
  -H "X-Owner-UID: 1000" \
  -H "X-Owner-GID: 1000" \
  -H "X-File-Mode: 600" \
  --data-binary @report.pdf \
  "http://localhost:4000/files/content"
```

### Search Files
```bash
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/files/search?paths=/export/homes/alice,/export/groups/team&pattern=**/*.pdf"
```

### Chunked Upload

```bash
# 1. Start upload
curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "path": "/export/homes/alice",
    "filename": "large.zip",
    "size": 104857600,
    "checksum": "sha256:abc123...",
    "chunkSize": 10485760
  }' \
  "http://localhost:4000/files/upload/start"

# 2. Upload chunks
curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Upload-Id: <uploadId>" \
  -H "X-Chunk-Index: 0" \
  -H "X-Chunk-Checksum: sha256:def456..." \
  --data-binary @chunk0.bin \
  "http://localhost:4000/files/upload/chunk"

# Repeat for all chunks - auto-completes on last chunk
```

## Client API

Type-safe client for server-side usage. All methods use config objects for better readability.

### Installation

```typescript
import { Filegate } from "@valentinkolb/filegate/client";
import { chunks, formatBytes } from "@valentinkolb/filegate/utils";
```

### Default Instance (via env vars)

Set `FILEGATE_URL` and `FILEGATE_TOKEN`, then import the pre-configured instance:

```typescript
import { filegate } from "@valentinkolb/filegate/client";

const info = await filegate.info({ path: "/export/homes/alice" });
```

### Custom Instance

```typescript
import { Filegate } from "@valentinkolb/filegate/client";

const client = new Filegate({
  url: "http://localhost:4000",
  token: "your-token",
});
```

### Client Methods

```typescript
// Get file/directory info
const info = await client.info({ path: "/export/homes/alice", showHidden: true });

// Download file (returns Response with streaming body)
const file = await client.download({ path: "/export/homes/alice/report.pdf" });
if (file.ok) {
  const text = await file.data.text();
  const buffer = await file.data.arrayBuffer();
  // Or stream directly: file.data.body
}

// Download directory as ZIP
const zip = await client.download({ path: "/export/homes/alice/documents" });

// Upload file (simple)
await client.upload.single({
  path: "/export/homes/alice",
  filename: "report.pdf",
  data: fileData,
  uid: 1000,
  gid: 1000,
  mode: "644",
});

// Upload file (chunked) - for large files
const startResult = await client.upload.chunked.start({
  path: "/export/homes/alice",
  filename: "large.zip",
  size: file.size,
  checksum: "sha256:...",
  chunkSize: 5 * 1024 * 1024,
  uid: 1000,
  gid: 1000,
});

// Send chunks
await client.upload.chunked.send({
  uploadId: startResult.data.uploadId,
  index: 0,
  data: chunkData,
  checksum: "sha256:...",
});

// Create directory
await client.mkdir({ path: "/export/homes/alice/new-folder", mode: "750" });

// Move/copy (within same basepath)
await client.move({ from: "/export/homes/alice/old.txt", to: "/export/homes/alice/new.txt" });
await client.copy({ from: "/export/homes/alice/file.txt", to: "/export/homes/alice/backup.txt" });

// Delete
await client.delete({ path: "/export/homes/alice/trash" });

// Search with glob patterns
const results = await client.glob({
  paths: ["/export/homes/alice", "/export/groups/team"],
  pattern: "**/*.pdf",
  showHidden: false,
  limit: 100,
});
```

## Browser Utils

Browser-compatible utilities for chunked uploads. Framework-agnostic with reactive state management.

### Basic Usage

```typescript
import { chunks, formatBytes } from "@valentinkolb/filegate/utils";

// Prepare upload (calculates checksum and chunk info)
const upload = await chunks.prepare({ file, chunkSize: 5 * 1024 * 1024 });

// Access properties
upload.file          // Original File/Blob
upload.fileSize      // Total size in bytes
upload.chunkSize     // Chunk size in bytes
upload.totalChunks   // Number of chunks
upload.checksum      // "sha256:..." of entire file

// Format bytes for display
formatBytes({ bytes: upload.fileSize });  // "52.43 MB"
```

### State Management

```typescript
// Subscribe to state changes (framework-agnostic)
const unsubscribe = upload.subscribe((state) => {
  console.log(`${state.percent}% - ${state.status}`);
  // state: { uploaded: number, total: number, percent: number, status: "pending" | "uploading" | "completed" | "error" }
});

// Mark chunk as completed
upload.complete({ index: 0 });

// Reset state
upload.reset();

// Unsubscribe when done
unsubscribe();
```

### Chunk Access

```typescript
// Get specific chunk (sync, returns Blob)
const chunk = upload.get({ index: 0 });

// Calculate chunk checksum
const hash = await upload.hash({ data: chunk });

// Iterate over all chunks
for await (const { index, data, total } of upload) {
  console.log(`Chunk ${index + 1}/${total}`);
}
```

### Upload Helpers

```typescript
// Send single chunk with retry
await upload.send({
  index: 0,
  retries: 3,
  fn: async ({ index, data }) => {
    await fetch("/api/upload/chunk", {
      method: "POST",
      headers: { "X-Chunk-Index": String(index) },
      body: data,
    });
  },
});

// Send all chunks (with skip for resume, concurrency, retries)
await upload.sendAll({
  skip: [0, 1],      // Already uploaded chunks (from resume)
  retries: 3,
  concurrency: 3,    // Parallel uploads
  fn: async ({ index, data }) => {
    await fetch("/api/upload/chunk", { ... });
  },
});
```

### Complete Example (Browser)

```typescript
import { chunks } from "@valentinkolb/filegate/utils";

async function uploadFile(file: File, targetPath: string, onProgress?: (state) => void) {
  const upload = await chunks.prepare({ file, chunkSize: 5 * 1024 * 1024 });
  
  if (onProgress) upload.subscribe(onProgress);
  
  // 1. Start/Resume upload
  const { uploadId, uploadedChunks, completed } = await fetch("/api/upload/start", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      path: targetPath,
      filename: file.name,
      size: upload.fileSize,
      checksum: upload.checksum,
      chunkSize: upload.chunkSize,
    }),
  }).then(r => r.json());
  
  if (completed) return; // Already done
  
  // 2. Upload all chunks (skipping already uploaded)
  await upload.sendAll({
    skip: uploadedChunks,
    retries: 3,
    fn: async ({ index, data }) => {
      await fetch("/api/upload/chunk", {
        method: "POST",
        headers: {
          "X-Upload-Id": uploadId,
          "X-Chunk-Index": String(index),
          "X-Chunk-Checksum": await upload.hash({ data }),
        },
        body: data,
      });
    },
  });
}

// Usage with React
function UploadButton() {
  const [progress, setProgress] = useState({ percent: 0, status: "pending" });
  
  const handleUpload = async (file: File) => {
    await uploadFile(file, "/documents", setProgress);
  };
  
  return <div>{progress.percent}% - {progress.status}</div>;
}
```

### Streaming Proxy (Server-Side)

The download response streams directly - perfect for proxying without buffering:

```typescript
// In your proxy server (e.g., with Hono, Express, etc.)
app.get("/api/files/download", async (c) => {
  const path = c.req.query("path");
  
  // Get streaming response from Filegate
  const response = await client.download({ path });
  
  if (!response.ok) {
    return c.json({ error: response.error }, response.status);
  }
  
  // Stream directly to client - no buffering!
  return new Response(response.data.body, {
    headers: {
      "Content-Type": response.data.headers.get("Content-Type") || "application/octet-stream",
      "Content-Disposition": response.data.headers.get("Content-Disposition") || "",
    },
  });
});
```

## Security

- Path validation with symlink protection
- Base path escape prevention
- Same-basepath enforcement for move/copy
- SHA-256 checksum verification
- Configurable upload/download size limits
- Glob pattern limits (max length 500 chars, configurable recursive wildcard limit)
- Security headers (X-Frame-Options, X-Content-Type-Options, etc.)

## Testing

```bash
# Run unit tests
bun run test:unit

# Run integration tests (requires Docker)
bun run test:integration:run

# Run all tests
bun run test:all
```

## Tech Stack

- **Runtime:** Bun
- **Framework:** Hono
- **Validation:** Zod
- **Docs:** hono-openapi + Scalar
