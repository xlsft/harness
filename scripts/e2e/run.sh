#!/usr/bin/env bash
# End-to-end test of core + client with a scripted model upstream (no real provider, no network).
#
# It builds both packages, starts a mock OpenAI-compatible upstream and the core server with a
# throwaway data dir, signs the client in through the device flow (approving it the way the
# browser page does), configures a provider and a model through the admin API, then runs one
# headless agent turn that must call the Bash tool and create a file. It also checks remote
# control: `harness-client serve` mirrors a session through RelayService, and a viewer that only
# talks to the server sends the prompt, answers the permission prompt and watches the turn.
#
# Usage: scripts/e2e/run.sh            (from anywhere; needs node 24, pnpm, go, curl, jq)
# Env:   E2E_KEEP=1 keeps the temp dir; E2E_CORE_PORT / E2E_UPSTREAM_PORT override ports.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
free_port() { node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})'; }
# Random free ports by default, so parallel runs (CI matrix, several checkouts) never collide.
CORE_PORT="${E2E_CORE_PORT:-$(free_port)}"
UPSTREAM_PORT="${E2E_UPSTREAM_PORT:-$(free_port)}"
CORE_URL="http://localhost:${CORE_PORT}"
ADMIN_EMAIL="admin@e2e.test"
ADMIN_PASSWORD="e2e-password-$(date +%s)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/harness-e2e.XXXXXX")"
PIDS=()

# Resolve the real node binary: version-manager shims (volta, asdf) do not forward signals, so
# killing the shim would orphan the actual server process.
NODE_BIN="$(node -p process.execPath)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; for f in "$WORK"/*.log; do echo "--- $f"; tail -n 40 "$f"; done >&2; exit 1; }

cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [[ "${E2E_KEEP:-}" == "1" ]]; then echo "kept $WORK"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT

wait_for() { # url, name
  for _ in $(seq 1 60); do curl -fsS "$1" >/dev/null 2>&1 && return 0; sleep 0.5; done
  fail "$2 did not become ready at $1"
}

log "building core"
(cd "$ROOT/packages/core" && pnpm install --frozen-lockfile >/dev/null && pnpm build >/dev/null)
log "building client"
(cd "$ROOT/packages/client" && go build -o "$WORK/harness-client" ./cmd/harness-client \
  && go build -o "$WORK/fake-mcp-server" ./internal/fakemcp/cmd/fake-mcp-server \
  && go build -o "$WORK/relayviewer" ./internal/e2e/relayviewer)

log "starting mock upstream on :$UPSTREAM_PORT"
"$NODE_BIN" "$ROOT/scripts/e2e/upstream.mjs" "$UPSTREAM_PORT" >"$WORK/upstream.log" 2>&1 &
PIDS+=($!)

log "starting core on :$CORE_PORT"
(
  cd "$ROOT/packages/core"
  PORT="$CORE_PORT" HARNESS_DATA_DIR="$WORK/data" HARNESS_PUBLIC_URL="$CORE_URL" \
    HARNESS_ADMIN_EMAIL="$ADMIN_EMAIL" HARNESS_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
    exec "$NODE_BIN" .output/server/index.mjs
) >"$WORK/core.log" 2>&1 &
PIDS+=($!)
wait_for "$CORE_URL/api/health" "core"

export HARNESS_HOME="$WORK/home" HARNESS_KEYRING=0

log "device login"
"$WORK/harness-client" login "$CORE_URL" >"$WORK/login.log" 2>&1 &
LOGIN_PID=$!
PIDS+=($LOGIN_PID)
CODE=""
for _ in $(seq 1 40); do
  CODE="$(grep -oE '[A-Z0-9]{4}-[A-Z0-9]{4}' "$WORK/login.log" | head -1 || true)"
  [[ -n "$CODE" ]] && break
  sleep 0.25
done
[[ -n "$CODE" ]] || fail "no user code printed by login"

JAR="$WORK/cookies"
CURL=(curl -fsS -c "$JAR" -b "$JAR" -H "origin: $CORE_URL")
"${CURL[@]}" -H 'content-type: application/json' \
  -d "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')" \
  "$CORE_URL/api/auth/sign-in/email" >/dev/null || fail "admin sign-in"
"${CURL[@]}" --data-urlencode "code=$CODE" -d action=approve "$CORE_URL/device" | grep -q 'Device connected' \
  || fail "device approval"
wait "$LOGIN_PID" || fail "login command exited with an error"
"$WORK/harness-client" whoami | grep -q "$ADMIN_EMAIL" || fail "whoami"

log "configuring provider and model"
JSON=(-H 'content-type: application/json')
"${CURL[@]}" "${JSON[@]}" -d "{\"id\":\"mock\",\"type\":\"openai-chat\",\"base_url\":\"http://127.0.0.1:${UPSTREAM_PORT}/v1\",\"api_key\":\"sk-mock\",\"allow_private_network\":true}" \
  "$CORE_URL/api/admin/providers" >/dev/null || fail "create provider"
"${CURL[@]}" "${JSON[@]}" -d '{"id":"mock-model","display_name":"Mock","provider_id":"mock","upstream_model":"mock","context_window":128000,"max_output_tokens":8000,"pricing":{"input":1,"output":2}}' \
  "$CORE_URL/api/admin/models" >/dev/null || fail "create model"
"${CURL[@]}" "${JSON[@]}" -X PUT -d '{"main":"mock-model","small":"mock-model","plan":"mock-model"}' \
  "$CORE_URL/api/admin/slots" >/dev/null || fail "set slots"

log "running an agent turn"
PROJ="$WORK/proj"
mkdir -p "$PROJ"
git -C "$PROJ" init -q
echo "# e2e" >"$PROJ/README.md"
git -C "$PROJ" add . && git -C "$PROJ" -c user.email=e2e@test -c user.name=e2e commit -qm init
RESULT="$(cd "$PROJ" && "$WORK/harness-client" -p "create out.txt with a marker" \
  --model mock-model --permission-mode bypassPermissions --output-format json)" || fail "agent turn"
echo "$RESULT" >"$WORK/result.json"

[[ "$(jq -r .subtype <<<"$RESULT")" == "success" ]] || fail "result subtype: $(jq -r .subtype <<<"$RESULT")"
[[ "$(jq -r .num_turns <<<"$RESULT")" == "2" ]] || fail "expected 2 turns, got $(jq -r .num_turns <<<"$RESULT")"
[[ "$(cat "$PROJ/out.txt" 2>/dev/null)" == "harness-e2e" ]] || fail "out.txt missing or wrong"
[[ "$(jq -r .total_cost_usd <<<"$RESULT")" != "0" ]] || fail "cost was not reported"
grep -q 'tools=.*Bash' "$WORK/upstream.log" || fail "upstream did not receive the Bash tool definition"

log "remote control through the relay"
PROJ2="$WORK/proj2"
mkdir -p "$PROJ2"
git -C "$PROJ2" init -q
SOCK="$WORK/agent.sock"
"$WORK/harness-client" serve --socket "$SOCK" >"$WORK/serve.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 40); do [[ -S "$SOCK" ]] && break; sleep 0.25; done
[[ -S "$SOCK" ]] || fail "serve did not create its socket"
"$WORK/relayviewer" --socket "$SOCK" --cwd "$PROJ2" --model mock-model >"$WORK/relay.log" 2>&1 \
  || fail "relay viewer: $(tail -n 5 "$WORK/relay.log")"
cat "$WORK/relay.log"

log "stdio MCP server through McpService"
TOKEN="$(curl -fsS -H 'content-type: application/json' \
  -d "$(jq -n --arg t "$(jq -r .device_token "$HARNESS_HOME/credentials.json")" '{deviceToken:$t}')" \
  "$CORE_URL/harness.auth.v1.DeviceAuthService/RefreshAccessToken" | jq -r .accessToken)"
[[ -n "$TOKEN" && "$TOKEN" != "null" ]] || fail "access token for McpService"
curl -fsS -H 'content-type: application/json' -H "authorization: Bearer $TOKEN" \
  -d "$(jq -n --arg c "$WORK/fake-mcp-server" '{server:{name:"e2e",scope:"SCOPE_USER",transport:"TRANSPORT_STDIO",enabled:true,
      stdio:{command:$c,env:{FAKE_MCP_SECRET:"${secret:TOKEN}"}}},secrets:{set:{TOKEN:"e2e-secret"}}}')" \
  "$CORE_URL/harness.mcp.v1.McpService/CreateServer" >/dev/null || fail "create MCP server"
MCP_RESULT="$(cd "$PROJ" && HARNESS_MCP_APPROVE=e2e "$WORK/harness-client" -p "use mcp to echo" \
  --model mock-model --permission-mode bypassPermissions --output-format json)" || fail "MCP agent turn"
echo "$MCP_RESULT" >"$WORK/mcp-result.json"
[[ "$(jq -r .result <<<"$MCP_RESULT")" == "MCP said: echo: harness-e2e secret=e2e-secret" ]] \
  || fail "MCP result: $(jq -r .result <<<"$MCP_RESULT")"
grep -q 'tools=.*mcp__e2e__echo' "$WORK/upstream.log" || fail "upstream did not receive the MCP tool definition"
[[ ! -e "$HARNESS_HOME/mcp-approvals.json" ]] || fail "an approval given through HARNESS_MCP_APPROVE must not be persisted"

log "e2e passed: $(jq -c '{subtype,num_turns,total_cost_usd,result}' <<<"$RESULT")"
