# ADR-0006: Allowed non-stdlib dependencies

- Status: Accepted
- Date: 2026-09-03

## Context

Locked decision originally allowed only `gopkg.in/yaml.v3`. Durability requires SQLite; pure-Go driver pulls transitive packages.

## Decision

**Allowed direct dependencies (MVP):**

1. `gopkg.in/yaml.v3` — catalog parse
2. `modernc.org/sqlite` — pure-Go SQLite (see ADR-0001)

Stdlib covers HTTP, exec, auth compare, NDJSON encoding, testing.

No other direct deps without a new ADR (logging frameworks, routers, ORMs are out).

## Consequences

- `go.mod` will list transitive modules of `modernc.org/sqlite`; that is expected.
- Reviewers should reject PRs adding routers, log SDKs, etc. without ADR update.
