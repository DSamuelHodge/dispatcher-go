# ADR-0004: Catalog reload — restart only (MVP)

- Status: Accepted
- Date: 2026-09-03

## Context

Spec mentioned SIGHUP reload “best-effort.” Hot reload races in-flight tasks, open streams, and circuit-breaker maps against a new verb set.

## Decision

**MVP: catalog loads at process start only.** Adding/changing verbs requires editing `verbs.yaml` and **restarting** the daemon. SIGHUP may log “reload not supported; restart required” but must not swap the catalog.

Post-MVP may add coordinated reload (drain → swap → re-admit) under a new ADR.

## Consequences

- Simpler M1–M7; matches “YAML edit + restart” operator story in the README.
- Boot hook always starts with a consistent catalog snapshot.
