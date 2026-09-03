# dispatcher-go

Loopback HTTP daemon (`127.0.0.1:8477`) — the exclusive, auditable interface between an
on-device AI agent and Termux:API. Verbs are declared in `verbs.yaml` (no code change to add
capabilities); high-risk verbs gate on on-device `termux-dialog` approval (or explicit
`always-approve` for away/DND). Every attempt is NDJSON-audited and durably queued in SQLite
(WAL) with retry, circuit-breaking, and crash resume.

- [`spec.md`](spec.md) — full specification (v1.0 MVP)
- [`SRS.md`](SRS.md) — software requirements, traceability, acceptance criteria

```text
cmd/dispatcher/main.go
internal/{config,verbs,auth,approve,exec,audit,queue,retry,circuit,streams,http}.go
verbs.yaml  .agent-token(0600)  logs/audit.log  data/tasks.db
```

Status: specification phase — see issues for the 7 build milestones.
