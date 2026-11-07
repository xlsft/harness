#!/usr/bin/env bash
# Passes when the answer is right, the hidden sheet is mentioned and no file changed.
set -euo pipefail
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
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
if [ "$answer" != "4217" ]; then
  echo "FAIL: expected ANSWER: 4217, got '${answer:-<none>}'" >&2
  exit 1
fi
if ! grep -qi 'hidden' "$RESULT"; then
  echo "FAIL: the answer does not say the sheet is hidden" >&2
  exit 1
fi
echo "ok"
