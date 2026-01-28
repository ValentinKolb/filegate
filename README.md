# Filegate

A secure file proxy server for building custom file management systems. Bring your own backend and frontend - Filegate handles the file operations.

```
Browser/App          Your Backend            Filegate            Filesystem
     |                    |                     |                    |
     |  upload request    |                     |                    |
     |------------------->|                     |                    |
     |                    |  proxy to filegate  |                    |
     |                    |-------------------->|                    |
     |                    |                     |  write file        |
     |                    |                     |------------------->|
     |                    |                     |                    |
     |                    |<--------------------|<-------------------|
     |<-------------------|                     |                    |
```

Filegate is designed to work behind your backend, not as a public-facing service. Your backend handles authentication and authorization, then proxies requests to Filegate. This gives you full control over access logic while Filegate handles the complexity of streaming, chunked uploads, permissions, and security.

## Features

- Streaming uploads and downloads (no memory buffering)
- Resumable chunked uploads with SHA-256 verification
- Directory downloads as TAR archives
- Unix file permissions (uid, gid, mode)
- Glob-based file search
- Type-safe client with full TypeScript support
- OpenAPI documentation

## Quick Start

### 1. Start Filegate with Docker

```bash
docker run -d \
  --name filegate \
  -p 4000:4000 \
  -e FILE_PROXY_TOKEN=your-secret-token \
  -e ALLOWED_BASE_PATHS=/data \
  -v /path/to/your/files:/data \
  ghcr.io/valentinkolb/filegate:latest
```

### 2. Install the Client

```bash
npm install @valentinkolb/filegate
```

### 3. Configure Environment

```bash
export FILEGATE_URL=http://localhost:4000
export FILEGATE_TOKEN=your-secret-token
```

### 4. Upload a File

```typescript
import { filegate } from "@valentinkolb/filegate/client";

const result = await filegate.upload.single({
  path: "/data/uploads",
  filename: "document.pdf",
  data: fileBuffer,
});

if (result.ok) {
  console.log("Uploaded:", result.data.path);
}
```

### 5. Download a File

```typescript
import { filegate } from "@valentinkolb/filegate/client";

const result = await filegate.download({ path: "/data/uploads/document.pdf" });

if (result.ok) {
  const blob = await result.data.blob();
}
```

## Architecture

Filegate follows a proxy architecture where your backend mediates all file operations:

```
+------------------+       +------------------+       +------------------+
|                  |       |                  |       |                  |
|   Your Client    |<----->|   Your Backend   |<----->|    Filegate      |
|   (Browser/App)  |       |   (Auth, Logic)  |       |   (File Ops)     |
|                  |       |                  |       |                  |
+------------------+       +------------------+       +------------------+
                                                              |
                                                              v
                                                      +------------------+
                                                      |                  |
                                                      |   Filesystem     |
                                                      |                  |
                                                      +------------------+
```

**Your Client** handles the UI and user interactions. It communicates with your backend.

**Your Backend** handles authentication, authorization, and business logic. It decides who can access what and proxies requests to Filegate.

**Filegate** handles the actual file operations: reading, writing, streaming, chunked uploads, permissions. It only accepts requests with a valid token.

This separation means you have full control over access patterns while Filegate handles the complexity of file operations.

## Core Concepts

### Base Paths

Filegate restricts all file operations to explicitly allowed directories called "base paths". This is a security boundary - files outside these paths cannot be accessed.

```bash
ALLOWED_BASE_PATHS=/data/uploads,/data/exports
```

With this configuration:
- `/data/uploads/file.txt` - allowed
- `/data/exports/report.pdf` - allowed
- `/home/user/file.txt` - forbidden
- `/data/../etc/passwd` - forbidden (path traversal blocked)

Symlinks that point outside base paths are also blocked.

### File Ownership

Filegate runs as root to set file ownership. When uploading files, you can specify Unix permissions:

```typescript
await client.upload.single({
  path: "/data/uploads",
  filename: "file.txt",
  data: buffer,
  uid: 1000,    // Owner user ID
  gid: 1000,    // Owner group ID
  mode: "644",  // Unix permissions (rw-r--r--)
});
```

This is essential when Filegate manages files for multiple users on a shared filesystem.

### Chunked Uploads

For large files, use chunked uploads. They support:
- Resume after connection failure
- Progress tracking
- Per-chunk checksum verification
- Automatic assembly when complete

```typescript
// Start or resume upload
const start = await client.upload.chunked.start({
  path: "/data/uploads",
  filename: "large-file.zip",
  size: file.size,
  checksum: "sha256:abc123...",  // Checksum of entire file
  chunkSize: 5 * 1024 * 1024,    // 5MB chunks
});

// Upload each chunk
for (let i = 0; i < start.data.totalChunks; i++) {
  if (start.data.uploadedChunks.includes(i)) continue; // Skip already uploaded
  
  await client.upload.chunked.send({
    uploadId: start.data.uploadId,
    index: i,
    data: chunkData,
  });
}
```

Uploads automatically expire after 24 hours of inactivity.

## Configuration

All configuration is done through environment variables.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `FILE_PROXY_TOKEN` | Yes | - | Bearer token for API authentication |
| `ALLOWED_BASE_PATHS` | Yes | - | Comma-separated list of allowed directories |
| `PORT` | No | 4000 | Server port |
| `MAX_UPLOAD_MB` | No | 500 | Maximum upload size in MB |
| `MAX_DOWNLOAD_MB` | No | 5000 | Maximum download size in MB |
| `MAX_CHUNK_SIZE_MB` | No | 50 | Maximum chunk size in MB |
| `SEARCH_MAX_RESULTS` | No | 100 | Maximum search results returned |
| `SEARCH_MAX_RECURSIVE_WILDCARDS` | No | 10 | Maximum `**` wildcards in glob patterns |
| `UPLOAD_EXPIRY_HOURS` | No | 24 | Hours until incomplete uploads expire |
| `UPLOAD_TEMP_DIR` | No | /tmp/filegate-uploads | Directory for temporary chunk storage |
| `DISK_CLEANUP_INTERVAL_HOURS` | No | 6 | Interval for cleaning orphaned chunks |

### Development Mode

For local development without root permissions, you can override file ownership:

```bash
DEV_UID_OVERRIDE=1000
DEV_GID_OVERRIDE=1000
```

This applies the specified uid/gid instead of the requested values. Do not use in production.

## Docker Compose Example

```yaml
services:
  filegate:
    image: ghcr.io/valentinkolb/filegate:latest
    ports:
      - "4000:4000"
    environment:
      FILE_PROXY_TOKEN: ${FILE_PROXY_TOKEN}
      ALLOWED_BASE_PATHS: /data
    volumes:
      - ./data:/data
      - filegate-chunks:/tmp/filegate-uploads

volumes:
  filegate-chunks:
```

## Client API

The client provides a type-safe interface for all Filegate operations.

### Installation

```bash
npm install @valentinkolb/filegate
```

### Default Instance

Set `FILEGATE_URL` and `FILEGATE_TOKEN` environment variables, then import the pre-configured client:

```typescript
import { filegate } from "@valentinkolb/filegate/client";

await filegate.info({ path: "/data/uploads" });
```

### Custom Instance

For more control or multiple Filegate servers, create instances manually:

```typescript
import { Filegate } from "@valentinkolb/filegate/client";

const client = new Filegate({
  url: "http://localhost:4000",
  token: "your-secret-token",
});
```

### Methods

```typescript
// Get file or directory info
await client.info({ path: "/data/file.txt", showHidden: false });

// Download file (returns streaming Response)
await client.download({ path: "/data/file.txt" });

// Download directory as TAR archive
await client.download({ path: "/data/folder" });

// Simple upload
await client.upload.single({
  path: "/data/uploads",
  filename: "file.txt",
  data: buffer,
  uid: 1000,
  gid: 1000,
  mode: "644",
});

// Chunked upload
await client.upload.chunked.start({ ... });
await client.upload.chunked.send({ ... });

// Create directory
await client.mkdir({ path: "/data/new-folder", mode: "755" });

// Delete file or directory
await client.delete({ path: "/data/old-file.txt" });

// Move (within same base path)
await client.move({ from: "/data/old.txt", to: "/data/new.txt" });

// Copy (within same base path)
await client.copy({ from: "/data/file.txt", to: "/data/backup.txt" });

// Search with glob patterns
await client.glob({
  paths: ["/data/uploads"],
  pattern: "**/*.pdf",
  limit: 50,
});
```

### Response Format

All methods return a discriminated union:

```typescript
type Response<T> = 
  | { ok: true; data: T }
  | { ok: false; error: string; status: number };

const result = await client.info({ path: "/data/file.txt" });

if (result.ok) {
  console.log(result.data.size);
} else {
  console.error(result.error); // "not found", "path not allowed", etc.
}
```

## Browser Utilities

For browser-based chunked uploads, use the utils package:

```typescript
import { chunks, formatBytes } from "@valentinkolb/filegate/utils";

// Prepare a file for chunked upload
const upload = await chunks.prepare({
  file: fileInput.files[0],
  chunkSize: 5 * 1024 * 1024,
});

console.log(upload.checksum);    // "sha256:..."
console.log(upload.totalChunks); // Number of chunks
console.log(formatBytes({ bytes: upload.fileSize })); // "52.4 MB"

// Subscribe to progress updates
upload.subscribe((state) => {
  console.log(`${state.percent}% - ${state.status}`);
});

// Upload all chunks with retry and concurrency
await upload.sendAll({
  skip: alreadyUploadedChunks,
  retries: 3,
  concurrency: 3,
  fn: async ({ index, data }) => {
    await fetch("/api/upload/chunk", {
      method: "POST",
      headers: {
        "X-Upload-Id": uploadId,
        "X-Chunk-Index": String(index),
      },
      body: data,
    });
  },
});
```

## API Endpoints

All `/files/*` endpoints require `Authorization: Bearer <token>`.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/docs` | OpenAPI documentation (Scalar UI) |
| GET | `/openapi.json` | OpenAPI specification |
| GET | `/llms.txt` | LLM-friendly markdown documentation |
| GET | `/files/info` | Get file or directory info |
| GET | `/files/content` | Download file or directory (TAR) |
| PUT | `/files/content` | Upload file |
| POST | `/files/mkdir` | Create directory |
| DELETE | `/files/delete` | Delete file or directory |
| POST | `/files/move` | Move file or directory |
| POST | `/files/copy` | Copy file or directory |
| GET | `/files/search` | Search with glob pattern |
| POST | `/files/upload/start` | Start or resume chunked upload |
| POST | `/files/upload/chunk` | Upload a chunk |

## Security

Filegate implements multiple security layers:

- **Path validation**: All paths are validated against allowed base paths
- **Symlink protection**: Symlinks pointing outside base paths are blocked
- **Path traversal prevention**: Sequences like `../` are normalized and checked
- **Size limits**: Configurable limits for uploads, downloads, and chunks
- **Search limits**: Glob pattern complexity is limited to prevent DoS
- **Secure headers**: X-Frame-Options, X-Content-Type-Options, etc.

## Development

```bash
# Install dependencies
bun install

# Run server
FILE_PROXY_TOKEN=dev ALLOWED_BASE_PATHS=/tmp bun run src/index.ts

# Run tests
bun run test:unit
bun run test:integration:run
```

## License

MIT
