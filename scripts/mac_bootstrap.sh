#!/bin/bash
# One-command macOS worker bootstrap for lanbus.
# Usage:
#   curl -fsSL http://HUB:8788/download/lanbus-mac-bootstrap.sh | \
#     bash -s -- http://HUB:8788 WORKER_TOKEN [agent] [name] [env] [cwd]
set -euo pipefail
HUB="${1:-http://127.0.0.1:8788}"
TOKEN="${2:-${LANBUS_TOKEN:-}}"
AGENT="${3:-${LANBUS_AGENT:-claude}}"
NAME="${4:-${LANBUS_NAME:-mac-claude}}"
ENVNAME="${5:-${LANBUS_ENV:-macos-claude}}"
CWD="${6:-${LANBUS_CWD:-$HOME}}"
if [ -z "$TOKEN" ]; then echo "worker token required" >&2; exit 2; fi
case "$(uname -m)" in
  arm64) ARCH=darwin-arm64 ;;
  x86_64) ARCH=darwin-amd64 ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 2 ;;
esac
BIN="${TMPDIR:-/tmp}/lanbus-worker-$ARCH"
curl -fsSL "$HUB/download/lanbus-worker-$ARCH" -o "$BIN"
chmod +x "$BIN"
export PATH="$HOME/.local/bin:$HOME/.claude/local:/opt/homebrew/bin:/usr/local/bin:$PATH"
echo "[lanbus] starting worker $NAME env=$ENVNAME agent=$AGENT cwd=$CWD hub=$HUB"
exec "$BIN" --name "$NAME" --env "$ENVNAME" --hub "$HUB" --token "$TOKEN" \
  --cwd "$CWD" --agent "$AGENT"
