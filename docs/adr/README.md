# Architecture Decision Records

ADRs capture locked design choices for dispatcher-go. Numbers are stable; status may move from Proposed → Accepted → Superseded.

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-sqlite-driver.md) | SQLite driver (pure Go) | Accepted |
| [0002](0002-audit-outbox.md) | Audit vs SQLite atomicity (outbox) | Accepted |
| [0003](0003-idempotency-store.md) | Idempotency key store | Accepted |
| [0004](0004-catalog-reload.md) | Catalog reload: restart only (MVP) | Accepted |
| [0005](0005-stream-vs-task.md) | Stream vs task for mic / sms-follow | Accepted |
| [0006](0006-allowed-dependencies.md) | Allowed non-stdlib dependencies | Accepted |
