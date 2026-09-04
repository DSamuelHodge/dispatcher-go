# On-device agent dispatcher — Go (Termux:API)

A loopback HTTP process that runs in Termux and is the only thing on the
phone allowed to touch Termux:API. A separate "brain" (local script or
remote model) calls typed verbs -- `battery.status`, `sms.send`,
`location.once`, `sensor.stream`, ... -- instead of raw `termux-*` argv.
The catalog carries tier, risk, approval, and an optional stdin hook;
high-risk verbs stop on an on-device confirm dialog; every attempt is
appended to `logs/audit.log` and committed to a SQLite outbox. This is
the device body. The model does not hold SMS, camera, or keystore
permission.

Single static binary, pure Go (SQLite via `modernc.org/sqlite`, no CGO),
76 verbs, no interpreter on-device.

## Install on device (Termux)

From a Termux session on the phone:

    curl -sL https://raw.githubusercontent.com/DSamuelHodge/dispatcher-go/main/setup.sh | bash

That downloads the prebuilt `android-arm64` binary (release `v0.7.0`;
falls back to building from source), copies `verbs.yaml` into
`~/dispatcher-go`, picks the first free loopback port starting at 8477
(so it never conflicts with another dispatcher), and installs
`~/.termux/boot/01-start-agent`. Reboot once so Termux:Boot picks it up,
or start it by hand:

    dispatcher-go -data-dir ~/dispatcher-go -catalog ~/dispatcher-go/verbs.yaml

## Auth

Every route requires an `X-Agent-Token` header. The token is generated
once into `~/dispatcher-go/.agent-token` (chmod 600) on first start, and
startup refuses to run if the file is group/world-readable.
Loopback-only is not private on Android -- any app can dial 127.0.0.1 --
so this token is the actual access control. Give it to the brain with
`cat ~/dispatcher-go/.agent-token`; rotate by deleting the file and
restarting the daemon. Comparison is constant-time.

## Manual smoke test (from another Termux session, device must be unlocked
## the first time so Android's permission prompts can fire)

    TOKEN=$(cat ~/dispatcher-go/.agent-token)
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:8477/v1/health
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:8477/v1/verbs
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{}' http://127.0.0.1:8477/v1/verbs/battery.status
    # poll the task to completion
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:8477/v1/tasks/<task_id>

    # Tier B -- start a subscription, poll it, stop it
    ID=$(curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"verb": "sensor.stream", "args": {"name": "accelerometer"}}' \
      http://127.0.0.1:8477/v1/streams | python -c 'import sys,json;print(json.load(sys.stdin)["stream_id"])')
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:8477/v1/streams/$ID?since=
    curl -X DELETE -H "X-Agent-Token: $TOKEN" http://127.0.0.1:8477/v1/streams/$ID

    # high-risk verb -- this will pop a termux-dialog confirm on the phone
    # and block until you tap yes/no (120s timeout denies)
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"args": {"number": "+15551234567"}, "stdin": "test"}' \
      http://127.0.0.1:8477/v1/verbs/sms.send

Repeat POSTs are safe: pass `"idempotency_key": "<uuid>"` and a replay
returns the original result (`replay: true`); a different payload under
the same key is rejected with `409 idempotency_conflict`.

## Adding a verb

Edit `verbs.yaml` only -- no code changes needed for any Tier A/B command
(including ones whose official script reads the payload from stdin: set
`stdin_arg: {arg: <name>}` and the dispatcher pipes the value, redacting
it in the audit log / confirm dialog).

Each entry needs `argv` (must start with a known `termux-*` binary, no
shell), `args` with `flag`/`type`/`required`, `tier`, `risk`, and
`approval` (`ask` / `always-approve` / `inherit`). Missing optional args
are omitted flag-and-all, never passed as empty strings. Unknown YAML
fields are rejected, so typos fail fast -- check with:

    go run ./cmd/dispatcher -catalog verbs.yaml -validate

The classified upstream surface is in
[docs/termux-api-reference.md](docs/termux-api-reference.md).
`verbs.yaml` is what the dispatcher loads. Copy a reference row into the
YAML with its real argv template and it is live everywhere (routing,
approval gating, execution) on next daemon restart.

## Approval model

`force_ask` verbs > per-verb setting > `~/.agent/approval-policy.json` >
daemon default. A global `always-approve` never silences `force_ask`
verbs (keystore sign/generate, `nfc.write`, ...). `/v1/health` reports
the full approval surface (`daemon_mode`, `policy_mode`,
`effective_global`, `force_ask_names`, per-verb counts) -- there is no
single "effective mode for all verbs".

## Audit log and durability

Every call attempt is appended to `logs/audit.log` as newline-delimited
JSON, written before execution so a crash still leaves a record of
intent. The same lifecycle is committed atomically to `data/tasks.db`
(task row + audit outbox row in one transaction), SQLite WAL +
`synchronous=FULL`, verified read-write at startup (fail-closed).

Lifecycle per call: `accepted`, then `approved`/`denied` for gated verbs,
then `executing` → result; failures retry with capped exponential
backoff, repeated failures trip the per-verb circuit breaker, and
exhaustion fires a `termux-notification`. A full queue rejects with
`503 queue_full` (check + insert are atomic, so floods can't overfill).
On restart the daemon resumes: `executing` → `pending`, stale `accepted`
→ `canceled`, both audited.

## Tests

    go test ./... -count=1 -p 2
    go vet ./...
    go run ./cmd/dispatcher -catalog verbs.yaml -validate

Spec, requirements, and design records: [`spec.md`](spec.md),
[`SRS.md`](SRS.md), [`docs/adr/`](docs/adr/).
