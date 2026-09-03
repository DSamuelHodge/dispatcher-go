# QA Report — ADR 0001–0006 implementation verification

- Date: 2026-09-03
- Scope: `docs/adr/README.md` + `0001`–`0006` vs. codebase at commit `2551ef5` (+ working tree)
- Method: source read of `cmd/`, `internal/`, `verbs.yaml`, CI/boot scripts; `go vet ./...`,
  `go test ./... -count=1` (2 runs), `go run ./cmd/dispatcher -validate`

## Verification results

| ADR | Title | Verdict |
|-----|-------|---------|
| 0001 | SQLite driver (pure Go) | ✅ PASS (1 doc nit) |
| 0002 | Audit vs SQLite atomicity (outbox) | ⚠️ PARTIAL — 2 gaps |
| 0003 | Idempotency key store | ❌ FAIL — schema only, no logic |
| 0004 | Catalog reload: restart only (MVP) | ⚠️ PASS w/ 1 gap (SIGHUP) |
| 0005 | Stream vs task for mic / sms-follow | ✅ PASS |
| 0006 | Allowed non-stdlib dependencies | ✅ PASS |

## Test / vet / validate baseline

- `go test ./... -count=1`: all packages pass on re-run (GO EXIT 0). First run had one
  failure: `TestStartGetDelete` (`internal/streams/streams_test.go:54`, "no events").
  Passes in isolation (`-count=2` green). Root cause: timing-sensitive — 2s deadline /
  20ms poll starves under full-suite parallel load. Flake, not logic bug. Recommend:
  raise deadline to 10s or gate CI with `-p 1` for the streams package. (Issue #8)
- `go vet ./...`: 5 warnings, all in `internal/api/server_test.go` (using HTTP response
  before error check, lines 203/250/267/279/295). Test-only, non-blocking. (Issue #8)
- `go run ./cmd/dispatcher -validate`: `catalog: verbs.yaml (44 verbs) OK`.

## Findings (severity-ranked)

### F1 [HIGH] ADR-0003 not implemented — idempotency key accepted but ignored
- `internal/api/server.go:167` parses `idempotency_key`; nothing downstream reads it.
- `idempotency_keys` table DDL matches the ADR (`internal/queue/sqlite.go:91-97`) but no
  code inserts, looks up, hashes, or GCs. No `409 idempotency_conflict` path exists.
- Impact: duplicate POSTs (client retries) create duplicate tasks — exactly what the ADR
  was written to prevent. → New issue: implement per ADR (canonical SHA-256 over
  `{verb, args, stdin_present+length}`, same-key/same-hash → original task, conflict → 409,
  24h TTL with lazy delete + GC).

### F2 [MEDIUM] ADR-0002 same-transaction atomicity not held
- ADR: "In one SQLite transaction: insert/update tasks/attempts + outbox row."
- Reality: `SQLite.Create` is a bare INSERT (`internal/queue/sqlite.go:123-129`); every
  `Update()` + `audit()` pair in `server.go` / `worker.go` is two separate statements
  (e.g. `server.go:225-244`, `worker.go:138-159`). Crash between them loses a transition.
- Fix: add `CreateWithAudit` / `UpdateWithAudit` tx-scoped helpers (or an `AppendAuditTx`).

### F3 [MEDIUM] ADR-0002 — `Resume()` transitions are never audited
- `internal/queue/resume.go:28-61`: executing→pending, accepted→pending,
  pending_approval→denied via `Update` only, no outbox rows. The crash-denial directly
  contradicts "Denied/canceled paths still record outbox rows."
- Fix: emit outbox rows for each resume transition (states: `resumed_pending`, `denied`).

### F4 [MEDIUM] ADR-0004 — SIGHUP kills the daemon ungracefully
- `cmd/dispatcher/main.go:139` handles SIGINT/SIGTERM only. SIGHUP default disposition
  terminates immediately: no HTTP drain, no `Streams.CloseAll`, no WAL checkpoint.
  ADR permits "log reload not supported" — current behavior is worse (abrupt death).
- Fix: handle SIGHUP as graceful shutdown (same path as SIGTERM) or ignore + log line.

### F5 [MEDIUM, spec-adjacent] Streams bypass approval / CB / queue-depth gates
- `handlePostStream` (`internal/api/server.go:414-462`) never calls `approve.Resolve`,
  never checks the circuit breaker or `MaxQueueDepth`. In global `ask` mode a
  `location.stream` starts with no on-device dialog. ADR-0005 is silent here, but spec §7
  gates verbs. Decide: gate stream creation like tasks, or record an explicit ADR exemption.

### F6 [LOW] ADR-0001 doc nit — phantom `driver_stub.go` reference
- ADR-0001 points at `internal/queue/driver_stub.go` ("until M4"); the file does not exist.
  Remove the parenthetical.

### F7 [LOW] Hardcoded health version
- `internal/api/server.go:144` reports `"version": "m6"`; binary is `0.7.0-dev`
  (`cmd/dispatcher/main.go:27`, `-ldflags -X main.Version=`). Wire health to `main.Version`.

### F8 [LOW] Empty optional template args become empty-string flags
- `subst` (`server.go:535-561`) renders missing optional args as `""`, passed literally
  (e.g. `location.once` without provider → `-p ""`), which `termux-location` may reject.
  Consider omitting flag+value pairs whose value is empty and not required.

## Deploy gate opinion

Safe to proceed to USB on-device smoke test with F1–F5 as known issues: none blocks a
`battery.status` round-trip. F5 deserves a conscious call before any `location.stream`
test on-device (approval bypass). F1 must land before any client with retry logic talks
to the daemon.
