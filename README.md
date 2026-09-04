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
  confirm; a policy file covers away/DND stretches.
- **Every attempt is on record.** NDJSON audit log plus a crash-safe
  SQLite outbox, with retry, circuit-breaking, and resume after reboot.

Single static binary, pure Go (no interpreter on-device).

## Install on device (Termux)

    curl -sL https://raw.githubusercontent.com/DSamuelHodge/dispatcher-go/main/setup.sh | bash

Installs the prebuilt `android-arm64` binary into `~/dispatcher-go`,
picks a free loopback port from 8477 up, and installs the Termux:Boot
hook. Reboot once, or start by hand:

    dispatcher-go -data-dir ~/dispatcher-go -catalog ~/dispatcher-go/verbs.yaml

## Auth

Every route needs an `X-Agent-Token` header. The token is generated into
`~/dispatcher-go/.agent-token` (0600) on first start. Loopback isn't
private on Android, so the token is the real access control — share it
with the brain, rotate by deleting the file and restarting.

## Smoke test

(Device unlocked the first time, so permission prompts can fire.)

    TOKEN=$(cat ~/dispatcher-go/.agent-token)
    PORT=8477  # or whichever free port setup.sh printed
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/health
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{}' http://127.0.0.1:$PORT/v1/verbs/battery.status
    curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:$PORT/v1/tasks/<task_id>

    # high-risk verb — pops a termux-dialog confirm on the phone (120s timeout denies)
    curl -X POST -H "X-Agent-Token: $TOKEN" -H 'Content-Type: application/json' \
      -d '{"args": {"number": "+15551234567"}, "stdin": "test"}' \
      http://127.0.0.1:$PORT/v1/verbs/sms.send

## Adding a verb

Edit `verbs.yaml` only. Each entry needs `argv` (a known `termux-*`
binary, no shell), `args` with `flag`/`type`/`required`, `tier`, `risk`,
and `approval`. Missing optional args are omitted flag-and-all; unknown
fields are rejected. Check with:

    go run ./cmd/dispatcher -catalog verbs.yaml -validate

Upstream reference: [docs/termux-api-reference.md](docs/termux-api-reference.md).

## Tests

    go test ./... -count=1 -p 2
    go vet ./...
    go run ./cmd/dispatcher -catalog verbs.yaml -validate

## Go deeper

- [`docs/OPERATOR_GUIDE.md`](docs/OPERATOR_GUIDE.md) — approval model,
  audit/durability, flags, troubleshooting, remote access via tailcat.
- [`spec.md`](spec.md), [`SRS.md`](SRS.md), [`docs/adr/`](docs/adr/) —
  spec, requirements, design records.
- [`SECURITY.md`](SECURITY.md), [`LICENSE`](LICENSE).
