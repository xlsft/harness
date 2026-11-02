#!/usr/bin/env bash
# Passes when the visible and hidden unittest suites pass.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cp -R "$TASK_DIR/hidden/." .
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -t . -v
