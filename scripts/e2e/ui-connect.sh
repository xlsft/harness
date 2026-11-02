#!/usr/bin/env bash
# Browser-transport check of `harness-client serve --listen` with the fake gateway (no core, no network).
#
# It builds harness-client and the fake gateway, starts the client's TCP listener on a random loopback
# port with a generated token, checks the access rules with curl (CORS preflight, Origin, Host, token),
# then runs packages/ui/test/connect.live.test.ts: the UI's ConnectTransport (connect-web over fetch,
# HTTP/1.1) initializes, creates a session and streams a turn through Subscribe.
#
# Usage: scripts/e2e/ui-connect.sh     (needs go, node, pnpm, curl; run `pnpm install` in packages/ui first)
# Env:   E2E_KEEP=1 keeps the temp dir.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
free_port() { node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})'; }
GW_PORT="$(free_port)"
AGENT_PORT="$(free_port)"
AGENT_URL="http://127.0.0.1:${AGENT_PORT}"
ORIGIN="http://localhost:3210"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/harness-ui-connect.XXXXXX")"
PIDS=()

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; for f in "$WORK"/*.log; do echo "--- $f"; tail -n 40 "$f"; done >&2; exit 1; }

cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [[ "${E2E_KEEP:-}" == "1" ]]; then echo "kept $WORK"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT

log "building harness-client and the fake gateway"
(cd "$ROOT/packages/client" && go build -o "$WORK/harness-client" ./cmd/harness-client \
  && go build -o "$WORK/fakegateway" ./internal/fakegateway/cmd/fakegateway)

log "starting the fake gateway on :$GW_PORT"
"$WORK/fakegateway" -addr "127.0.0.1:$GW_PORT" -scenario echo >"$WORK/gateway.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 40); do grep -q listening "$WORK/gateway.log" 2>/dev/null && break; sleep 0.25; done
grep -q listening "$WORK/gateway.log" || fail "fake gateway did not start"

export HARNESS_HOME="$WORK/home" HARNESS_KEYRING=0 HARNESS_TMPDIR="$WORK/tmp" HARNESS_NO_SHELL_SNAPSHOT=1
export HARNESS_ACCESS_TOKEN="hat_e2e" HARNESS_SERVER_URL="http://127.0.0.1:$GW_PORT"
mkdir -p "$HARNESS_TMPDIR"

log "refusing a non-loopback bind"
if "$WORK/harness-client" serve --listen "0.0.0.0:$AGENT_PORT" >"$WORK/refuse.log" 2>&1; then
  fail "serve accepted 0.0.0.0 without --allow-non-loopback"
fi
grep -q -- '--allow-non-loopback' "$WORK/refuse.log" || fail "no hint about --allow-non-loopback"

log "starting harness-client serve --listen 127.0.0.1:$AGENT_PORT"
"$WORK/harness-client" serve --listen "127.0.0.1:$AGENT_PORT" >"$WORK/client.log" 2>&1 &
PIDS+=($!)
TOKEN_FILE="$HARNESS_HOME/agent-token"
for _ in $(seq 1 40); do grep -q 'serving AgentService on http' "$WORK/client.log" 2>/dev/null && break; sleep 0.25; done
[[ -s "$TOKEN_FILE" ]] || fail "no token file at $TOKEN_FILE"
[[ "$(stat -f %Lp "$TOKEN_FILE" 2>/dev/null || stat -c %a "$TOKEN_FILE")" == "600" ]] || fail "token file is not mode 0600"
TOKEN="$(tr -d '\n' <"$TOKEN_FILE")"

status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
RPC="$AGENT_URL/harness.agent.v1.AgentService/ListSessions"
JSON=(-H 'content-type: application/json' -H 'connect-protocol-version: 1' -d '{}')

log "checking access rules"
code="$(status -X OPTIONS -H "origin: $ORIGIN" -H 'access-control-request-method: POST' \
  -H 'access-control-request-headers: authorization,content-type,connect-protocol-version' "$RPC")"
[[ "$code" == "204" ]] || fail "preflight: $code"
[[ "$(status "${JSON[@]}" -H "origin: $ORIGIN" "$RPC")" == "401" ]] || fail "missing token was not 401"
[[ "$(status "${JSON[@]}" -H "origin: $ORIGIN" -H "authorization: Bearer wrong-token-0000000000" "$RPC")" == "401" ]] || fail "wrong token was not 401"
[[ "$(status "${JSON[@]}" -H 'origin: https://evil.example' -H "authorization: Bearer $TOKEN" "$RPC")" == "403" ]] || fail "foreign origin was not 403"
[[ "$(status "${JSON[@]}" -H "host: evil.example:$AGENT_PORT" -H "authorization: Bearer $TOKEN" "$RPC")" == "403" ]] || fail "rebinding Host was not 403"
headers="$(curl -s -D - -o /dev/null "${JSON[@]}" -H "origin: $ORIGIN" -H "authorization: Bearer $TOKEN" "$RPC")"
grep -qi '^HTTP/1.1 200' <<<"$headers" || fail "authorized call: $headers"
grep -qi "^access-control-allow-origin: $ORIGIN" <<<"$headers" || fail "no CORS header on the authorized call"

log "running the UI's ConnectTransport against the client"
PROJ="$WORK/proj"
mkdir -p "$PROJ"
(cd "$ROOT/packages/ui" && HARNESS_AGENT_URL="$AGENT_URL" HARNESS_AGENT_TOKEN="$TOKEN" HARNESS_AGENT_CWD="$PROJ" \
  HARNESS_AGENT_ORIGIN="$ORIGIN" pnpm exec vitest run test/connect.live.test.ts) >"$WORK/vitest.log" 2>&1 \
  || fail "connect-web live test"
grep -q '2 passed' "$WORK/vitest.log" || fail "live test was skipped or incomplete"

log "ui-connect passed"
