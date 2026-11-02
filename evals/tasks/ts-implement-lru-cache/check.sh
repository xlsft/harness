#!/usr/bin/env bash
# Passes when the visible and hidden LRU tests pass.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cp -R "$TASK_DIR/hidden/." .
node --test test/*.test.ts
