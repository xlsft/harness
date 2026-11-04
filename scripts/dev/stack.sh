#!/usr/bin/env bash
# Local development stack: builds and starts core with an admin account and signs the client in.
# Providers, keys, models and the auto-mode classifier are configured in the app (Settings ->
# Providers): global ones as the admin, personal ones as any user. No keys go through env vars.
#
# Usage:
#   scripts/dev/stack.sh        # start
#   scripts/dev/stack.sh stop   # stop the stack started by this script
#
# State lives in packages/core/.data-dev (server) and .dev/home (client), both git-ignored.
#
# Object store: Garage publishes Linux binaries only, so instead of a fourth process core uses its file backend behind the
# same interface (packages/core/.data-dev/objects). To try a real S3, export HARNESS_S3_ENDPOINT, HARNESS_S3_ACCESS_KEY and
# HARNESS_S3_SECRET_KEY before starting, for example against the image's Garage (deploy/README.md).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PORT="${HARNESS_DEV_PORT:-3737}"
URL="http://localhost:${PORT}"
DATA="$ROOT/packages/core/.data-dev"
DEV="$ROOT/.dev"
ADMIN_EMAIL="${HARNESS_ADMIN_EMAIL:-admin@harness.local}"
mkdir -p "$DEV"
PASSFILE="$DEV/admin-password"
[[ -f "$PASSFILE" ]] || (umask 077; openssl rand -hex 16 >"$PASSFILE")
ADMIN_PASSWORD="$(cat "$PASSFILE")"
export HARNESS_HOME="$DEV/home" HARNESS_KEYRING=0

# Resolve the real node binary: version-manager shims (volta, asdf) do not forward signals, so
# killing the shim would orphan the actual server process.
NODE_BIN="$(node -p process.execPath)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarn:\033[0m %s\n' "$*" >&2; }

# Stops core and waits until it has exited: it holds the embedded database lock until then, and a core
# started too early fails with "database in use".
stop_core() {
  [[ -f "$DEV/core.pid" ]] || return 0
  local pid
  pid="$(cat "$DEV/core.pid")"
  if kill "$pid" 2>/dev/null; then
    for _ in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    kill -9 "$pid" 2>/dev/null || true
    log "core stopped"
  fi
  rm -f "$DEV/core.pid"
}

if [[ "${1:-}" == "stop" ]]; then
  stop_core
  exit 0
fi

log "building core and client"
(cd "$ROOT/packages/core" && pnpm install --frozen-lockfile >/dev/null && pnpm build >/dev/null)
(cd "$ROOT/packages/client" && go build -o "$DEV/harness-client" ./cmd/harness-client)

if ! curl -fsS "$URL/api/health" >/dev/null 2>&1; then
  log "starting core on $URL (logs: .dev/core.log)"
  (
    cd "$ROOT/packages/core"
    PORT="$PORT" HARNESS_DATA_DIR="$DATA" HARNESS_PUBLIC_URL="$URL" \
      HARNESS_ADMIN_EMAIL="$ADMIN_EMAIL" HARNESS_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
      nohup "$NODE_BIN" .output/server/index.mjs >"$DEV/core.log" 2>&1 &
    echo $! >"$DEV/core.pid"
  )
  for _ in $(seq 1 60); do curl -fsS "$URL/api/health" >/dev/null 2>&1 && break; sleep 0.5; done
fi
curl -fsS "$URL/api/health" >/dev/null || { warn "core is not healthy, see .dev/core.log"; exit 1; }

JAR="$DEV/cookies"
CURL=(curl -fsS -c "$JAR" -b "$JAR" -H "origin: $URL")
JSON=(-H 'content-type: application/json')
"${CURL[@]}" "${JSON[@]}" -d "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')" \
  "$URL/api/auth/sign-in/email" >/dev/null

if ! "$DEV/harness-client" whoami >/dev/null 2>&1; then
  log "signing the client in (device flow, auto-approved as admin)"
  "$DEV/harness-client" login "$URL" >"$DEV/login.log" 2>&1 &
  LOGIN_PID=$!
  CODE=""
  for _ in $(seq 1 40); do
    CODE="$(grep -oE '[A-Z0-9]{4}-[A-Z0-9]{4}' "$DEV/login.log" | head -1 || true)"
    [[ -n "$CODE" ]] && break
    sleep 0.25
  done
  "${CURL[@]}" --data-urlencode "code=$CODE" -d action=approve "$URL/device" >/dev/null
  wait "$LOGIN_PID"
fi

log "ready"
echo "  server:   $URL  (admin: $ADMIN_EMAIL, password in .dev/admin-password)"
echo "  client:   HARNESS_HOME=$DEV/home HARNESS_KEYRING=0 $DEV/harness-client -p \"...\" --model <id>"
echo "  models:   $("$DEV/harness-client" whoami >/dev/null && curl -fsS -b "$JAR" "$URL/api/admin/models" | jq -r '[.models[]?.id // .[]?.id] | join(", ")' 2>/dev/null || echo '?')"
echo "  objects:  ${HARNESS_S3_ENDPOINT:-files in packages/core/.data-dev/objects (dev fallback)}"
echo "  stop:     scripts/dev/stack.sh stop"
