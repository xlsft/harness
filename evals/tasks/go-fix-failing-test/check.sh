#!/usr/bin/env bash
# Passes when the original tests (restored from hidden/) pass.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GOTOOLCHAIN=local GOFLAGS=-count=1 GOPROXY=off
cp -R "$TASK_DIR/hidden/." .
go vet ./...
go test ./...
