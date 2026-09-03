# ADR-0002: Audit vs SQLite atomicity (outbox)

- Status: Accepted
- Date: 2026-09-03

## Context

Spec says “enqueue + audit-append in the same transaction where feasible.” Audit is NDJSON on the filesystem; tasks live in SQLite. A single ACID transaction cannot span both.

## Decision

**SQLite is the source of truth for task state.** Audit uses a transactional **outbox**:

1. In one SQLite transaction: insert/update `tasks` / `attempts` and append a row to `audit_outbox(id, ts, payload_json, written_at NULL)`.
2. A dedicated writer drains `audit_outbox` in order, appends NDJSON to `logs/audit.log`, `fsync`s, then marks `written_at`.
3. On crash: unwritten outbox rows are drained again (at-least-once audit). Consumers must tolerate duplicate lines keyed by `(task_id, attempt, state)`.

Denied/canceled paths still record outbox rows before returning to the client when a task row exists.

## Consequences

- Schema gains `audit_outbox` (M4).
- True “never lose a transition” is guaranteed for DB-backed states; file-only tooling must read outbox if log tail is behind.
- fsync-per-line remains on the drain path; batching fsync within a drain tick is allowed if ordering is preserved (NFR may be revisited).
