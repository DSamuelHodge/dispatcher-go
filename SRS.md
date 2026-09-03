# Software Requirements Specification (SRS) — dispatcher-go

> Derived from `spec.md` v1.0 (2026-09-03). Normative for MVP unless marked post-MVP.
> Target: loopback HTTP daemon `127.0.0.1:8477`, Go single static binary for Termux.

## 1. Purpose

Provide the **exclusive, auditable, crash-safe interface** between an on-device AI agent and
Termux:API (`termux-*` CLI surface, app v0.53.0). The daemon mediates every device-capability
invocation through authentication, risk-based approval, redacted auditing, durable queuing with
retry, circuit-breaking, and bounded streaming subscriptions — all driven by a declarative
`verbs.yaml` catalog requiring no code change to add verbs.

## 2. Scope

**In scope (MVP):**

- Loopback-only HTTP API (`POST /v1/verbs/{name}`, task query, stream lifecycle, catalog, health).
- Declarative verb catalog (`verbs.yaml` v1) covering Tier A (perceive) and Tier B (one-shot act +
  watch streams) from the verified Termux:API surface (§5 of spec).
- Token auth (`X-Agent-Token`, `.agent-token` chmod 0600).
- Approval gate (`ask` via `termux-dialog confirm` 120s block; `always-approve` auto-execute;
  `force_ask` override; `~/.agent/approval-policy.json` runtime merge; future `approval_backend: float`).
- Secret hygiene (`stdin:true` piped + `[REDACTED]` everywhere).
- NDJSON audit log (`logs/audit.log`, fsync/line, lifecycle
  `requested → approved|denied → executing → executed|timeout|failed|will-retry|exhausted`).
- SQLite durability (`data/tasks.db`, WAL + `synchronous=FULL`), retry (1+5, exp backoff base 1s ×2 +
  jitter ≤250ms, retryable = timeout/failed/nonzero-exit only), exhaustion notify via
  `termux-notification` (circuit bypass allowlisted), per-template circuit-breaker
  (5 consecutive → open 60s → half-open probe), crash resume (pending/executing→pending/
  retry-due, `created_at` order, no phantom-executed).
- Watch streams (location/sensor/mic/sms-follow) with ring buffer (default 128):
  `POST /v1/streams → GET poll (?since=) → DELETE`.
- Boot lifecycle (`~/.termux/boot/01-start-agent` + resume).
- `go test` matrix with fake-`termux-*` PATH shim for CI.

**Out of scope (explicit non-goals):**

- Tier C UI automation, X11 control, STT libraries, SSH/ngrok remote exposure (threat-model conflict).
- Tasker/Float adapters (post-MVP `adapter:` verb type).

## 3. Stakeholders & actors

| Actor | Interest |
|---|---|
| On-device agent (sole HTTP client) | Single interface for perceive/act/watch with predictable errors |
| Device owner (approver) | On-device `termux-dialog` consent; away/DND via explicit `always-approve` |
| Operator (Termux user) | Boot survival, observable health, tamper-evident audit |

## 4. Definitions

Verb, Tier A/B, risk (none/low/medium/high), approval (inherit/ask/always-approve + force_ask),
`stdin:true`, task, attempt, stream, circuit (closed/open/half-open), exhaustion.

## 5. Functional requirements

### FR-1 Verb catalog

- FR-1.1 `verbs.yaml` `version: 1` with `daemon{listen, approval_mode, approval_backend,
  task_timeout_s, max_retries, backoff_base_s, cb_trip_threshold, cb_open_s}` + `verbs[]`
  `{name, tier, risk, approval, force_ask_even_if_global_auto, argv, args[], stdin_arg,
  timeout_s, retries?, retry_backoff?, circuit_breaker_threshold?, parser?, watch?}`.
- FR-1.2 `argv[0]` MUST be a `termux-*` binary from `termux-api-package/scripts/`; unknown
  binary/flag rejected at load. Tier A MUST NOT contain mutating argv (load-time validation).
- FR-1.3 `{{.arg}}` template substitution only; execution via `os/exec` directly — no shell.
  `wifi.info` accepts both `connectioninfo`/`connection-info` spellings (one verb).
- FR-1.4 Seed catalog per spec §5 (Tier A list, Tier B act list with corrected shapes —
  `termux-sms-send -n "<n>" "<t>"`, `termux-telephony-call`, `termux-camera-photo -c`,
  `termux-tts-speak "<t>"`, `termux-media-player play -f`, `termux-brightness N`,
  `termux-wallpaper -f`, `termux-toast` vs `termux-notification` split, `termux-open` for URLs,
  `termux-sensor -s/-n/-d/-a/-l`, `termux-location -p/-r`; dropped non-binaries listed in spec §5.2).
- FR-1.5 Catalog reload on start/restart (SIGHUP reload best-effort); adding a verb = YAML edit only.

### FR-2 HTTP API (bind `127.0.0.1` only; deny non-loopback)

- FR-2.1 `POST /v1/verbs/{name}` `{args, stdin?, idempotency_key?}` → `202 {task_id, status}`.
  Unknown verb → 404; validation failure → 400; bad/absent token → 401; open circuit → 503
  `circuit_open`. Gated verbs may return `pending_approval`.
- FR-2.2 `GET /v1/tasks/{id}` → status + `argv_redacted` + truncated stdout/stderr (4KB cap).
- FR-2.3 `GET /v1/tasks?state=` (pending|retry_scheduled|exhausted|…).
- FR-2.4 Streams: `POST /v1/streams {verb, args}` → `{stream_id}`; `GET /v1/streams/{id}?since=`;
  `DELETE /v1/streams/{id}` (kill proc, free buffer, audit entry). Unknown/closed id → 404.
- FR-2.5 `GET /v1/verbs` (effective catalog), `GET /v1/health`
  `{mode, queue_depth, cb_states, uptime}`.

### FR-3 Auth

- FR-3.1 All requests require `X-Agent-Token`, constant-time compare; 401 on mismatch.
- FR-3.2 Token persisted in `.agent-token` (0600, generated `rand 32B hex` on first boot).

### FR-4 Approval & redaction

- FR-4.1 Resolution order: `force_ask` > per-verb > `approval-policy.json` > `daemon.approval_mode`.
- FR-4.2 `ask`: run `termux-dialog confirm -t "Approve <verb>?"` (redacted args), 120s block;
  `yes` → approved; else/timeout → denied. Denied/canceled NEVER retry.
- FR-4.3 `always-approve`: auto-execute, audit `approved{by:"policy"}`. Explicitly user-enabled only.
- FR-4.4 `stdin:true`: body → child stdin pipe; `[REDACTED]` in dialog, task GET, audit, SQLite.

### FR-5 Audit

- FR-5.1 NDJSON `logs/audit.log`, one object per transition with
  `{ts, task_id, verb, tier, risk, approval, argv_redacted, exit_code, latency_ms, attempt, error}`.
- FR-5.2 `fsync` per write; no secrets ever persisted (enforced by redaction tests).

### FR-6 Durability, retry, circuit-breaker, resume

- FR-6.1 SQLite `journal_mode=WAL`, `synchronous=FULL`; schema `tasks/attempts/streams` per spec §9;
  enqueue + audit in one transaction where feasible.
- FR-6.2 Retryable = `timeout|failed|nonzero-exit` only; schedule `base*2^n + jitter`, attempts 0–5.
- FR-6.3 Exhaustion → state `exhausted` + `termux-notification --title "dispatcher: <verb> exhausted"
  -c "task <id> after 6 attempts"` (bypasses open circuit).
- FR-6.4 CB per verb-template: 5 consecutive timeout/fail → open 60s (fast 503) → single half-open
  probe → close on success.
- FR-6.5 Boot resume: requeue `pending`, `executing`→`pending`, due `retry_scheduled`, ordered by
  `created_at`; never mark executed without waitpid result (kill -9 test).

### FR-7 Boot

- FR-7.1 `~/.termux/boot/01-start-agent` starts daemon at boot; resume per FR-6.5; health reflects
  recovered depth.

## 6. Non-functional requirements

- NFR-1 Security: loopback-only bind; no TLS (loopback); no shell; no remote expose; 0600 token;
  redaction total; `force_ask` cannot be overridden by global auto.
- NFR-2 Reliability: WAL+FULL durability; idempotent resume; bounded streams; proc-group kill on
  DELETE/timeout; no goroutine/process leaks.
- NFR-3 Observability: audit fsync; health endpoint; per-attempt rows; `go test` + fake-PATH shim CI.
- NFR-4 Performance: p99 Tier A passthrough <250ms overhead (excl. `termux-*` runtime);
  audit write <5ms; 50 concurrent verbs without deadlock; stream buffer O(1) eviction.
- NFR-5 Portability: single static Go binary; stdlib-first; only `yaml.v3` extra dep; Termux paths
  (`$PREFIX/bin`) resolved at runtime.
- NFR-6 Maintainability: `internal/{config,verbs,auth,approve,exec,audit,queue,retry,circuit,streams,http}.go`
  layout; `cmd/dispatcher/main.go` entrypoint.

## 7. Data & schema

SQLite DDL (normative):

```sql
PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE tasks(id TEXT PRIMARY KEY, verb TEXT NOT NULL, args_json TEXT NOT NULL,
  argv_redacted TEXT NOT NULL, state TEXT NOT NULL, attempt INTEGER NOT NULL DEFAULT 0,
  next_run_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE attempts(task_id TEXT NOT NULL, n INTEGER NOT NULL,
  started_at TEXT NOT NULL, ended_at TEXT, exit_code INTEGER, error TEXT,
  PRIMARY KEY(task_id, n));
CREATE TABLE streams(id TEXT PRIMARY KEY, verb TEXT NOT NULL, pid INTEGER,
  buf_path TEXT, created_at TEXT NOT NULL);
```

## 8. Error handling & status codes

400 validation/unknown-arg · 401 token · 404 verb/task/stream · 409 conflict (duplicate idempotency
with differing payload) · 503 circuit_open/queue-full · 504 approval-timeout (→ denied) /
task-timeout (→ retryable). All errors JSON `{code, message, task_id?}` + audit entry.

## 9. Acceptance criteria (MVP)

1. `battery.status` round-trips against real `termux-battery-status` with token; wrong token → 401.
2. `sms.send` in `ask` mode blocks on real `termux-dialog confirm`; deny → denied, no retry, audited.
3. `stdin:true` secret appears nowhere (audit, GET, dialog, db) — grep test passes.
4. Kill -9 mid-task → reboot resumes as pending in order; no phantom-executed.
5. 6th failure → exhausted + real `termux-notification`; CB opens after 5, half-open recovers.
6. `location.stream`/`sensor.stream` POST→GET→DELETE with buffer cap; DELETE kills proc.
7. New verb added via YAML-only edit works after restart; Tier A mutating argv rejected at load.
8. `go test ./...` green incl. fake-PATH shim suite.

## 10. Traceability

| SRS | spec.md | Milestone |
|---|---|---|
| FR-1 | §4–§5 | M1, M7 |
| FR-2/FR-3 | §6–§7 | M2 |
| FR-4 | §7 | M3 |
| FR-5/FR-6 | §8–§9 | M4–M5 |
| Streams | §5.3 | M6 |
| Boot | §9–§10 | M5 |
| NFR/Acceptance | §11 | M7 |
