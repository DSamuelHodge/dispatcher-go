# Give your AI agent eyes, hands, ears, not keys.

Your phone can text, sense, locate, and notify. An AI agent wants those
abilities without raw shell access.

dispatcher-go is the single loopback gatekeeper between the agent and
Termux:API. The agent gets named verbs (`battery.status`, `sms.send`,
`location.once`...) and executes them with full autonomy once it holds
the token. Everything is written to an audit log. The model never holds
SMS, camera, or keystore permission — the gatekeeper does.

- **One thing touches Termux:API.** A loopback daemon (`127.0.0.1:8477`)
  is the only process allowed near it.
- **Typed verbs, not argv.** No shell, no raw `termux-*` strings from the
  model; new capabilities are YAML-only additions.
- **Full agent autonomy.** Token-authenticated agents execute any catalog
  verb without on-device confirm gates. Trust is the token + loopback bind.
- **Every attempt is on record.** NDJSON audit log plus a crash-safe
  SQLite outbox, with retry, circuit-breaking, and resume after reboot.

Single static binary, pure Go (no interpreter on-device).

## Install on device (Termux)

    curl -sL https://raw.githubusercontent.com/DSamuelHodge/dispatcher-go/main/setup.sh | bash

Installs the prebuilt `android-arm64` binary (release `v0.8.0`) into `~/dispatcher-go`,
picks a free loopback port from 8477 up, and installs the Termux:Boot
hook. Reboot once, or start by hand:

    dispatcher-go -data-dir ~/dispatcher-go -catalog ~/dispatcher-go/verbs.yaml

## Auth

Every route needs an `X-Agent-Token` header. The token is generated into
`~/dispatcher-go/.agent-token` (0600) on first start. Loopback isn't
private on Android, so the token is the real access control — share it
with the brain, rotate by deleting the file and restarting.

## Verb discovery

Do not load every verb schema into the agent context. Prefer
`GET /v1/verbs/search?q=…`, then `GET /v1/verbs/{name}`, then `POST`.
Default `GET /v1/verbs` is a short summary list; task GETs are compact.
Agent playbook: [`skills/dispatcher-go/SKILL.md`](skills/dispatcher-go/SKILL.md).

## Go deeper

- [`docs/OPERATOR_GUIDE.md`](docs/OPERATOR_GUIDE.md) — smoke test,
  adding verbs, tests, approval model, audit/durability, flags,
  troubleshooting, remote access via tailcat.
- [`spec.md`](spec.md), [`SRS.md`](SRS.md), [`docs/adr/`](docs/adr/) —
  spec, requirements, design records.
- [`SECURITY.md`](SECURITY.md), [`LICENSE`](LICENSE).
