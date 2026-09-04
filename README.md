# dispatcher-go — give an AI agent hands, not keys

Your phone can text, sense, locate, and notify. An AI "brain" wants to
use those abilities, but raw shell access is all-or-nothing: one bad
command and it reads your SMS or wipes storage with no record and no
chance to stop it.

dispatcher-go is the single gatekeeper between the brain and your phone.
The brain gets 76 safe, named verbs (`battery.status`, `sms.send`,
`location.once`...). Dangerous ones pause for an on-device yes/no tap.
Everything is written to an audit log. The model never holds SMS,
camera, or keystore permission — the gatekeeper does.

- **One thing touches Termux:API.** A loopback daemon (`127.0.0.1:8477`)
  is the only process allowed near it.
- **Typed verbs, not argv.** No shell, no raw `termux-*` strings from the
  model; new capabilities are YAML-only additions.
- **Humans approve danger.** High-risk verbs block on a `termux-dialog`
  confirm; a policy file covers away/DND stretches without silencing the
  scariest verbs.
- **Every attempt is on record.** NDJSON audit log plus a crash-safe
  SQLite outbox: accepted → approved/denied → result, with retry,
  circuit-breaking, and resume after reboot.

Single static binary, pure Go (no interpreter on-device).

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
    PORT=8477  # or whichever free port setup.sh printed
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/health
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/verbs
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{}' http://127.0.0.1:$PORT/v1/verbs/battery.status
    # poll the task to completion
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/tasks/<task_id>

    # Tier B -- start a subscription, poll it, stop it
    ID=$(curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"verb": "sensor.stream", "args": {"name": "accelerometer"}}' \
      http://127.0.0.1:$PORT/v1/streams | grep -o '"stream_id":"[^"]*"' | cut -d'"' -f4)
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/streams/$ID?since=
    curl -X DELETE -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/streams/$ID

    # high-risk verb -- this will pop a termux-dialog confirm on the phone
    # and block until you tap yes/no (120s timeout denies)
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"args": {"number": "+15551234567"}, "stdin": "test"}' \
      http://127.0.0.1:$PORT/v1/verbs/sms.send

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

The policy file is one object; a missing file means "no override":

    {"approval_mode": "always-approve"}   # or "ask"; anything else fails startup

Point the daemon at it with `-policy-file ~/.agent/approval-policy.json`.
Use it for away/DND stretches, then delete or flip back to `"ask"` --
`force_ask` verbs still prompt either way.

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

## Flags

    -data-dir    base dir for .agent-token, logs/, data/ (default ".")
    -catalog     path to verbs.yaml (default ./verbs.yaml)
    -token-file  agent token path (default <data-dir>/.agent-token)
    -policy-file approval-policy.json path (default none)
    -audit-log   NDJSON audit path (default <data-dir>/logs/audit.log)
    -db          SQLite tasks db (default <data-dir>/data/tasks.db)
    -sync-exec   run first attempt inline after approval (debug; default: worker)
    -validate    load and check verbs.yaml, then exit
    -version     print version and exit

## Troubleshooting

- `connection refused` -- daemon isn't running, or you're on the wrong
  port (re-check what setup.sh printed; another dispatcher may own 8477).
- `401 unauthorized` -- wrong/missing `X-Agent-Token`; re-read
  `~/dispatcher-go/.agent-token` (no trailing newline issues: use
  `$(cat ...)` unquoted only inside the header string as shown).
- Confirm dialog never appears -- device must be unlocked the first time;
  the Termux:API app must be installed or `termux-dialog` times out to
  deny after 120s.
- Verb fails with `executable file not found in $PATH` -- the `termux-api`
  package isn't installed (`pkg install termux-api`) or the Termux:API
  app is missing.
- `503 queue_full` -- worker isn't draining (check `logs/daemon.stderr`);
  `503 circuit_open` -- verb tripped its breaker after repeated failures,
  resolves after the cooldown or a restart.
- Boot hook doesn't fire -- the Termux:Boot *app* must be installed and
  the device rebooted once after placing `~/.termux/boot/01-start-agent`.

## Remote access: give a remote agent the phone (tailcat)

On-device daemons serve phones; remote agents need hands and eyes. The
transport is [tailcat](https://github.com/tailscale/tailcat) — WireGuard
encryption + NAT traversal with no accounts, no tailnet, no root, all
userspace (perfect for unrooted Termux). The phone serves its loopback
dispatcher port; the agent forwards it locally. `dispatcher-go` itself
doesn't change: loopback bind, token auth, and audit all stay as-is.

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

**Full autonomy posture.** Remote agents get no dialog taps, so the
phone runs global `always-approve` (`{"approval_mode":
"always-approve"}` policy file) **plus** `-unattended`
(`DISPATCHER_UNATTENDED=1` also works). Either alone changes nothing
for high-risk verbs; together, per-verb `ask` and `force_ask` gates are
overridden to approve. Every bypassed approval audits
`unattended:true`, and `/v1/health` reports
`approval.unattended`/`unattended_high_risk` — check it remotely to
confirm the posture you think is running. Without `-unattended`, gated
verbs time out to denied when no human is present.

**Caveats.** tailcat has no CLI/wire stability promises — the release
pins v0.5.0. Public DERP relays are rate-limited and metadata-logged
(payload stays WireGuard-encrypted); self-host `derper` later if
throughput or privacy demands it. Keep `termux-wake-lock` on and expect
tunnel flaps on network switches — the agent must treat dial failures
as "phone unreachable" and retry.

Spec, requirements, and design records: [`spec.md`](spec.md),
[`SRS.md`](SRS.md), [`docs/adr/`](docs/adr/),
[`SECURITY.md`](SECURITY.md), [`LICENSE`](LICENSE).
