# Go SDK

Module: `github.com/valentinkolb/filegate/sdk/filegate`. Stateless, scoped, mirrors the TS SDK shape.

## Construction

```go
import "github.com/valentinkolb/filegate/sdk/filegate"

fg, err := filegate.New(filegate.Config{
    BaseURL:    "http://127.0.0.1:8080",
    Token:      "dev-token",
    HTTPClient: nil,                                   // optional, defaults to http.DefaultClient
    UserAgent:  "my-app/1.2.3",                        // optional
    DefaultHeaders: http.Header{"X-Trace": {"..."}},   // optional
})
if err != nil {
    return err
}

// MustNew panics on bad config — use only in main() or tests.
fg = filegate.MustNew(filegate.Config{BaseURL: "...", Token: "..."})
```

## Scoped namespaces

```go
fg.Paths      // PathsClient
fg.Nodes      // NodesClient
fg.Uploads    // UploadsClient (has .Chunked)
fg.Transfers  // TransfersClient
fg.Search     // SearchClient
fg.Index      // IndexClient
fg.Stats      // StatsClient
```

Pure helpers live in dedicated subpackages, reachable without constructing
a client:

```go
import (
    "github.com/valentinkolb/filegate/sdk/filegate/chunks"
    "github.com/valentinkolb/filegate/sdk/filegate/relay"
)

sum := chunks.SHA256Bytes(data)
total := chunks.TotalChunks(size, chunkSize)
start, end, err := chunks.Bounds(index, size, chunkSize)

// HTTP relay (proxying upstream Filegate response to your own ResponseWriter)
n, err := relay.CopyResponse(w, upstreamResp)
```

## Conflict-mode constants

```go
filegate.ConflictError        // "error"      — default
filegate.ConflictOverwrite    // "overwrite"
filegate.ConflictRename       // "rename"

filegate.MkdirConflictError   // "error"
filegate.MkdirConflictSkip    // "skip"
filegate.MkdirConflictRename  // "rename"
```

Use the typed constants over raw strings — they prevent typos and make
search-and-replace safe.

## Common operations

### List mounts and browse

```go
ctx := context.Background()
roots, err := fg.Paths.List(ctx)
for _, m := range roots.Items {
    fmt.Println(m.ID, m.Name, m.Path)
}

// Note the option type is GetNodeOptions (used for both Paths.Get and Nodes.Get),
// passed by value (not by pointer):
meta, err := fg.Nodes.Get(ctx, nodeID, filegate.GetNodeOptions{PageSize: 100})
meta, err = fg.Paths.Get(ctx, "data/photos", filegate.GetNodeOptions{})
```

### One-shot upload

```go
data := strings.NewReader("hello")
res, err := fg.Paths.Put(ctx, "data/uploads/hello.txt", data, filegate.PutPathOptions{
    ContentType: "text/plain",
    OnConflict:  filegate.ConflictError, // default — explicit shown for clarity
})
fmt.Println(res.NodeID, res.CreatedID, res.Node.Path)
```

`PutPathOptions` is passed by value (not pointer). Empty `OnConflict`
defers to the server default ("error").

### Streaming download — two methods, two shapes

```go
// Method 1: PipeContent — writes to an io.Writer, returns metadata (size, headers).
//           Returns *APIError on non-2xx; nothing is written to dst in that case.
result, err := fg.Nodes.PipeContent(ctx, nodeID, false /* inline */, myWriter)
if err != nil { /* ... */ }
fmt.Println("copied", result.Bytes, "bytes; content-type:", result.Header.Get("Content-Type"))

// Method 2: ContentRaw — returns the *http.Response unchanged, including 4xx/5xx.
//           Use this for relay/passthrough handlers. You own resp.Body.Close().
resp, err := fg.Nodes.ContentRaw(ctx, nodeID, false)
if err != nil { return err }       // network-level error only
defer resp.Body.Close()
relay.CopyResponse(w, resp)        // mirrors status + headers + body
```

The same `*Raw` / non-raw split applies to:
- `Paths.Put` (non-raw, throws) vs `Paths.PutRaw` (raw, returns response).
- `Nodes.ThumbnailRaw` (raw only — most callers want to relay the image bytes).
- `Uploads.Chunked.SendChunk` (non-raw) vs `SendChunkRaw` (raw).

### Mkdir

```go
recursive := true
node, err := fg.Nodes.Mkdir(ctx, parentID, filegate.MkdirRequest{
    Path:       "subdir/nested",
    Recursive:  &recursive,                            // *bool, not bool
    OnConflict: string(filegate.MkdirConflictSkip),    // idempotent; wire field is string
    Ownership:  &filegate.Ownership{UID: ptrInt(1000), GID: ptrInt(1000), Mode: "750"},
})

func ptrInt(i int) *int { return &i }
```

`MkdirRequest`, `TransferRequest`, and `ChunkedStartRequest` all keep
their `OnConflict` field as a wire-level `string` (they're aliases of
`api/v1` types). Cast the typed constants explicitly. `PutPathOptions`
is the one Go-SDK-only type whose `OnConflict` is the typed
`FileConflictMode` directly.

### Transfer (move/copy)

```go
recursiveOwnership := true
out, err := fg.Transfers.Create(ctx, filegate.TransferRequest{
    Op:             "move",
    SourceID:       srcID,
    TargetParentID: parentID,
    TargetName:     "destination.bin",
    OnConflict:     string(filegate.ConflictRename), // wire field is string-typed
}, &recursiveOwnership /* third arg: *bool */)
```

`apiv1.TransferRequest.OnConflict` is a `string` on the wire, so cast the
typed constant. (The other write endpoints have first-class typed fields;
Transfer's wire shape is older and intentionally string-only on the
boundary.)

The third argument controls the `recursiveOwnership` query param.

### Search

```go
files := true
res, err := fg.Search.Glob(ctx, filegate.GlobOptions{
    Pattern:    "**/*.{jpg,png}",
    Paths:      []string{"data"},
    Limit:      200,
    Files:      &files,           // *bool — server defaults if nil
    Directories: nil,             // not requested → server default (false)
})
```

### Chunked upload — streaming, bandwidth-saving start check

The example below opens the file once, hashes it streaming, then reads each
chunk via `io.SectionReader` — peak memory is bounded by
`concurrency * chunkSize`, not the file size. See [`chunked-uploads.md`](chunked-uploads.md)
for the full async/parallel variant. **Do not use `os.ReadFile` for large
chunked uploads** — that defeats the purpose by buffering the whole file
in memory.

```go
import (
    "io"
    "github.com/valentinkolb/filegate/sdk/filegate/chunks"
)

f, err := os.Open(srcPath)
if err != nil { return err }
defer f.Close()
info, err := f.Stat()
if err != nil { return err }
size := info.Size()
const chunkSize = int64(8 << 20)

// Streaming whole-file hash — no memory blow-up.
checksum, err := chunks.SHA256Reader(f)
if err != nil { return err }
if _, err := f.Seek(0, io.SeekStart); err != nil { return err }

start, err := fg.Uploads.Chunked.Start(ctx, filegate.ChunkedStartRequest{
    ParentID:   parentID,
    Filename:   filepath.Base(srcPath),
    Size:       size,
    Checksum:   checksum,
    ChunkSize:  chunkSize,
    OnConflict: string(filegate.ConflictError), // 409 immediately if exists
})
if err != nil {
    var fe *filegate.APIError
    if errors.As(err, &fe) && fe.IsConflict() {
        // fe.ExistingID, fe.ExistingPath — see error model below
    }
    return err
}

total := chunks.TotalChunks(size, chunkSize)
for i := 0; i < total; i++ {
    s, e, _ := chunks.Bounds(i, size, chunkSize)
    section := io.NewSectionReader(f, s, e-s)
    buf := make([]byte, e-s)
    if _, err := io.ReadFull(section, buf); err != nil { return err }
    res, err := fg.Uploads.Chunked.SendChunk(ctx, start.UploadID, i,
        bytes.NewReader(buf), chunks.SHA256Bytes(buf))
    if err != nil { return err }
    if res.Completed {
        fmt.Println("done:", res.Complete.File.ID)
    }
}
```

## Error model

API errors return as `*APIError` (use `errors.As`):

```go
type APIError struct {
    StatusCode   int
    Message      string
    Body         string  // raw response body (truncated at 1 MiB)
    ExistingID   string  // populated on 409, empty otherwise
    ExistingPath string  // populated on 409, empty otherwise
}

func (e *APIError) IsConflict() bool   // → e.StatusCode == 409
```

Usage:

```go
_, err := fg.Paths.Put(ctx, path, body, filegate.PutPathOptions{})
if err != nil {
    var apiErr *filegate.APIError
    if errors.As(err, &apiErr) {
        log.Printf("filegate %d: %s", apiErr.StatusCode, apiErr.Message)
        if apiErr.IsConflict() {
            // apiErr.ExistingID, apiErr.ExistingPath have the diagnostic
            // fields the daemon sent — render a "what should we do?" prompt
            // without an extra resolve round-trip.
            log.Printf("collides with %s (id %s)", apiErr.ExistingPath, apiErr.ExistingID)
        }
    } else {
        // ctx canceled, network error, etc.
    }
}
```

`*APIError` is only returned by the **non-`Raw`** methods. The `*Raw`
methods return the raw `*http.Response` on 4xx/5xx — including the JSON
body with `existingId`/`existingPath`. That's the contract that makes them
relay-friendly.

## Context propagation

Every method takes a `context.Context`. Pass your incoming HTTP request's
context so cancellations propagate:

```go
func myHandler(w http.ResponseWriter, r *http.Request) {
    res, err := fg.Paths.Put(r.Context(), "data/file.bin", r.Body, filegate.PutPathOptions{})
    // ...
}
```

If the client cancels the upstream request, the Filegate call cancels too —
no orphaned uploads.
