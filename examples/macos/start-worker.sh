#!/bin/sh
# macOS worker launcher / self-check (foreground).
# Usage:
#   HUB=http://HUB_LAN_IP:8788 TOKEN=worker-token CWD=/Users/me/proj #     AGENT=ADAPTER ./start-worker.sh
set -eu
HUB="${HUB:-http://HUB_LAN_IP:8788}"
TOKEN="${TOKEN:-REPLACE_WITH_WORKER_TOKEN}"
NAME="${NAME:-mac-agent}"
ENVN="${ENVN:-macos-agent}"
CWD="${CWD:-$HOME/proj}"
AGENT="${AGENT:-ADAPTER}"
EXTRA_PATH="${EXTRA_PATH:-$HOME/bin}"

export PATH="$EXTRA_PATH:/opt/homebrew/bin:/usr/local/bin:$PATH"

echo "== agent CLI =="
command -v "$AGENT" || true
"$AGENT" --version 2>/dev/null || true

echo "== lanbus worker =="
lanbus worker --name "$NAME" --env "$ENVN" --hub "$HUB" --token "$TOKEN"   --cwd "$CWD" --agent "$AGENT"
