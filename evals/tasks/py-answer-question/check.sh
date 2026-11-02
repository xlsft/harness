#!/usr/bin/env bash
# Passes when the agent's final answer is 5 and no file was modified.
set -euo pipefail
if [ -n "$(git status --porcelain --untracked-files=all | grep -v '__pycache__' || true)" ]; then
  echo "FAIL: the working tree was modified:" >&2
  git status --porcelain --untracked-files=all >&2
  exit 1
fi
RESULT="${EVAL_RESULT_FILE:-}"
if [ -z "$RESULT" ] || [ ! -s "$RESULT" ]; then
  echo "FAIL: no final answer text (EVAL_RESULT_FILE is empty or missing)" >&2
  exit 1
fi
answer="$(grep -Eo 'ANSWER:[[:space:]]*[0-9]+' "$RESULT" | tail -n 1 | grep -Eo '[0-9]+' || true)"
if [ "$answer" != "5" ]; then
  echo "FAIL: expected ANSWER: 5, got '${answer:-<none>}'" >&2
  exit 1
fi
echo "ok: ANSWER: 5"
