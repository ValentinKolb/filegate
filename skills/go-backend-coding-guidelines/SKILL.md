---
name: go-backend-coding-guidelines
description: Enforce shared coding standards for Go backend projects. Use when writing, refactoring, or reviewing server-side Go code, especially for HTTP handlers, services/use-cases, data-access layers, background workers, and API error/response contracts.
---

# Go Backend Coding Guidelines

Apply this skill as the default quality layer for backend Go code changes.

## Core Priorities

Apply in this order:

1. YAGNI: implement only what the current milestone needs.
2. KISS: favor obvious code over abstraction-heavy designs.
3. DRY: deduplicate proven repetition, not speculative repetition.
4. Consistency: prefer existing repo conventions over personal preference.

## Mandatory Architecture Rules

1. Keep HTTP handlers thin: parse input, call service, map error/response.
2. Keep business rules in services/use-cases, not in transport layers.
3. Keep data-access details in repository/store packages.
4. Keep package responsibilities explicit and narrow.
5. Avoid deep cross-package internals imports.
6. Avoid hidden global mutable state; pass dependencies explicitly.

## Backend Domain Invariants

Never violate these constraints:

1. Keep trust boundaries explicit (authn/authz, tenancy, internal-only endpoints).
2. Preserve consistency for write operations (validation, persistence, side effects).
3. Keep idempotency rules explicit for mutating endpoints/jobs where relevant.
4. Keep public API contracts stable unless intentionally versioned.
5. Keep internal/private resources inaccessible from public APIs.
6. Keep side effects deterministic and observable (logs/metrics/events).

## API and Error Conventions

1. Return stable JSON error envelopes: machine-readable `error` and human-readable `message`.
2. Validate early; fail with specific domain error types (`validation_failed`, `not_found`, `conflict`, etc.).
3. Keep response payloads predictable and typed; avoid polymorphic surprises.
4. Keep endpoint semantics deterministic for pagination, filtering, and sorting.
5. Keep optional behavior flags/headers documented and consistently applied.

## Go Style Baseline

1. Prefer stdlib-first solutions.
2. Keep interfaces small and consumer-driven.
3. Keep constructors explicit (`NewX(...)`) and dependency-injected.
4. Keep functions short and single-purpose.
5. Add comments for package intent and non-obvious logic, not trivial lines.
6. Keep concurrency explicit and local (locks/channels scoped tightly).

## Package Documentation Rule (`doc.go`)

For every exported package, add and maintain a `doc.go` with a consistent structure.
Use the template in `references/doc-go-template.md`.

## Change Workflow

For each non-trivial change:

1. Identify the target layer (handler, service, store, domain).
2. Define invariants and failure modes before coding.
3. Implement the smallest complete slice.
4. Add/adjust tests at the same layer as the behavior change.
5. Run quality gates from `references/testing-gates.md`.
6. Keep package docs (`doc.go`) aligned with new responsibilities.

## Reference Routing

Read only what is needed:

1. Use `references/backend-architecture.md` for backend boundaries and invariants.
2. Use `references/go-style.md` for concrete coding and layout conventions.
3. Use `references/doc-go-template.md` when creating/updating package docs.
4. Use `references/testing-gates.md` before finishing implementation.

Do not load all references by default.
