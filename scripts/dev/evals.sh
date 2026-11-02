#!/usr/bin/env bash
# Runs the eval suite (evals/) against the local dev stack started by scripts/dev/stack.sh.
#
# Usage:
#   scripts/dev/evals.sh                                  # all tasks, the default test models
#   scripts/dev/evals.sh -models claude-haiku -repeat 3   # extra flags go to evals/run
#   EVAL_MODELS=qwen-flash,glm-flash scripts/dev/evals.sh
#
# Results land in evals/results/<timestamp>/ (results.md, results.json, per-attempt logs).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEV="$ROOT/.dev"
MODELS="${EVAL_MODELS:-claude-haiku,gpt-luna,deepseek-flash,qwen-flash,glm-flash}"

[[ -x "$DEV/harness-client" ]] || { echo "run scripts/dev/stack.sh first" >&2; exit 1; }
export HARNESS_HOME="$DEV/home" HARNESS_KEYRING=0
"$DEV/harness-client" whoami >/dev/null || { echo "client is not signed in; run scripts/dev/stack.sh" >&2; exit 1; }

cd "$ROOT/evals"
exec go run ./run -client "$DEV/harness-client" -models "$MODELS" -parallel "${EVAL_PARALLEL:-3}" "$@"
