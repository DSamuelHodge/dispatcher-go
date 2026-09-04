# Security Policy

## Supported versions

This project is pre-1.0. Security fixes land on `main` only.

## What this software can do

`dispatcher-go` mediates access to **Termux:API** device capabilities (SMS, telephony, clipboard, camera, microphone, keystore, location, etc.) for a local AI agent. A compromised token or malicious verb catalog is equivalent to full device-capability access for every catalog verb.

## Threat model (summary)

- Bind **127.0.0.1 only** — do not expose via SSH reverse tunnels, ngrok, or LAN.
- **Token** in `.agent-token` mode `0600`; treat like a password.
- **Token = full autonomy** for every catalog verb (`sms.send`, `telephony.call`, keystore, mic, …). There is no on-device confirm gate.
- Secrets use `stdin` piping and must appear as `[REDACTED]` in audit, task GET, and SQLite `argv_redacted`.
- No shell interpolation of argv.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for vulnerabilities that could lead to:

- unauthorized SMS/call/clipboard/keystore access
- token theft or audit bypass
- escape from loopback binding assumptions

Instead, email the repository owner listed on the GitHub profile for [DSamuelHodge/dispatcher-go](https://github.com/DSamuelHodge/dispatcher-go), or use GitHub **Private vulnerability reporting** if enabled on the repo.

Include:

1. Affected commit / version
2. Impact (which verbs / data)
3. Reproduction steps (on-device or with fake PATH shim)
4. Any known mitigations

You should receive an acknowledgement within 7 days. We ask for reasonable time before public disclosure after a fix is available.

## Hardening checklist for operators

- Treat `.agent-token` as full device control; never copy it off-device casually
- Rotate the token (delete file + restart) after any suspected leak
- Review `verbs.yaml` changes like code review
- Monitor `logs/audit.log` / outbox for unexpected high-risk verbs
- Do not run the binary as a shared multi-user service
