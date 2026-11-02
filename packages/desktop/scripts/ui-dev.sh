#!/usr/bin/env bash
# Dev server for the desktop shell: builds the sidecar, then reuses a UI dev server already running on :3210
# (Nuxt allows one per directory), otherwise starts one. In the shell the UI takes its agent endpoint from Tauri, so how that
# server was started (mock or connect env) does not matter.
set -euo pipefail
# A fresh sidecar for every dev start, so the app never runs a stale harness-client.
bash "$(dirname "$0")/build-sidecar.sh"
if curl -s -o /dev/null -m 2 http://localhost:3210/; then
  echo "using the UI dev server on :3210"
  exit 0
fi
exec pnpm --dir "$(dirname "$0")/../../ui" exec nuxt dev --port 3210
