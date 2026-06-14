# Conflict Handling

This is a load-bearing repo invariant: every write surface that can hit a name collision exposes the same vocabulary, with the same default. Skipping or "simplifying" this is how silent data loss enters production.

## The vocabulary (from `domain/conflict.go`)

```go
type ConflictMode string

const (
    ConflictError     ConflictMode = "error"     // default everywhere
    ConflictOverwrite ConflictMode = "overwrite" // file-write only (PUT, upload sessions, transfer)
    ConflictRename    ConflictMode = "rename"    // all surfaces
    ConflictSkip      ConflictMode = "skip"      // mkdir only
)
```

`ParseConflictMode(s, allowed)` validates the string against a per-endpoint allowed set:

- `FileConflictModes` allows `error|overwrite|rename` (rejects `skip`).
- Upload sessions intentionally narrow this to `error|overwrite`; `rename` is
  rejected so commit recovery has one stable target path.
- `MkdirConflictModes` allows `error|skip|rename` (rejects `overwrite` — replacing a directory subtree is a Transfer operation, not a mkdir one).

Empty string → `ConflictError`. Adding new write surfaces? Use these helpers, do not invent new vocabulary.

## Endpoint matrix

| Endpoint                         | Default | Allowed             | Notes                                                          |
|----------------------------------|---------|---------------------|----------------------------------------------------------------|
| `PUT /v1/paths/{path}`           | error   | error/overwrite/rename | Query param `?onConflict=`. Dir-at-target always blocks (except rename → unique sibling). |
| `POST /v1/nodes/{id}/mkdir`      | error   | error/skip/rename   | Body field `onConflict`. Intermediate path segments always reuse-if-dir (mkdir -p semantics). File-at-leaf only resolves with rename. |
| `POST /v1/uploads/sessions` | error   | error/overwrite        | Body field `onConflict`. Optimistic conflict check at session creation; commit verifies the target again atomically. |
| `POST /v1/transfers`             | error   | error/overwrite/rename | Body field `onConflict`. The original; the rest of the API was harmonized to it. |

## Where the logic lives

- `domain.WriteContentByVirtualPath(vp, body, mode)` — file PUT entrypoint;
  rename works against existing files **and** existing directories
  (produces a unique sibling file name).
- `domain.MkdirRelative(parentID, relPath, recursive, ownership, mode)` —
  mkdir; only the LEAF segment respects mode. Rename works against
  existing files **and** existing directories (produces a unique sibling
  directory name).
- `domain.ReplaceFile(parentID, name, srcPath, ownership, mode)` —
  upload-session commit storage helper.
- Transfer (`POST /v1/transfers`) follows the same typed pattern as the
  other write surfaces: HTTP layer parses with `ParseConflictMode`,
  `domain.TransferRequest.OnConflict` is a typed `ConflictMode`. The
  wire-level `apiv1.TransferRequest.OnConflict` stays a `string` for
  backwards compatibility — convert at the adapter boundary.
- `makeUniquePath(target string)` — produces `<stem>-NN<ext>` (capped at
  999, then falls back to `<stem>-<unixMillis><ext>`).

## Rename semantics — by entrypoint

`rename` produces a fresh sibling name.

`WriteContentByVirtualPath` (`PUT /v1/paths`) and `MkdirRelative` (`POST
/v1/nodes/{id}/mkdir`):

| You want to write... | At the target there is... | rename produces                                |
|----------------------|---------------------------|------------------------------------------------|
| File                 | (nothing)                 | File at original name                          |
| File                 | File                      | File at `<stem>-NN<ext>`                       |
| File                 | Directory                 | File at `<stem>-NN<ext>` (dir untouched)       |
| Directory (mkdir)    | (nothing)                 | Directory at original name                     |
| Directory (mkdir)    | Directory                 | Directory at `<stem>-NN`                       |
| Directory (mkdir)    | File                      | Directory at `<stem>-NN` (file untouched)      |

Upload sessions run both checks: session creation fails fast for obvious
conflicts, and commit repeats the authoritative check against live filesystem
state before installing bytes.

## Conflict response body

Use `writeConflict(w, msg, existingID, existingPath)` from `adapter/http/router.go`:

```json
{
  "error": "filename already exists in parent",
  "existingId": "0193...",
  "existingPath": "mount/dir/X"
}
```

Diagnostic fields are best-effort — empty if lookup fails — but always populate them when you know them. The TS SDK and any UI built on top depend on them to render meaningful prompts.

## When you add a new write endpoint

1. Add `OnConflict string` to the request type in `api/v1/types.go`.
2. In the HTTP handler: `mode, err := domain.ParseConflictMode(body.OnConflict, domain.<File|Mkdir>ConflictModes)`.
3. Pass `mode` into the domain method as a typed `domain.ConflictMode`.
4. In the domain method, switch on `mode` and respect `error|overwrite|rename` (and `skip` for mkdir).
5. On 409, use `writeConflict(...)` with the diagnostic fields.
6. Test all three (or four) modes plus the cross-type cases (file-vs-dir).
7. Update the TS SDK type and the docs.
