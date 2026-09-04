#!/bin/sh
# Agent side: forward the phone's dispatcher to a local port over tailcat.
# Usage: tailcat-forward.sh <tc-address> [local-port [remote-port]]
#   Defaults: local 18477, remote 8477 (the dispatcher's loopback port).
# Requires a tailcat client key the phone allowlisted:
#   tailcat genkey --client --key=agent-phone-1   (auto-used when present)
# Then: curl -H "X-Agent-Token: $TOKEN" http://127.0.0.1:18477/v1/health
set -eu

ADDR="${1:-${TAILCAT_ADDR:-}}"
LOCAL="${2:-${TAILCAT_LOCAL:-18477}}"
REMOTE="${3:-${TAILCAT_REMOTE:-8477}}"
[ -n "$ADDR" ] || { echo "usage: $0 <tc-address> [local-port [remote-port]]" >&2; exit 1; }
command -v tailcat >/dev/null 2>&1 || { echo "tailcat-forward: tailcat not on PATH" >&2; exit 1; }

echo "tailcat-forward: probing $ADDR (until direct path)..."
tailcat ping --until-direct --timeout 30s "$ADDR" 2>/dev/null || \
  echo "tailcat-forward: no direct path (DERP relay fallback is fine), continuing"

backoff=2
while true; do
  echo "tailcat-forward: 127.0.0.1:${LOCAL} -> phone:${REMOTE}"
  tailcat forward "$ADDR" "${LOCAL}:${REMOTE}" || true
  echo "tailcat-forward: forward dropped, retrying in ${backoff}s" >&2
  sleep $backoff
  backoff=$((backoff < 60 ? backoff * 2 : 60))
done
