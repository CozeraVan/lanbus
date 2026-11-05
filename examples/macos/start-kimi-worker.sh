#!/bin/sh
# macOS Kimi worker 前台启动 / 自检
# 用法:
#   HUB=http://192.168.1.10:8788 TOKEN=worker-token CWD=/Users/me/proj ./start-kimi-worker.sh
set -eu
HUB="${HUB:-http://HUB_LAN_IP:8788}"
TOKEN="${TOKEN:-REPLACE_WITH_WORKER_TOKEN}"
NAME="${NAME:-mac-kimi}"
CWD="${CWD:-$HOME/proj}"
KIMI_BIN="${KIMI_BIN:-$HOME/.kimi-code/bin}"

export PATH="$KIMI_BIN:/opt/homebrew/bin:/usr/local/bin:$PATH"

echo "== kimi =="
command -v kimi || true
kimi --version 2>/dev/null || true

echo "== lanbus worker =="
lanbus worker --name "$NAME" --env macos --hub "$HUB" --token "$TOKEN" \
  --cwd "$CWD" --agent kimi
