#!/usr/bin/env bash
# Passes when createUser takes an options object everywhere and every caller keeps its behavior.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if grep -rnE 'isAdmin' src >/dev/null; then
  echo "FAIL: isAdmin is still referenced:" >&2; grep -rn isAdmin src >&2; exit 1
fi
if grep -rnE '(createUser|mk)\([[:space:]]*("|[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*,)' src test | grep -v 'function ' >/dev/null; then
  echo "FAIL: positional createUser calls remain:" >&2
  grep -rnE '(createUser|mk)\([[:space:]]*("|[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*,)' src test | grep -v 'function ' >&2; exit 1
fi
cp -R "$TASK_DIR/hidden/." .
node --test test/*.test.ts
