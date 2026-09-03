# dispatcher-go — Termux:API Loopback Daemon Specification

> Version: 1.0 (MVP) · Date: 2026-09-03 · Target: `127.0.0.1:8477`
> Sources verified live: `termux/termux-api` app v0.53.0 (`app/src/main/java/com/termux/api/apis/`),
> `termux/termux-api-package` (`scripts/termux-*.in`, incl. `termux-dialog.in`, `termux-notification.in`).

## 1. Overview

A loopback HTTP daemon (`127.0.0.1:8477`) serving as the **exclusive interface** to Termux:API.
All verbs are defined declaratively in a YAML catalog (`verbs.yaml`):

- **Tier A** — read-only perceive.
- **Tier B** — one-shot act + stateful watch subscriptions.
- (Tier C UI automation — `ui.tap/type/gesture/screen.read` — is an explicit **non-goal** for MVP;
  it requires an AccessibilityService companion app and violates the `termux-*` argv rule.)

No code change is needed to add verbs — only edit YAML and **restart** the daemon
(MVP does not hot-reload; ADR-0004).

## 2. Locked decisions

- **Language:** Go, stdlib-first (`net/http`, `os/exec`, `database/sql`).
  Allowed direct deps: `gopkg.in/yaml.v3` + `modernc.org/sqlite` (pure Go; ADR-0001, ADR-0006).
  Single static binary for Termux (no CGO).
- **Approval:** global `approval_mode` (`ask` | `always-approve`) **+** per-verb override;
  highest-risk verbs set `force_ask_even_if_global_auto: true`.
- **Watch subscriptions:** only true-streaming verbs (`location`, `sensor`, `microphone-record`-style
  continuous, `sms-inbox` follow) with a bounded ring buffer. No generic poll-wrapper.
- **Failure policy:** exponential backoff ×5 (base 1s ×2 + jitter ≤250ms), circuit-breaker trips
  after 5 consecutive fail/timeout with half-open probe.

## 3. Architecture

```text
agent ──HTTP 127.0.0.1:8477 + X-Agent-Token──▶ dispatcher-go
        verbs.yaml (declarative, load on start; SIGHUP/restart to reload — no code change)
          ├─ auth middleware → exec engine (argv template + stdin piping, NO shell)
          ├─ approval gate (termux-dialog confirm | auto in always-approve)
          ├─ audit logger (logs/audit.log NDJSON) + SQLite WAL queue (data/tasks.db)
          ├─ retry worker (exp backoff) + circuit breaker + exhaustion notifier (termux-notification)
          └─ stream registry (Tier B watch: POST → GET poll → DELETE)
```

File layout:

```text
cmd/dispatcher/main.go
internal/config/ verbs/ termuxallow/ auth/ approve/ execx/ audit/
internal/queue/ retry/ circuit/ streams/ api/
verbs.yaml
docs/adr/
.agent-token            # chmod 0600, generated on first boot (not committed)
logs/audit.log          # NDJSON lifecycle log (outbox-drained; ADR-0002)
data/tasks.db           # SQLite WAL + synchronous=FULL (modernc.org/sqlite)
~/.agent/approval-policy.json   # runtime ask|always-approve override
~/.termux/boot/01-start-agent   # boot lifecycle hook
```

## 4. `verbs.yaml` schema (normative)

```yaml
version: 1
daemon:
  listen: "127.0.0.1:8477"
  approval_mode: ask | always-approve   # global default
  approval_backend: dialog | float       # dialog = termux-dialog confirm (MVP); float = post-MVP alt
  task_timeout_s: 30
  max_retries: 5                        # total attempts = 1 + 5 retries
  backoff_base_s: 1                     # delay = base*2^n + jitter(<=250ms)
  cb_trip_threshold: 5                  # consecutive timeout/fail to open circuit
  cb_open_s: 60
verbs:
  - name: battery.status                # HTTP: POST /v1/verbs/battery.status
    tier: A                             # A = perceive read-only; B = act | watch
    risk: none | low | medium | high
    approval: inherit | ask | always-approve
    force_ask_even_if_global_auto: false
    argv: ["termux-battery-status"]     # template; {{.arg}} substitution, exec directly, no shell
    args:
      - {name: limit, flag: -l, type: int, required: false}
    stdin_arg: null                     # or {arg: text} → piped via stdin, redacted everywhere
    timeout_s: 15
    retries: null                       # null = inherit daemon default
    retry_backoff: exponential
    circuit_breaker_threshold: null     # null = inherit daemon default
    parser: json | text | exit           # json = parse stdout as JSON; text = raw; exit = exit-code only
    watch: false                        # or {mode: stream, buffer: 128}
```

Rules:

- `argv[0]` MUST be a `termux-*` binary from `termux-api-package/scripts/`; flags copied verbatim
  from each `.in` script. Non-`termux-*` adapters (Tasker plugin etc.) are post-MVP via a future
  `adapter:` field — not MVP.
- Tier A verbs MUST NOT contain mutating argv (validated at load).
- `approval: inherit` resolves to the effective global mode from `daemon.approval_mode` merged with
  `~/.agent/approval-policy.json` (file wins; per-verb `ask`/`always-approve` wins over both;
  `force_ask_even_if_global_auto: true` wins over everything).
- `stdin_arg` names the request field piped to the child stdin pipe, never placed in argv,
  and rendered as `"[REDACTED]"` in audit log, confirm dialog, task GET, and SQLite `argv_redacted`.
- Per-verb `retries` / `circuit_breaker_threshold` override daemon defaults when set.

## 5. Tier catalog (seed set; exact flags transcribed from each `.in` script at build)

### 5.1 Tier A — perceive (read-only; `risk: none|low|medium`, default `approval: inherit`)

- `battery.status` → `termux-battery-status` (level, status, health, temperature, plugged).
- `camera.info` → `termux-camera-info`; `audio.info` → `termux-audio-info`.
- `wifi.info` → `termux-wifi-connectioninfo`; `wifi.scan-one-shot` → `termux-wifi-scaninfo`.
  (Accept both `connectioninfo` and `connection-info` spellings; they alias one verb.)
- `telephony.cell` → `termux-telephony-cellinfo`; `telephony.device` → `termux-telephony-deviceinfo`.
  (There is NO `termux-telephony-signalstrength` binary — do not catalog it.)
- `clipboard.get` → `termux-clipboard-get`; `contacts.list` → `termux-contact-list`;
  `call-log.read` → `termux-call-log [-l N]`; `sms.read` → `termux-sms-list […]`
  (`termux-sms-inbox` kept as legacy alias).
- `sensor.list` → `termux-sensor -l`; `location.once` → `termux-location -p gps|network -r once|last`.
- `volume.info` → `termux-volume` (query form); `tts.engines` → `termux-tts-engines`.
- `notification.list` → `termux-notification-list`; `infrared.frequencies` → `termux-infrared-frequencies`.
- `saf.ls` / `saf.stat` / `saf.dirs` → `termux-saf-ls|stat|dirs`; `nfc.list` / `usb.list` (list forms).

### 5.2 Tier B one-shot act (`risk: medium|high`, approval gate applies)

- `clipboard.set` → `termux-clipboard-set` (`stdin:true`).
- `sms.send` → `termux-sms-send -n "<number>" "<text>"` — HIGH, `force_ask: true`, `stdin:true`
  for text, timeout 30s. (NOT `termux-sms send -n … -m …`.)
- `telephony.call` → `termux-telephony-call <number>` — HIGH, `force_ask: true`.
- `camera.photo` → `termux-camera-photo -c 0|1 <output-file>`.
- `tts.speak` → `termux-tts-speak "<text>" [--engine … --language … --pitch … --rate …]`.
- `media.play` → `termux-media-player play -f <file>` (also `pause|stop|info`); `media.scan` → `termux-media-scan`.
- `toast.show` → `termux-toast ["<text>"]` (low); `notification.send` → `termux-notification -t … -c … [--id/--priority/--sound/--ongoing/…]`.
- `notification.remove` → `termux-notification-remove`; `notification.channel` → `termux-notification-channel`.
- `vibrate` → `termux-vibrate [-d ms]`; `torch` → `termux-torch on|off`; `brightness.set` → `termux-brightness <0-255>`.
- `volume.set` → `termux-volume <stream> <level>`; `wallpaper.set` → `termux-wallpaper -f <file>`.
- `share` → `termux-share […]`; `open.url` → `termux-open <url>`; `download.file` → `termux-download [-d desc -p path -t title] <URL>`.
- `saf.create/mkdir/write/rm/managedir` → `termux-saf-*`; `storage.get` → `termux-storage-get`.
- `fingerprint.auth` → `termux-fingerprint`; `keystore.*` → `termux-keystore …` — HIGH, `force_ask: true`, `stdin:true`.
- `infrared.transmit` → `termux-infrared-transmit …`; `job-scheduler.*` → `termux-job-scheduler …`.
- `speech-to-text` → `termux-speech-to-text […]`; `dialog.*` → `termux-dialog <widget> […]`
  (widgets: `confirm|checkbox|counter|date|radio|sheet|spinner|speech|text|time`;
  flags `-i/-m/-p/-n/-t/-d/-v/-r` per widget; the approval primitive itself — NEVER gated).
- `wake-lock` / `wake-unlock` are Termux core-package binaries, not Termux:API — allowlisted
  passthrough only, or omitted from the API catalog.
- Dropped (no such API binary): `sms.open`, `screen.wake`, `display.state`, `termux-brightness-set`,
  `termux-wallpaper-set`, `termux-media-play`, `termux-url-opener`, `termux-telephony-signalstrength`.

### 5.3 Tier B watch (stateful subscriptions; bounded ring buffer, default 128)

MVP watch verbs (ADR-0005):

- `location.stream` → `termux-location -p gps|network -r updates` (held proc; HIGH battery cost — document).
- `sensor.stream` → `termux-sensor -s <name> [-n <count>] [-d <delay-ms>]` / `-a` (all).

Not watch streams in MVP:

- `microphone.record` → **one-shot task** via `termux-microphone-record -f <file> [-l seconds]` (bounded capture).
- `sms-inbox.follow` → **deferred** (no upstream follow; poll-wrapper is a non-goal per §2).

Lifecycle (watch only): `POST /v1/streams {verb, args}` → `{stream_id}`; `GET /v1/streams/{id}?since=` (ring buffer);
`DELETE /v1/streams/{id}` kills the child proc and frees the buffer.

## 6. HTTP surface (loopback only)

- `POST /v1/verbs/{name}` `{args, stdin?, idempotency_key?}` → `202 {task_id, status}`.
  Gated Tier B verbs may first return `pending_approval`.
- `GET /v1/tasks/{id}` (status + redacted argv + truncated stdout/stderr).
- `GET /v1/tasks?state=pending|retry_scheduled|exhausted`.
- `POST /v1/streams`, `GET /v1/streams/{id}`, `DELETE /v1/streams/{id}`.
- `GET /v1/verbs` (catalog dump), `GET /v1/health` (queue depth, CB state, effective approval mode).
- Bind `127.0.0.1` only; refuse `0.0.0.0`. No TLS (loopback). SSH/ngrok remote exposure is REJECTED
  (breaks threat model; debugging over adb / on-device shell only).
- Every request requires `X-Agent-Token` (constant-time compare); `401` otherwise.

## 7. Auth, approval, redaction

- Token file `.agent-token`, `chmod 0600`, generated on first boot.
- Effective approval resolution order: `force_ask` > per-verb setting > `~/.agent/approval-policy.json`
  > `daemon.approval_mode`.
- `ask` mode for gated verbs: daemon runs `termux-dialog confirm -t "Approve <verb>?"` with REDACTED
  args, blocks up to 120s; `yes` → approved, anything else/timeout → denied (denied NEVER retries).
- `always-approve` mode: auto-executes (explicitly user-enabled; supports away/DND workflows),
  logs `approved{by:"policy"}`.
- `stdin:true` args travel request-body → child-stdin pipe only; `"[REDACTED]"` in audit log,
  confirm text, task GET, and SQLite.

## 8. Audit log (NDJSON lifecycle)

- Path `logs/audit.log`, one JSON object per line per transition:
  `requested → approved|denied → executing → executed|timeout|failed|will-retry|exhausted`.
- Fields: `{ts, task_id, verb, tier, risk, approval, argv_redacted, exit_code, latency_ms, attempt, error}`.
- `fsync` per line. No secrets ever written.

## 9. Durability: SQLite + retry + circuit-breaker + resume

- Driver: `modernc.org/sqlite` (ADR-0001). Path `data/tasks.db`, `journal_mode=WAL`, `synchronous=FULL`.
- Timestamps: UTC `RFC3339Nano` text columns for deterministic ordering.
- Tables:
  - `tasks(id, verb, args_json, argv_redacted, state, attempt, next_run_at, created_at, updated_at)`
  - `attempts(task_id, n, started_at, ended_at, exit_code, error)`
  - `streams(id, verb, pid, buf_path, created_at)`
  - `audit_outbox(id, ts, payload_json, written_at)` — ADR-0002
  - `idempotency_keys(key, verb, request_hash, task_id, created_at)` — ADR-0003 (TTL 24h)
- **Atomicity:** task state + outbox row commit in one SQLite transaction; NDJSON file is drained
  asynchronously with fsync (at-least-once). SQLite is source of truth.
- Retry ONLY on terminal attempt outcomes `timeout|failed` (including nonzero exit). Never on
  `denied`/`canceled`. Attempt index `n` runs `0..max_retries` inclusive (default max_retries=5 ⇒
  6 attempts). After a failed attempt with index `n` where `n < max_retries`, schedule
  `retry_scheduled` with delay `backoff_base_s * 2^n + jitter(≤250ms)` (`n` = index of the
  attempt that just failed). When attempt `max_retries` fails → `exhausted`.
- On exhaustion: state `exhausted` + `termux-notification --title "dispatcher: <verb> exhausted"
  -c "task <id> after 6 attempts"` (allowlisted to bypass an open circuit).
- Circuit-breaker per verb-template: 5 consecutive timeout/fail → `open` 60s (fast `503 circuit_open`),
  single half-open probe, close on success.
- Crash resume on boot: `SELECT … WHERE state IN ('pending','executing','retry_scheduled')
  ORDER BY created_at` — `executing` rows from a dead PID are requeued as `pending`
  (never marked executed without a waitpid result); due `retry_scheduled` rows resume in order.
- Queue-full: when pending depth ≥ `daemon.max_queue_depth` → `503` `{code:"queue_full"}`.

## 9.1 Task state machine (normative)

Single source of truth for task lifecycle. HTTP `status` fields and audit transitions MUST use
these state names only.

```text
                     ┌──────────────────────────────────────────────┐
                     │                                              │
  POST verb ──▶ accepted ──▶ pending_approval ──▶ approved ──▶ pending
                     │              │                                  │
                     │              └──── denied (terminal)             │
                     │              └──── canceled (terminal)           │
                     │                                                 ▼
                     └──────── (no gate) ──────────────────────▶ executing
                                                                   │
                    ┌──────────────────────────────────────────────┤
                    ▼              ▼              ▼                 ▼
               executed         timeout        failed          (kill/crash)
               (terminal)         │              │              executing
                                  └──────┬───────┘                 │
                                         ▼                         ▼
                                 retry_scheduled ──(due)──▶ pending
                                         │
                                         └─(attempt n==max_retries failed)──▶ exhausted (terminal)
```

### States

| State | Meaning |
|-------|---------|
| `accepted` | Request authenticated & validated; task row created; not yet gated/executed |
| `pending_approval` | Waiting on approval backend (`ask`); HTTP may return 202 with this status |
| `approved` | Gate passed (`ask` yes or policy auto); eligible to run |
| `denied` | Terminal — user/policy denied or approval timeout; **never retries** |
| `canceled` | Terminal — explicit cancel; **never retries** |
| `pending` | Runnable, waiting for worker slot |
| `executing` | Child process started; not terminal until waitpid/timeout |
| `executed` | Terminal success (exit 0, or parser-defined success) |
| `timeout` | Attempt timed out (retryable if attempts remain) |
| `failed` | Attempt failed / nonzero exit (retryable if attempts remain) |
| `retry_scheduled` | Waiting until `next_run_at` |
| `exhausted` | Terminal — `max_retries` exhausted after retryable failures |

Notes:

- Gated verbs enter `pending_approval` before `approved`/`denied`. Ungated verbs may skip
  `pending_approval` and go `accepted → pending` (still audit an `approved{by:"policy"}` when
  auto policy applies).
- `timeout`/`failed` are **attempt outcomes** recorded on `attempts` and in audit; the task row
  then moves to `retry_scheduled`, `exhausted`, or stays reflectable via last outcome fields on GET.
  For `GET /v1/tasks/{id}`, return the **task** state (`pending`/`retry_scheduled`/…); include
  `last_attempt_outcome` when useful.
- Practical simplification for MVP storage: persist task states
  `{accepted, pending_approval, denied, canceled, pending, executing, retry_scheduled, executed, exhausted}`
  and store attempt outcomes `timeout|failed|ok` on `attempts` only (do not leave the task row
  permanently in `timeout`/`failed` except as last_outcome). Audit still emits
  `executing → executed|timeout|failed|will-retry|exhausted` transition events.
- **No phantom `executed`:** crash during `executing` resumes as `pending` (FR-6.5).
- Approval HTTP: always `202` with `{task_id, status}` where `status` is the task state
  (e.g. `pending_approval` or `pending`). Use `504` only if the **client** waits synchronously
  for approval completion and the 120s gate times out (maps to task `denied`).

## 10. Complementary add-ons (post-MVP, non-contradicting extensions)

- **Termux:Boot** — in scope as lifecycle (`01-start-agent` + resume). MVP.
- **Tasker query** (`tasker.query_variable`, e.g. DND state) — Tier A adapter via Tasker plugin. Post-MVP.
- **Termux:Float** prompt — alternative `approval_backend`. Post-MVP.
- **Termux:X11 control, faster-whisper/cloud STT library, SSH/ngrok exposure** — deferred/rejected
  per §6 (scope / threat-model conflict).

## 11. Build milestones

1. Config + verbs loader + catalog validation (Tier A forbids mutating argv; unknown binary/flag rejected). **Done in tree.**
2. Auth + HTTP skeleton + `battery.status` end-to-end against real `termux-battery-status`.
3. Approval gate (`termux-dialog confirm`) + redaction tests + `approval-policy.json` merge.
4. Audit NDJSON + SQLite queue + worker + retry + exhaustion notification.
5. Circuit-breaker + resume-order test (kill -9 mid-task → requeued, never phantom-executed).
6. Streams (`location.stream`, `sensor.stream`) + ring buffer + DELETE cleanup.
7. Full `verbs.yaml` seed (§5) + `go test` matrix with fake-`termux-*` PATH shim for CI.
