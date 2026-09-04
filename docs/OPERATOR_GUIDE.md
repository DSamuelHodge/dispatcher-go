# Operator guide (dispatcher-go)

Deep reference for running the daemon day-to-day. The [README](../README.md)
covers install and auth.

## Smoke test

From another Termux session (device unlocked the first time, so
permission prompts can fire):

    TOKEN=$(cat ~/dispatcher-go/.agent-token)
    PORT=8477  # or whichever free port setup.sh printed
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/health
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/verbs
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{}' http://127.0.0.1:$PORT/v1/verbs/battery.status
    # poll the task to completion
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/tasks/<task_id>

    # Tier B — start a subscription, poll it, stop it
    ID=$(curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"verb": "sensor.stream", "args": {"name": "accelerometer"}}' \
      http://127.0.0.1:$PORT/v1/streams | grep -o '"stream_id":"[^"]*"' | cut -d'"' -f4)
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/streams/$ID?since=
    curl -X DELETE -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/streams/$ID

    # mutating verb — executes immediately under full autonomy
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"args": {"number": "+15551234567"}, "stdin": "test"}' \
      http://127.0.0.1:$PORT/v1/verbs/sms.send

## Adding a verb

Edit `verbs.yaml` only — no code changes needed for any Tier A/B command
(including ones whose official script reads the payload from stdin: set
`stdin_arg: {arg: <name>}` and the dispatcher pipes the value, redacting
it in the audit log / task GET).

Each entry needs `argv` (must start with a known `termux-*` binary, no
shell), `args` with `flag`/`type`/`required`, and `tier` (`A`/`B`).
Missing optional args are omitted flag-and-all, never passed as empty
strings. Unknown YAML fields are rejected, so typos fail fast — check with:

    go run ./cmd/dispatcher -catalog verbs.yaml -validate

The classified upstream surface is in
[termux-api-reference.md](termux-api-reference.md).
`verbs.yaml` is what the dispatcher loads. Copy a reference row into the
YAML with its real argv template and it is live on next daemon restart.

## Tests

    go test ./... -count=1 -p 2
    go vet ./...
    go run ./cmd/dispatcher -catalog verbs.yaml -validate

## Verb discovery (token-efficient)

Agents must not dump the full catalog into context every turn.

- `GET /v1/verbs` — **summary** by default (`name`, `tier`, one-line `summary`)
- `GET /v1/verbs?detail=names` — names only
- `GET /v1/verbs?detail=full` — full dump (operators/debug only)
- `GET /v1/verbs/search?q=sms&limit=8` — ranked search
- `GET /v1/verbs/{name}` — full schema for one verb before `POST`
- `GET /v1/tasks/{id}` — **compact** by default; `?detail=full` for the raw row;
  `?fields=state,result,stdout&max_stdout=512` to select/truncate

See `skills/dispatcher-go/SKILL.md` for the agent playbook.

## Autonomy model

There is no ask/always-approve/unattended switch. After token auth and
catalog validation, verbs run. Secrets still use `stdin` piping and
appear as `[REDACTED]` in audit, task GET, and argv redaction.


## Audit log and durability

Every call attempt is appended to `logs/audit.log` as newline-delimited
JSON, written before execution so a crash still leaves a record of
intent. The same lifecycle is committed atomically to `data/tasks.db`
(task row + audit outbox row in one transaction), SQLite WAL +
`synchronous=FULL`, verified read-write at startup (fail-closed).

Lifecycle per call: `accepted`, then `executing` → result (or `pending`
when the worker owns first exec). Failures retry with capped exponential
backoff, repeated failures trip the per-verb circuit breaker, and
exhaustion fires a `termux-notification`. A full queue rejects with
`503 queue_full` (check + insert are atomic, so floods can't overfill).
On restart the daemon resumes: `executing` → `pending`, stale `accepted`
→ `canceled`, both audited.

Repeat POSTs are safe: pass `"idempotency_key": "<uuid>"` and a replay
returns the original result (`replay: true`); a different payload under
the same key is rejected with `409 idempotency_conflict`.

## Flags

    -data-dir    base dir for .agent-token, logs/, data/ (default ".")
    -catalog     path to verbs.yaml (default ./verbs.yaml)
    -token-file  agent token path (default <data-dir>/.agent-token)
    -audit-log   NDJSON audit path (default <data-dir>/logs/audit.log)
    -db          SQLite tasks db (default <data-dir>/data/tasks.db)
    -sync-exec   run first attempt inline (debug; default: worker)
    -validate    load and check verbs.yaml, then exit
    -version     print version and exit

## Troubleshooting

- `connection refused` — daemon isn't running, or you're on the wrong
  port (re-check what setup.sh printed; another dispatcher may own 8477).
- `401 unauthorized` — wrong/missing `X-Agent-Token`; re-read
  `~/dispatcher-go/.agent-token` (no trailing newline issues: use
  `$(cat ...)` unquoted only inside the header string as shown).
- Verb fails with `executable file not found in $PATH` — the `termux-api`
  package isn't installed (`pkg install termux-api`) or the Termux:API
  app is missing.
- `503 queue_full` — worker isn't draining (check `logs/daemon.stderr`);
  `503 circuit_open` — verb tripped its breaker after repeated failures,
  resolves after the cooldown or a restart.
- Boot hook doesn't fire — the Termux:Boot *app* must be installed and
  the device rebooted once after placing `~/.termux/boot/01-start-agent`.

## Remote access: give a remote agent the phone (tailcat)

The transport is [tailcat](https://github.com/tailscale/tailcat) —
WireGuard encryption + NAT traversal with no accounts, no tailnet, no
root, all userspace (perfect for unrooted Termux). The phone serves its
loopback dispatcher port; the agent forwards it locally.
`dispatcher-go` itself doesn't change: loopback bind, token auth, and
audit all stay as-is.

```
phone (Termux)                              agent machine
dispatcher 127.0.0.1:8477 ◄── tailcat serve ──WireGuard/DERP──► tailcat forward ──► 127.0.0.1:18477
   (unchanged)          ▲                                                        │
              --allow=<agent key>                                      curl + X-Agent-Token
```

Three independent credentials: the `tc...` address (capability), the
client-key allowlist (the server silently ignores anyone else), and the
dispatcher token. Rotate any one alone.

**Phone (Termux):**

    INSTALL_TAILCAT=1 bash setup.sh   # or re-run with the env var to add tailcat later
    tailcat genkey --key=dispatcher --fixed-region   # prints tc... address (stable)
    # agent side: tailcat genkey --client --key=agent-phone-1  (prints nodekey:...)
    echo 'nodekey:PASTE-AGENT-KEY' > ~/dispatcher-go/.tailcat-allow
    sh ~/dispatcher-go/scripts/tailcat-serve.sh      # supervised; address printed on start
    cp scripts/02-start-tailcat ~/.termux/boot/     # serve across reboots (needs Termux:Boot app)

Without `.tailcat-allow` (or `TAILCAT_ALLOW`) the serve script refuses
to run — it never serves to bare address-holders.

**Agent:**

    tailcat forward tcPASTE-ADDRESS 18477:8477 &
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:18477/v1/health

`scripts/tailcat-forward.sh` wraps this with a `ping --until-direct`
pre-check and reconnect backoff: `tailcat-forward.sh <tc-addr>`.

**Autonomy.** The daemon executes every catalog verb without a human
confirm gate. Possession of `X-Agent-Token` is full device-capability
access for verbs in `verbs.yaml`. `/v1/health` reports `"autonomy":"full"`.

**Caveats.** tailcat has no CLI/wire stability promises — the release
pins v0.5.0. Public DERP relays are rate-limited and metadata-logged
(payload stays WireGuard-encrypted); self-host `derper` later if
throughput or privacy demands it. Keep `termux-wake-lock` on and expect
tunnel flaps on network switches — the agent must treat dial failures
as "phone unreachable" and retry.
