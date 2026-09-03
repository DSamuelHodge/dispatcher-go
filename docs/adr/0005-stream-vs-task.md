# ADR-0005: Stream vs task for microphone and sms-follow

- Status: Accepted
- Date: 2026-09-03

## Context

Spec §5.3 listed `microphone-record` and `sms-inbox.follow` as watch streams. True watch verbs hold a long-lived process and expose a ring buffer (`POST/GET/DELETE /v1/streams`).

- `termux-microphone-record` is typically a **bounded capture** (`-l seconds` or stop) writing a file — a one-shot **task**, not an event stream.
- `termux-sms-list` / inbox is **one-shot**; there is no upstream “follow” mode. A generic poll-wrapper was an explicit non-goal (§2).

## Decision

| Capability | MVP shape | API |
|------------|-----------|-----|
| `location.stream` | watch stream | `/v1/streams` |
| `sensor.stream` | watch stream | `/v1/streams` |
| `microphone.record` | one-shot task | `/v1/verbs/microphone.record` |
| `sms.read` | one-shot task | `/v1/verbs/sms.read` |
| `sms-inbox.follow` | **deferred** (post-MVP) | — |

No poll-wrapper streams in MVP.

## Consequences

- Seed `verbs.yaml` matches this table.
- M6 implements only location/sensor streams.
- If product later needs SMS follow, add an explicit poll stream type under a new ADR (contradicts current §2 only if accepted).
