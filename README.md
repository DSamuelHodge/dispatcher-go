# dispatcher-go

Loopback HTTP daemon (`127.0.0.1:8477`) — the exclusive, auditable interface between an
on-device AI agent and Termux:API. Verbs are declared in `verbs.yaml` (no code change to add
capabilities); high-risk verbs gate on on-device `termux-dialog` approval (or explicit
`always-approve` for away/DND). Every attempt is NDJSON-audited and durably queued in SQLite
(WAL) with retry, circuit-breaking, and crash resume.

- [`spec.md`](spec.md) — full specification (v1.0 MVP)
- [`SRS.md`](SRS.md) — software requirements, traceability, acceptance criteria
- [`docs/adr/`](docs/adr/) — architecture decision records
- [`SECURITY.md`](SECURITY.md) — reporting & operator hardening
- [`LICENSE`](LICENSE) — MIT

## Layout

```text
cmd/dispatcher/main.go
internal/
  config/ verbs/ termuxallow/   # M1
  auth/ api/ execx/ queue/      # M2 (in-memory tasks; SQLite in M4)
  approve/ audit/               # M3
  queue/ (sqlite) worker/ retry/ notify/  # M4
  circuit/                              # M5
  streams/                              # M6
verbs.yaml
docs/adr/
```

Runtime (never committed): `.agent-token` (0600), `logs/audit.log`, `data/tasks.db`

## Quick start

```bash
go test ./...
go run ./cmd/dispatcher -validate
go run ./cmd/dispatcher                 # listens on 127.0.0.1:8477 (worker on; use -sync-exec for inline)
# token is created at ./.agent-token on first run
curl -sS -H "X-Agent-Token: $(tr -d '\n' < .agent-token)" \
  -H 'Content-Type: application/json' \
  -d '{}' http://127.0.0.1:8477/v1/verbs/battery.status
```

Routes: `GET /v1/health`, `GET /v1/verbs`, `POST /v1/verbs/{name}`, `GET /v1/tasks/{id}`, `GET /v1/tasks`.

Status: **M5 complete** (circuit-breaker, crash resume, Termux boot hook).
Streams M6 / full seed+CI M7 — see issues #1–#7.

## Boot (Termux)

Copy [`scripts/01-start-agent`](scripts/01-start-agent) to `~/.termux/boot/01-start-agent` (Termux:Boot).
On start the daemon runs crash-resume (`executing`→`pending`) before accepting work.
