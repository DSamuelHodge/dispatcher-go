# ADR-0001: SQLite driver (pure Go)

- Status: Accepted
- Date: 2026-09-03

## Context

The daemon needs crash-safe task durability (WAL + `synchronous=FULL`) on Termux (Android userland). CGO-based drivers (`mattn/go-sqlite3`) complicate cross-compile and Termux builds.

## Decision

Use **`modernc.org/sqlite`** (pure Go, no CGO) as the only SQLite driver.

## Consequences

- Pin a driver version compatible with the project `go` directive (MVP: `modernc.org/sqlite v1.34.5` with `go 1.22`). Newer driver majors may raise the minimum Go version.
- Slightly different performance vs CGo SQLite; acceptable for on-device queue depths in the low thousands.
- Import side-effect registration: `_ "modernc.org/sqlite"` with `database/sql` driver name `"sqlite"`.
