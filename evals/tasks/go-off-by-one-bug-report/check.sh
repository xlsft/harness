#!/usr/bin/env bash
# Passes when the hidden regression test passes and the agent added or changed a test file.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GOTOOLCHAIN=local GOFLAGS=-count=1 GOPROXY=off
if [ -z "$(git status --porcelain --untracked-files=all -- '*_test.go')" ]; then
  echo "FAIL: no regression test was added or changed" >&2
  exit 1
fi
cp -R "$TASK_DIR/hidden/." .
go vet ./...
go test ./...
