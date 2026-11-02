#!/usr/bin/env bash
# Local development stack: builds and starts core, signs in the client, and (optionally) configures
# OpenRouter as the model provider with a set of cheap, tool-capable models for testing.
#
# Usage:
#   OPENROUTER_API_KEY=sk-or-... scripts/dev/stack.sh      # start + configure OpenRouter models
#   scripts/dev/stack.sh                                    # start only (no provider)
#   scripts/dev/stack.sh stop                               # stop the stack started by this script
#
# State lives in packages/core/.data-dev (server) and .dev/home (client), both git-ignored.
# The API key is only sent to the local server's admin API (stored there encrypted); it is
# never written to the repository.
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

if [[ -n "${OPENROUTER_API_KEY:-}" ]]; then
  log "configuring OpenRouter providers"
  provider() { # id type base_url
    local body
    body="$(jq -n --arg id "$1" --arg t "$2" --arg u "$3" --arg k "$OPENROUTER_API_KEY" \
      '{id:$id,type:$t,display_name:("OpenRouter (" + $t + ")"),base_url:$u,api_key:$k}')"
    "${CURL[@]}" "${JSON[@]}" -d "$body" "$URL/api/admin/providers" >/dev/null 2>&1 \
      || "${CURL[@]}" "${JSON[@]}" -X PATCH -d "$body" "$URL/api/admin/providers/$1" >/dev/null 2>&1 \
      || warn "provider $1 ($2) not accepted by this server version"
  }
  provider openrouter-anthropic anthropic https://openrouter.ai/api
  provider openrouter-chat openai-chat https://openrouter.ai/api/v1
  provider openrouter-responses openai-responses https://openrouter.ai/api/v1

  log "configuring test models"
  model() { # json
    local id; id="$(jq -r .id <<<"$1")"
    "${CURL[@]}" "${JSON[@]}" -d "$1" "$URL/api/admin/models" >/dev/null 2>&1 \
      || "${CURL[@]}" "${JSON[@]}" -X PATCH -d "$1" "$URL/api/admin/models/$id" >/dev/null 2>&1 \
      || warn "model $id not accepted (is its provider configured?)"
  }
  caps='"images":true,"pdf":false,"parallel_tool_calls":true'
  model '{"id":"claude-haiku","display_name":"Claude Haiku 5.5","provider_id":"openrouter-anthropic","upstream_model":"anthropic/claude-haiku-5.5","context_window":200000,"max_output_tokens":16000,"capabilities":{'"$caps"',"thinking":true,"prompt_cache":true,"effort":[]},"pricing":{"input":0.10,"output":0.50}}'
  model '{"id":"gpt-luna","display_name":"GPT-6 Luna","provider_id":"openrouter-responses","upstream_model":"openai/gpt-6-luna","context_window":400000,"max_output_tokens":32000,"capabilities":{'"$caps"',"thinking":true,"prompt_cache":true,"effort":["low","medium","high"]},"pricing":{"input":0.10,"output":0.50}}'
  model '{"id":"deepseek-flash","display_name":"DeepSeek V4.1 Flash","provider_id":"openrouter-chat","upstream_model":"deepseek/deepseek-v4.1-flash","context_window":1000000,"max_output_tokens":32000,"capabilities":{"images":false,"pdf":false,"parallel_tool_calls":true,"thinking":true,"prompt_cache":true,"effort":[]},"pricing":{"input":0.045,"output":0.50}}'
  model '{"id":"qwen-flash","display_name":"Qwen 3.7 Flash","provider_id":"openrouter-chat","upstream_model":"qwen/qwen3.7-flash","context_window":1000000,"max_output_tokens":32000,"capabilities":{"images":false,"pdf":false,"parallel_tool_calls":true,"thinking":false,"prompt_cache":false,"effort":[]},"pricing":{"input":0.03,"output":0.13}}'
  model '{"id":"glm-flash","display_name":"GLM 5.3 Flash","provider_id":"openrouter-chat","upstream_model":"z-ai/glm-5.3-flash","context_window":1000000,"max_output_tokens":32000,"capabilities":{"images":false,"pdf":false,"parallel_tool_calls":true,"thinking":false,"prompt_cache":false,"effort":[]},"pricing":{"input":0.029,"output":0.092}}'
  "${CURL[@]}" "${JSON[@]}" -X PUT -d '{"main":"claude-haiku","small":"glm-flash","plan":"claude-haiku"}' \
    "$URL/api/admin/slots" >/dev/null || warn "slots not set"
fi

log "ready"
echo "  server:   $URL  (admin: $ADMIN_EMAIL, password in .dev/admin-password)"
echo "  client:   HARNESS_HOME=$DEV/home HARNESS_KEYRING=0 $DEV/harness-client -p \"...\" --model <id>"
echo "  models:   $("$DEV/harness-client" whoami >/dev/null && curl -fsS -b "$JAR" "$URL/api/admin/models" | jq -r '[.models[]?.id // .[]?.id] | join(", ")' 2>/dev/null || echo '?')"
echo "  stop:     scripts/dev/stack.sh stop"
