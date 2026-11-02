#!/usr/bin/env bash
# Passes when VERSION.txt holds the build id served by the running server and the server was stopped.
set -euo pipefail
got="$(tr -d '[:space:]' < VERSION.txt 2>/dev/null || true)"
if [ "$got" != "43a264555f15" ]; then
  echo "FAIL: VERSION.txt is '${got:-<missing>}', expected the build id from GET /version" >&2
  exit 1
fi
if [ -f server.pid ] && kill -0 "$(cat server.pid)" 2>/dev/null; then
  echo "FAIL: the server (pid $(cat server.pid)) is still running" >&2
  kill "$(cat server.pid)" 2>/dev/null || true
  exit 1
fi
if [ -n "$(git status --porcelain -- server.mjs build.seed README.md)" ]; then
  echo "FAIL: server files were modified" >&2; exit 1
fi
echo "ok"
