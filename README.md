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
  config/ verbs/ termuxallow/   # M1 implemented
  auth/ approve/ execx/ audit/  # scaffold (later milestones)
  queue/ retry/ circuit/ streams/ api/
verbs.yaml
docs/adr/
```

Runtime (never committed): `.agent-token` (0600), `logs/audit.log`, `data/tasks.db`

## Quick start (M1)

```bash
go test ./...
go run ./cmd/dispatcher -version
go run ./cmd/dispatcher -validate
```

Status: **M1 complete** (catalog load + allowlist validation). HTTP/auth begins in M2 — see issues #1–#7.
