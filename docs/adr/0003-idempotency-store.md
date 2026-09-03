# ADR-0003: Idempotency key store

- Status: Accepted
- Date: 2026-09-03

## Context

`POST /v1/verbs/{name}` accepts optional `idempotency_key`. Conflicting reuse should yield HTTP 409; identical reuse should return the original `task_id`.

## Decision

Persist idempotency in SQLite table:

```sql
CREATE TABLE idempotency_keys (
  key TEXT PRIMARY KEY,
  verb TEXT NOT NULL,
  request_hash TEXT NOT NULL, -- SHA-256 of canonical JSON {verb, args, stdin_present}
  task_id TEXT NOT NULL,
  created_at TEXT NOT NULL   -- RFC3339Nano UTC
);
```

- **TTL:** 24h from `created_at` (lazy delete on lookup + periodic GC).
- **Same key + same hash:** return existing task (`202` with original `task_id` / status).
- **Same key + different hash:** `409` `{code:"idempotency_conflict"}`.
- Keys are **per token identity** (single agent token in MVP ⇒ global unique `key`).
- Empty/missing key ⇒ always create a new task.

## Consequences

- Implemented with queue package in M4/M2 boundary; M2 may accept the field and store once queue exists.
- Canonical hash must redact stdin body content (hash a boolean `stdin_present` + length, not secret bytes).
