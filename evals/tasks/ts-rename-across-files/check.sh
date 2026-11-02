#!/usr/bin/env bash
# Passes when no reference to calcTotal remains and the tests (plus a hidden one) pass.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if grep -rn 'calcTotal' src test; then
  echo "FAIL: calcTotal is still referenced" >&2
  exit 1
fi
grep -q 'export function computeOrderTotal' src/pricing.ts || { echo "FAIL: computeOrderTotal is not defined in src/pricing.ts" >&2; exit 1; }
cp -R "$TASK_DIR/hidden/." .
node --test test/*.test.ts
