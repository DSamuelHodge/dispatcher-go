# Give your AI agent eyes, hands, ears, not keys.

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

## Go deeper

- [`docs/OPERATOR_GUIDE.md`](docs/OPERATOR_GUIDE.md) — smoke test,
  adding verbs, tests, approval model, audit/durability, flags,
  troubleshooting, remote access via tailcat.
- [`spec.md`](spec.md), [`SRS.md`](SRS.md), [`docs/adr/`](docs/adr/) —
  spec, requirements, design records.
- [`SECURITY.md`](SECURITY.md), [`LICENSE`](LICENSE).
