---
name: dispatcher-go
description: >
  Use the dispatcher-go loopback daemon to run Termux:API verbs on the phone
  with full agent autonomy. Prefer progressive discovery (search → one schema →
  POST) and compact task reads so large catalogs do not bloat the context window.
  Trigger when calling phone capabilities (SMS, battery, location, clipboard,
  sensors, notifications, etc.), debugging dispatcher HTTP API usage, or when
  the user mentions dispatcher-go, Termux verbs, or X-Agent-Token on :8477.
---

# dispatcher-go agent skill

## What it is

`dispatcher-go` is the **only** process that talks to Termux:API. You call a
loopback HTTP API with a bearer-like header. There is **no** human confirm gate:
holding the token means full autonomy for every catalog verb. Every call is
audited on device.

Default base URL: `http://127.0.0.1:8477`  
Auth header: `X-Agent-Token: <token>`  
Token file (on phone): `$DISPATCHER_HOME/.agent-token` (often `~/dispatcher-go/.agent-token`)

If the agent machine reaches the phone via tailcat/port-forward, use that local
forward URL instead; auth is unchanged.

## Hard rules (token budget)

1. **Never** load `GET /v1/verbs?detail=full` into context unless debugging.
2. **Never** paste entire task `stdout`/`stderr` into reasoning when a compact
   status or `result` field is enough.
3. Discover with **search → get-one → post**, not dump-all-then-pick.
4. Secrets go in JSON `stdin`, never in argv-facing args. They must not reappear
   in task GET or audit lines (expect `[REDACTED]`).
5. Do not invent verb names. If search misses, try a broader query or
   `detail=names`, then stop—do not guess `termux-*` argv.

## Efficient workflow

```text
health  →  search verbs  →  GET one schema  →  POST verb  →  compact task GET
```

### 1. Confirm the daemon

```http
GET /v1/health
X-Agent-Token: …
```

Expect `"autonomy":"full"`. If connection refused, the daemon is down or the
port/forward is wrong—do not retry inventing verbs.

### 2. Find the verb (cheap)

```http
GET /v1/verbs/search?q=battery&limit=8
```

Response shape:

```json
{
  "query": "battery",
  "count": 1,
  "total": 76,
  "hits": [
    {"name": "battery.status", "tier": "A", "summary": "A · termux-battery-status", "score": 65, "stream": false}
  ]
}
```

Optional overview without argv bloat:

```http
GET /v1/verbs                 # detail=summary (default)
GET /v1/verbs?detail=names    # string list only
```

### 3. Load **one** full schema before calling

```http
GET /v1/verbs/sms.send
```

Use this for `args`, `stdin_arg`, `parser`, `timeout_s`, `watch`.  
Do not keep dozens of full schemas in context—drop them after the call.

### 4. Execute

```http
POST /v1/verbs/sms.send
Content-Type: application/json

{
  "args": {"number": "+15551234567"},
  "stdin": "message body for stdin_arg verbs",
  "idempotency_key": "optional-stable-uuid"
}
```

- Omit unknown fields (`DisallowUnknownFields`).
- Required args from the schema must be present.
- `202` body: `{"task_id","status"}` (often already `executed` when sync-exec is on; otherwise poll).

Streams (watch verbs only):

```http
POST /v1/streams   {"verb":"location.stream","args":{…}}
GET  /v1/streams/{id}?since=0
DELETE /v1/streams/{id}
```

Confirm `"stream": true` on search hits or `watch.mode=stream` on the schema.

### 5. Read results (compact)

```http
GET /v1/tasks/{id}
```

Default compact fields: `id`, `verb`, `state`, `attempt`, `exit_code`, `error`,
`result`, `last_attempt_outcome` — **no** large stdout.

When you need more:

| Need | Request |
|------|---------|
| Parsed JSON result only | default compact (use `result`) |
| Specific keys | `?fields=state,result,error` |
| Truncated logs | `?fields=state,stdout,stderr&max_stdout=512` |
| Raw task row | `?detail=full` (avoid in long chats) |

Terminal states you care about: `executed`, `failed`/`timeout` (may retry),
`retry_scheduled`, `exhausted`, `canceled`. Do not retry client-side forever when
`503 circuit_open` or `503 queue_full`—back off.

### 6. Idempotency

For side effects (SMS, call, clipboard set, …) pass a stable
`idempotency_key`. Replays return the original `task_id` with `"replay": true`.
Different payload under the same key → `409 idempotency_conflict`.

## Context hygiene checklist

- [ ] Used search (or names) instead of full catalog dump  
- [ ] Loaded at most 1–3 full verb schemas for the current step  
- [ ] POSTed only after schema check  
- [ ] Polled compact task GET; avoided `detail=full` unless necessary  
- [ ] Put secrets in `stdin` only  
- [ ] Recorded `task_id` for the user when an action mattered  

## Anti-patterns

| Bad | Good |
|-----|------|
| `GET /v1/verbs?detail=full` every turn | `search?q=` then `GET /v1/verbs/{name}` |
| Stuffing all 76 summaries into the system prompt | Search per subtask; keep a tiny working set |
| `GET /v1/tasks/id?detail=full` always | Compact default; fields/max_stdout when needed |
| Shelling out to `termux-*` on device | Always POST the named verb through dispatcher |
| Guessing args from memory | Re-read one schema; validation errors are authoritative |

## Minimal curl sketch

```bash
TOKEN=$(cat ~/dispatcher-go/.agent-token)
BASE=http://127.0.0.1:8477
H=(-H "X-Agent-Token: $TOKEN" -H "Content-Type: application/json")

curl -s "${H[@]}" "$BASE/v1/health"
curl -s "${H[@]}" "$BASE/v1/verbs/search?q=battery&limit=5"
curl -s "${H[@]}" "$BASE/v1/verbs/battery.status"
curl -s -X POST "${H[@]}" -d '{}' "$BASE/v1/verbs/battery.status"
# → task_id; then:
curl -s "${H[@]}" "$BASE/v1/tasks/<task_id>"
```

## When stuck

- `401` — wrong/missing token  
- `404 unknown_verb` — search again; catalog may have changed after restart  
- `400 validation_error` — re-fetch schema; fix args/stdin  
- `503 circuit_open` / `queue_full` — wait/backoff; check phone daemon health  
- Connection errors — daemon/port/forward; not a catalog problem  

Operator reference: `docs/OPERATOR_GUIDE.md`. Catalog source: `verbs.yaml` (restart required after YAML edits).
