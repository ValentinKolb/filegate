# Backend Architecture Guardrails

Use this reference when design decisions are unclear.

## Layer Boundaries

1. Handler layer: decode request, auth checks, call service, encode response.
2. Service/use-case layer: apply business rules, validation orchestration, transaction boundaries.
3. Repository/store layer: execute persistence operations and data mapping.
4. Infrastructure layer: external clients, queues, caches, metrics, and logging adapters.
5. Worker/scheduler layer: orchestrate async flows using the same service contracts.

Do not move service rules into handlers or store SQL plumbing into services.

## Non-Negotiable Invariants

1. Enforce authentication and authorization at clear boundaries.
2. Preserve tenant/project/environment isolation when multi-tenant behavior exists.
3. Keep write flows consistent:
   - validations run before persistence,
   - transactions define atomic boundaries,
   - events/jobs are emitted only from committed state.
4. Keep private/internal resources inaccessible via public API paths.
5. Keep retries/idempotency safe for both sync and async execution paths.

## API Consistency Rules

1. Keep error envelopes stable across endpoints.
2. Keep list metadata contract consistent (`limit`, `offset`, cursors, total fields where used).
3. Keep filtering/sorting semantics deterministic and documented.
4. Keep backward compatibility unless version boundaries explicitly change.

## Data Evolution Rules

1. Keep schema/data migrations explicit, reversible where possible, and reviewed.
2. Validate input/output contract changes with tests before rollout.
3. Keep data-access constraints and indexes explicit in migration/code paths.
4. Keep compatibility strategy clear for rolling deployments.

## Runtime and Isolation

1. Keep concurrency controls scoped to owned state.
2. Avoid global locks that block unrelated workloads.
3. Keep startup/shutdown lifecycles explicit and graceful.
4. Keep observability baseline present (structured logs, metrics, health checks).
