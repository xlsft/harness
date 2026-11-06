#!/usr/bin/env bash
# End-to-end test of an always-on host (docs/design/hosts.md): the real core and the real `harness-client daemon`, with a
# scripted model and a fake code host that serves real git over HTTP and checks credentials. No laptop in sight: the session
# is started, watched and followed up through the server alone. A team host needs root on Linux, and is not covered here.
#
# Usage: scripts/e2e/hosts.sh          (needs node 24, pnpm, go, curl, jq, git)
# Env:   E2E_KEEP=1 keeps the temp dir.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
free_port() { node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})'; }
CORE_PORT="$(free_port)"
UPSTREAM_PORT="$(free_port)"
CODEHOST_PORT="$(free_port)"
CORE_URL="http://localhost:${CORE_PORT}"
ADMIN_EMAIL="admin@e2e.test"
ADMIN_PASSWORD="e2e-password-$(date +%s)"
GIT_TOKEN="glpat-e2e-$(date +%s)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/harness-hosts-e2e.XXXXXX")"
PIDS=()
NODE_BIN="$(node -p process.execPath)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; for f in "$WORK"/*.log; do echo "--- $f"; tail -n 40 "$f"; done >&2; exit 1; }
cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [[ "${E2E_KEEP:-}" == "1" ]]; then echo "kept $WORK"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT
wait_for() { for _ in $(seq 1 60); do curl -fsS "$1" >/dev/null 2>&1 && return 0; sleep 0.5; done; fail "$2 did not become ready at $1"; }

log "building core and client"
(cd "$ROOT/packages/core" && pnpm install --frozen-lockfile >/dev/null && pnpm build >/dev/null)
(cd "$ROOT/packages/client" && go build -o "$WORK/harness-client" ./cmd/harness-client)

log "a repository on the fake code host"
REPOS="$WORK/repos"
mkdir -p "$REPOS/acme"
git init -q --bare -b main "$REPOS/acme/widgets.git"
git -C "$REPOS/acme/widgets.git" config http.receivepack true
git clone -q "$REPOS/acme/widgets.git" "$WORK/seed" 2>/dev/null
git -C "$WORK/seed" checkout -q -b main
echo "# widgets" >"$WORK/seed/README.md"
git -C "$WORK/seed" add .
git -C "$WORK/seed" -c user.email=e2e@test -c user.name=e2e commit -qm init
git -C "$WORK/seed" push -q origin main

"$NODE_BIN" "$ROOT/scripts/e2e/upstream.mjs" "$UPSTREAM_PORT" >"$WORK/upstream.log" 2>&1 &
PIDS+=($!)
"$NODE_BIN" "$ROOT/scripts/e2e/codehost.mjs" "$CODEHOST_PORT" "$REPOS" "$GIT_TOKEN" >"$WORK/codehost.log" 2>&1 &
PIDS+=($!)
(
  cd "$ROOT/packages/core"
  PORT="$CORE_PORT" HARNESS_DATA_DIR="$WORK/data" HARNESS_PUBLIC_URL="$CORE_URL" \
    HARNESS_ADMIN_EMAIL="$ADMIN_EMAIL" HARNESS_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
    exec "$NODE_BIN" .output/server/index.mjs
) >"$WORK/core.log" 2>&1 &
PIDS+=($!)
wait_for "$CORE_URL/api/health" "core"

export HOME="$WORK/fake-home" HARNESS_KEYRING=0
mkdir -p "$HOME"

log "the admin signs in, configures a model and a connection"
HARNESS_HOME="$WORK/admin-home" "$WORK/harness-client" login "$CORE_URL" >"$WORK/login.log" 2>&1 &
LOGIN_PID=$!
PIDS+=($LOGIN_PID)
CODE=""
for _ in $(seq 1 160); do
  CODE="$(grep -oE '[A-Z0-9]{4}-[A-Z0-9]{4}' "$WORK/login.log" | head -1 || true)"
  [[ -n "$CODE" ]] && break
  sleep 0.25
done
[[ -n "$CODE" ]] || fail "no user code printed by login"
JAR="$WORK/cookies"
CURL=(curl -fsS -c "$JAR" -b "$JAR" -H "origin: $CORE_URL")
JSON=(-H 'content-type: application/json')
"${CURL[@]}" "${JSON[@]}" -d "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')" "$CORE_URL/api/auth/sign-in/email" >/dev/null || fail "admin sign-in"
"${CURL[@]}" --data-urlencode "code=$CODE" -d action=approve "$CORE_URL/device" | grep -q 'Device connected' || fail "device approval"
wait "$LOGIN_PID" || fail "login"
"${CURL[@]}" "${JSON[@]}" -d "{\"id\":\"mock\",\"type\":\"openai-chat\",\"base_url\":\"http://127.0.0.1:${UPSTREAM_PORT}/v1\",\"api_key\":\"sk-mock\",\"allow_private_network\":true}" "$CORE_URL/api/admin/providers" >/dev/null || fail "provider"
"${CURL[@]}" "${JSON[@]}" -d '{"id":"mock-model","display_name":"Mock","provider_id":"mock","upstream_model":"mock","context_window":128000,"max_output_tokens":8000,"pricing":{"input":1,"output":2}}' "$CORE_URL/api/admin/models" >/dev/null || fail "model"
"${CURL[@]}" "${JSON[@]}" -X PUT -d '{"main":"mock-model","small":"mock-model","plan":"mock-model"}' "$CORE_URL/api/admin/slots" >/dev/null || fail "slots"

TOKEN="$(curl -fsS "${JSON[@]}" -d "$(jq -n --arg t "$(jq -r .device_token "$WORK/admin-home/credentials.json")" '{deviceToken:$t}')" "$CORE_URL/harness.auth.v1.DeviceAuthService/RefreshAccessToken" | jq -r .accessToken)"
[[ -n "$TOKEN" && "$TOKEN" != "null" ]] || fail "access token"
rpc() { curl -fsS "${JSON[@]}" -H "authorization: Bearer $TOKEN" -d "$2" "$CORE_URL/harness.$1"; }

rpc vcs.v1.VcsService/PutConnection "$(jq -n --arg u "http://127.0.0.1:${CODEHOST_PORT}" --arg t "$GIT_TOKEN" '{type:"PROVIDER_TYPE_GITLAB",baseUrl:$u,displayName:"fake",accessToken:$t,allowPrivateNetwork:true}')" >/dev/null || fail "connection"

log "registering a host"
MADE="$(rpc relay.v1.HostsService/CreateAgentHost '{"name":"e2e-host","scope":"AGENT_HOST_SCOPE_USER","maxSessions":2,"allowedRepositories":["127.0.0.1/acme/*"]}')" || fail "create host"
HOST_ID="$(jq -r .host.id <<<"$MADE")"
HOST_TOKEN="$(jq -r .deviceToken <<<"$MADE")"
[[ "$HOST_TOKEN" == hdt_* ]] || fail "no device token"

log "starting the daemon"
HARNESS_HOME="$WORK/host-home" HARNESS_SERVER_URL="$CORE_URL" HARNESS_DEVICE_TOKEN="$HOST_TOKEN" \
  "$WORK/harness-client" daemon --root "$WORK/host-root" --allowedTools Bash --verbose >"$WORK/daemon.log" 2>&1 &
DAEMON=$!
PIDS+=($DAEMON)
for _ in $(seq 1 60); do
  [[ "$(rpc relay.v1.HostsService/ListAgentHosts '{}' | jq -r --arg id "$HOST_ID" '.hosts[] | select(.id==$id) | .online // false')" == "true" ]] && break
  sleep 0.5
done
[[ "$(rpc relay.v1.HostsService/ListAgentHosts '{}' | jq -r --arg id "$HOST_ID" '.hosts[] | select(.id==$id) | .online // false')" == "true" ]] || fail "the host did not come online"

log "starting a session in a repository, from the server alone"
WORK_ROW="$(rpc relay.v1.HostsService/StartHostWork "$(jq -n --arg h "$HOST_ID" '{hostId:$h,repositoryKey:"127.0.0.1/acme/widgets",prompt:"create out.txt with a marker",title:"Add the marker",model:"mock-model"}')")" || fail "start work"
WORK_ID="$(jq -r .work.id <<<"$WORK_ROW")"
for _ in $(seq 1 120); do
  ROW="$(rpc relay.v1.HostsService/GetHostWork "$(jq -n --arg i "$WORK_ID" '{id:$i}')")"
  [[ "$(jq -r '.work.pullRequestUrl // ""' <<<"$ROW")" != "" ]] && break
  [[ "$(jq -r '.work.state' <<<"$ROW")" == "HOST_WORK_STATE_FAILED" ]] && fail "the work failed: $(jq -r .work.error <<<"$ROW")"
  sleep 0.5
done
echo "$ROW" >"$WORK/work.json"
[[ "$(jq -r '.work.pullRequestUrl // ""' <<<"$ROW")" != "" ]] || fail "no pull request: $ROW"
SESSION="$(jq -r .work.sessionId <<<"$ROW")"
BRANCH="$(jq -r .work.branch <<<"$ROW")"
[[ "$BRANCH" == "harness/$WORK_ID" ]] || fail "branch: $BRANCH"

log "the work landed"
[[ "$(git --git-dir="$REPOS/acme/widgets.git" show "$BRANCH:out.txt")" == "harness-e2e" ]] || fail "the branch does not carry the agent's file"
[[ "$(git --git-dir="$REPOS/acme/widgets.git" rev-list --count main)" == "1" ]] || fail "main moved"
[[ "$(git --git-dir="$REPOS/acme/widgets.git" log -1 --format=%ae "$BRANCH")" == "$ADMIN_EMAIL" ]] || fail "the commit is not signed by the user"
grep -q "merge request created $BRANCH -> main: Draft: Add the marker" "$WORK/codehost.log" || fail "no merge request"

log "the token stayed out of the host's files and argv"
if grep -rlF "$GIT_TOKEN" "$WORK/host-root" "$WORK/host-home" 2>/dev/null; then fail "the git token is on the host's disk"; fi
if grep -F "$GIT_TOKEN" "$WORK/daemon.log"; then fail "the git token is in the log"; fi
if grep -F "$HOST_TOKEN" "$WORK/daemon.log"; then fail "the device token is in the log"; fi
if grep -rlF "$HOST_TOKEN" "$WORK/host-root" "$WORK/host-home" 2>/dev/null; then fail "the device token is on the host's disk"; fi

log "the session is there to watch and steer through the relay"
HOSTS="$(rpc relay.v1.RelayService/ListHosts '{}')"
for _ in $(seq 1 20); do
  HOSTS="$(rpc relay.v1.RelayService/ListHosts '{}')"
  jq -e --arg s "$SESSION" '[.hosts[] | select(.online) | .sessions[]? | .id] | index($s)' <<<"$HOSTS" >/dev/null && break
  sleep 0.5
done
jq -e --arg s "$SESSION" '[.hosts[] | select(.online) | .sessions[]? | .id] | index($s)' <<<"$HOSTS" >/dev/null || fail "the session is not on the relay: $HOSTS $(grep -c . "$WORK/daemon.log")"
STEER="$(rpc relay.v1.RelayService/SendUserMessage "$(jq -n --arg s "$SESSION" --arg u "$(uuidgen | tr 'A-Z' 'a-z')" '{request:{sessionId:$s,uuid:$u,content:[{text:{text:"anything else?"}}]},options:{waitForAck:true}}')")" || fail "steer"
[[ "$(jq -r .result.delivery <<<"$STEER")" == "DELIVERY_STATE_ACKED" ]] || fail "the message was not delivered: $STEER"

log "load and capacity show"
SHOWN="$(rpc relay.v1.HostsService/ListAgentHosts '{"all":true}')" || fail "admin list"
jq -e --arg id "$HOST_ID" '.hosts[] | select(.id==$id) | .maxSessions == 2 and .isolation == "AGENT_HOST_ISOLATION_NONE"' <<<"$SHOWN" >/dev/null || fail "admin view: $SHOWN"

log "the daemon stops on SIGTERM"
kill -TERM "$DAEMON"
for _ in $(seq 1 40); do kill -0 "$DAEMON" 2>/dev/null || break; sleep 0.25; done
if kill -0 "$DAEMON" 2>/dev/null; then fail "the daemon did not stop"; fi

log "hosts e2e passed"
