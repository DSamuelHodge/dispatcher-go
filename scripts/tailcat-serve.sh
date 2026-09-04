#!/data/data/com.termux/files/usr/bin/sh
# Phone side: serve the dispatcher over tailcat (WireGuard, no accounts).
# Fail-closed: refuses to run without a client allowlist.
#
# First run (in Termux):
#   pkg install -y termux-api  # if not already
#   tailcat genkey --key=dispatcher --fixed-region   # prints tc... address once
#   <agent side> tailcat genkey --client --key=agent-phone-1  # prints nodekey:...
#   echo 'nodekey:XXXX...' > ~/dispatcher-go/.tailcat-allow
#   TAILCAT_ALLOW=$(cat ~/dispatcher-go/.tailcat-allow) ./scripts/tailcat-serve.sh
# Read the tc... address from the first startup lines, give it + the agent
# token to the remote agent out of band. Boot hook: scripts/02-start-tailcat.
set -eu

PREFIX_DIR="${PREFIX:-/data/data/com.termux/files/usr}"
TAILCAT="${TAILCAT_BIN:-$PREFIX_DIR/bin/tailcat}"
INSTALL_DIR="${INSTALL_DIR:-${HOME}/dispatcher-go}"
ALLOW_FILE="${TAILCAT_ALLOW_FILE:-$INSTALL_DIR/.tailcat-allow}"
ALLOW="${TAILCAT_ALLOW:-}"
if [ -z "$ALLOW" ] && [ -f "$ALLOW_FILE" ]; then
  ALLOW=$(tr -d ' \t\r\n' < "$ALLOW_FILE")
fi
PORT="${TAILCAT_SERVE_PORT:-8477}"
KEY="${TAILCAT_KEY:-dispatcher}"
LOGDIR="${INSTALL_DIR}/logs"

[ -x "$TAILCAT" ] || { echo "tailcat-serve: $TAILCAT not executable (run setup.sh with INSTALL_TAILCAT=1)" >&2; exit 1; }
case "$ALLOW" in
  nodekey:*) ;;
  *) echo "tailcat-serve: refusing to serve without TAILCAT_ALLOW=nodekey:... (or $ALLOW_FILE)" >&2; exit 1 ;;
esac

mkdir -p "$LOGDIR"
echo "tailcat-serve: serving 127.0.0.1:${PORT} key=${KEY} allow=${ALLOW} (log: ${LOGDIR}/tailcat.stdout)"
# Supervised: tailcat exits on network loss; back off and re-serve.
# The tc... address is printed on every start (stable while key lives).
backoff=2
while true; do
  # shellcheck disable=SC2086
  nohup "$TAILCAT" serve --key="$KEY" --allow="$ALLOW" "$PORT" \
    >>"$LOGDIR/tailcat.stdout" 2>>"$LOGDIR/tailcat.stderr" &
  pid=$!
  wait $pid
  code=$?
  echo "tailcat-serve: exited $code, restarting in ${backoff}s" >>"$LOGDIR/tailcat.stdout"
  sleep $backoff
  backoff=$((backoff < 60 ? backoff * 2 : 60))
done
