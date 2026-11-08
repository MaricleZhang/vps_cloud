#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .local
if [ ! -d web/node_modules ]; then
  npm --prefix web ci
fi
(cd backend && go build -o ../.local/cloud .)
api_pid=''
web_pid=''
cleanup() {
  if [ -n "$api_pid" ]; then kill "$api_pid" 2>/dev/null || true; fi
  if [ -n "$web_pid" ]; then kill "$web_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT INT TERM
WEB_DIST=web/dist .local/cloud -demo -db .local/demo.db &
api_pid=$!
(cd web && exec node node_modules/vite/bin/vite.js --host 127.0.0.1) &
web_pid=$!
printf '\nNice Cloud 本地演示：http://127.0.0.1:5173\n按 Ctrl+C 停止前后端服务。\n\n'
while kill -0 "$api_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; do
  sleep 1
done
printf '\n一个服务已退出，正在停止另一个服务。\n'
exit 1
