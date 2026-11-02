#!/usr/bin/env bash
# Passes when the final answer is 335 and no file was modified.
set -euo pipefail
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
  echo "FAIL: the working tree was modified:" >&2
  git status --porcelain --untracked-files=all >&2
  exit 1
fi
RESULT="${EVAL_RESULT_FILE:-}"
[ -s "$RESULT" ] || { echo "FAIL: no final answer text" >&2; exit 1; }
answer="$(grep -Eo 'ANSWER:[[:space:]]*[0-9]+' "$RESULT" | tail -n 1 | grep -Eo '[0-9]+' || true)"
if [ "$answer" != "335" ]; then
  echo "FAIL: expected ANSWER: 335, got '${answer:-<none>}'" >&2
  exit 1
fi
echo "ok: ANSWER: 335"
