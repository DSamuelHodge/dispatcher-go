# Operator guide (dispatcher-go)

Deep reference for running the daemon day-to-day. The [README](../README.md)
covers install, auth, and smoke tests.

## Approval model

`force_ask` verbs > per-verb setting > `~/.agent/approval-policy.json` >
daemon default. A global `always-approve` never silences `force_ask`
verbs (keystore sign/generate, `nfc.write`, ...). `/v1/health` reports
the full approval surface (`daemon_mode`, `policy_mode`,
`effective_global`, `force_ask_names`, per-verb counts) — there is no
single "effective mode for all verbs".

The policy file is one object; a missing file means "no override":

    {"approval_mode": "always-approve"}   # or "ask"; anything else fails startup

Point the daemon at it with `-policy-file ~/.agent/approval-policy.json`.
Use it for away/DND stretches, then delete or flip back to `"ask"` —
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

Repeat POSTs are safe: pass `"idempotency_key": "<uuid>"` and a replay
returns the original result (`replay: true`); a different payload under
the same key is rejected with `409 idempotency_conflict`.

## Flags

    -data-dir    base dir for .agent-token, logs/, data/ (default ".")
    -catalog     path to verbs.yaml (default ./verbs.yaml)
    -token-file  agent token path (default <data-dir>/.agent-token)
    -policy-file approval-policy.json path (default none)
    -audit-log   NDJSON audit path (default <data-dir>/logs/audit.log)
    -db          SQLite tasks db (default <data-dir>/data/tasks.db)
    -sync-exec   run first attempt inline after approval (debug; default: worker)
    -unattended  remote-agent full autonomy (see below; also DISPATCHER_UNATTENDED=1)
    -validate    load and check verbs.yaml, then exit
    -version     print version and exit

## Troubleshooting

- `connection refused` — daemon isn't running, or you're on the wrong
  port (re-check what setup.sh printed; another dispatcher may own 8477).
- `401 unauthorized` — wrong/missing `X-Agent-Token`; re-read
  `~/dispatcher-go/.agent-token` (no trailing newline issues: use
  `$(cat ...)` unquoted only inside the header string as shown).
- Confirm dialog never appears — device must be unlocked the first time;
  the Termux:API app must be installed or `termux-dialog` times out to
  deny after 120s.
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
