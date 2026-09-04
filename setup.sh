#!/bin/sh
# On-device Termux install for dispatcher-go (Go loopback dispatcher).
# Usage:
#   curl -sL https://raw.githubusercontent.com/DSamuelHodge/dispatcher-go/main/setup.sh | bash
#
# No ADB. Run this inside Termux. Installs the prebuilt android-arm64
# binary (falls back to building from source), the verbs catalog, and the
# Termux:Boot hook. Picks the first free loopback port starting at 8477 so
# it never conflicts with an already-running dispatcher.

set -eu

REPO="${REPO:-DSamuelHodge/dispatcher-go}"
REPO_REF="${REPO_REF:-main}"
RELEASE_TAG="${RELEASE_TAG:-v0.7.0}"
TAILCAT_RELEASE_TAG="${TAILCAT_RELEASE_TAG:-tailcat-v0.5.0}"
INSTALL_TAILCAT="${INSTALL_TAILCAT:-0}"
INSTALL_DIR="${INSTALL_DIR:-${HOME}/dispatcher-go}"
PREFIX_DIR="${PREFIX:-/data/data/com.termux/files/usr}"
BIN_DIR="${BIN_DIR:-${PREFIX_DIR}/bin}"
BIN="${BIN_DIR}/dispatcher-go"
BOOT_DIR="${HOME}/.termux/boot"
RAW="https://raw.githubusercontent.com/${REPO}/${REPO_REF}"

die() {
  echo "setup.sh: $*" >&2
  exit 1
}

if [ ! -d /data/data/com.termux ]; then
  die "this installer is for Termux on Android"
fi

if ! command -v pkg >/dev/null 2>&1; then
  die "pkg not on PATH -- run from a Termux shell"
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "-> installing curl (pkg)"
  pkg install -y curl || die "curl is required"
fi

echo "-> installing termux-api helpers (pkg)"
if ! pkg install -y termux-api; then
  echo "setup.sh: pkg install termux-api failed; continuing -- install the Termux:API app + package or verbs will fail at runtime" >&2
fi

# Pick the first free loopback port: anything answering HTTP (even 404/401)
# means occupied; connection-refused means free.
PORT=8477
for p in 8477 8478 8479 8480 8481 8482; do
  if curl -sm 1 -o /dev/null "http://127.0.0.1:${p}/" 2>/dev/null; then
    echo "-> port ${p} occupied, trying next"
  else
    PORT=$p
    break
  fi
done
echo "-> using port ${PORT}"

echo "-> installing into ${INSTALL_DIR} (+ ${BIN})"
mkdir -p "$INSTALL_DIR" "$INSTALL_DIR/logs" "$INSTALL_DIR/data" "$BIN_DIR" "$BOOT_DIR"

install_binary() {
  url="https://github.com/${REPO}/releases/download/${RELEASE_TAG}/dispatcher-go-android-arm64"
  echo "-> downloading ${url}"
  if curl -fSL --retry 2 -o "${BIN}.new" "$url"; then
    chmod 700 "${BIN}.new"
    mv -f "${BIN}.new" "$BIN"
    return 0
  fi
  rm -f "${BIN}.new"
  return 1
}

build_binary() {
  echo "-> release download failed; building from source (needs golang, takes a while)"
  command -v git >/dev/null 2>&1 || pkg install -y git || die "git is required"
  command -v go >/dev/null 2>&1 || pkg install -y golang || die "golang is required for source build"
  WORKDIR="${TMPDIR:-/tmp}/dispatcher-go-src"
  rm -rf "$WORKDIR"
  git clone --depth 1 --branch "$REPO_REF" "https://github.com/${REPO}.git" "$WORKDIR"
  (cd "$WORKDIR" && GOOS=android GOARCH=arm64 go build -o "$BIN" ./cmd/dispatcher)
  chmod 700 "$BIN"
  rm -rf "$WORKDIR"
}

if ! install_binary; then
  build_binary
fi
[ -x "$BIN" ] || die "binary install failed"

echo "-> fetching verbs.yaml + boot hook (${REPO_REF})"
curl -fsSL -o "${INSTALL_DIR}/verbs.yaml" "${RAW}/verbs.yaml" || die "verbs.yaml download failed"
curl -fsSL -o "${BOOT_DIR}/01-start-agent" "${RAW}/scripts/01-start-agent" || die "boot hook download failed"
chmod 700 "${BOOT_DIR}/01-start-agent"
cp -a "$0" "${INSTALL_DIR}/setup.sh" 2>/dev/null || curl -fsSL -o "${INSTALL_DIR}/setup.sh" "${RAW}/setup.sh" || true

if [ "$PORT" != "8477" ]; then
  echo "-> rewriting catalog listen to 127.0.0.1:${PORT}"
  sed -i "s|^\( *listen: \"127.0.0.1:\)[0-9]*\"|\1${PORT}\"|" "${INSTALL_DIR}/verbs.yaml"
fi

"$BIN" -catalog "${INSTALL_DIR}/verbs.yaml" -data-dir "$INSTALL_DIR" -validate \
  || die "catalog validation failed"

if [ "$INSTALL_TAILCAT" = "1" ]; then
  echo "-> installing tailcat (remote access)"
  curl -fSL --retry 2 -o "${BIN_DIR}/tailcat.new" \
    "https://github.com/${REPO}/releases/download/${TAILCAT_RELEASE_TAG}/tailcat-android-arm64" \
    || die "tailcat download failed"
  chmod 700 "${BIN_DIR}/tailcat.new"
  mv -f "${BIN_DIR}/tailcat.new" "${BIN_DIR}/tailcat"
  mkdir -p "${INSTALL_DIR}/scripts"
  curl -fsSL -o "${INSTALL_DIR}/scripts/tailcat-serve.sh" "${RAW}/scripts/tailcat-serve.sh" || die "tailcat-serve.sh download failed"
  curl -fsSL -o "${BOOT_DIR}/02-start-tailcat" "${RAW}/scripts/02-start-tailcat" || die "tailcat boot hook download failed"
  chmod 700 "${INSTALL_DIR}/scripts/tailcat-serve.sh" "${BOOT_DIR}/02-start-tailcat"
  echo "-> tailcat installed; REMOTE-ACCESS NEXT STEPS (see README):"
  echo "   1. ${BIN_DIR}/tailcat genkey --key=dispatcher --fixed-region   # prints tc... address"
  echo "   2. agent: tailcat genkey --client --key=agent-phone-1           # prints nodekey:..."
  echo "   3. echo 'nodekey:...' > ${INSTALL_DIR}/.tailcat-allow"
  echo "   4. sh ${INSTALL_DIR}/scripts/tailcat-serve.sh"
fi

echo
echo "installed to ${INSTALL_DIR}"
echo "binary:   ${BIN} ($("$BIN" -version 2>/dev/null || echo dispatcher-go))"
echo "start:    ${BIN} -data-dir ${INSTALL_DIR} -catalog ${INSTALL_DIR}/verbs.yaml"
echo "boot:     Termux:Boot will run ~/.termux/boot/01-start-agent after reboot"
echo "token:    cat ${INSTALL_DIR}/.agent-token   (created on first start)"
echo
echo "smoke:"
echo "  ${BIN} -data-dir ${INSTALL_DIR} -catalog ${INSTALL_DIR}/verbs.yaml &"
echo "  TOKEN=\$(cat ${INSTALL_DIR}/.agent-token)"
echo "  curl -H \"X-Agent-Token: \$TOKEN\" http://127.0.0.1:${PORT}/v1/health"
echo "  curl -X POST -H \"X-Agent-Token: \$TOKEN\" -H 'Content-Type: application/json' -d '{}' \\"
echo "    http://127.0.0.1:${PORT}/v1/verbs/battery.status"
