#!/usr/bin/env bash
# Passes when createUser takes an options object everywhere and every caller keeps its behavior.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if grep -rnE 'isAdmin' src >/dev/null; then
  echo "FAIL: isAdmin is still referenced:" >&2; grep -rn isAdmin src >&2; exit 1
fi
if grep -rnE '(createUser|mk)\([[:space:]]*("|[a-z])' src test | grep -v '({' >/dev/null; then
  echo "FAIL: positional createUser calls remain:" >&2
  grep -rnE '(createUser|mk)\([[:space:]]*("|[a-z])' src test | grep -v '({' >&2; exit 1
fi
cp -R "$TASK_DIR/hidden/." .
node --test test/*.test.ts
