# Go Style Conventions for Backend Projects

Use this file for concrete coding decisions.

## Package and File Layout

1. Keep package names short, lowercase, and responsibility-specific.
2. Keep files grouped by concern (e.g. `handler_*.go`, `service_*.go`, `store_*.go`).
3. Add `doc.go` for exported packages.
4. Avoid circular dependencies; extract shared types to a neutral package.

## Public API Shape

1. Use explicit constructors (`NewX(...)`) with dependency injection.
2. Keep interfaces near the consumer, not the implementation.
3. Keep interfaces minimal; do not create interfaces for a single local call site.
4. Use config structs when parameter lists exceed 3-4 inputs.

## Function and Method Style

1. Keep methods short and linear; reduce branching depth.
2. Return early on invalid input and error paths.
3. Prefer explicit values over hidden mutation.
4. Use named return values only when this increases readability.

## Error Style

1. Return `error`; avoid panic for expected runtime failures.
2. Wrap internal errors with context (`fmt.Errorf("...: %w", err)`).
3. Map domain errors to stable API error codes in one place.
4. Do not leak storage-driver internals directly to API clients.

## Concurrency Style

1. Scope mutexes and channels tightly to the owning type.
2. Document invariants for lock-protected state.
3. Avoid shared mutable state across packages.
4. Prefer clear serialization rules over implicit concurrent writes.

## Data Access Style

1. Parameterize all user-provided values in SQL/queries.
2. Keep query construction centralized and testable.
3. Keep transactions explicit and minimal in scope.
4. Keep persistence mapping and query logic close to repository/store code.

## Comments and Sections

1. Comment package intent in `doc.go`.
2. Comment non-obvious behavior, assumptions, and side effects.
3. Use section separators for large files when they improve scanability.
4. Remove stale comments in the same change that makes them stale.
