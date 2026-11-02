#!/usr/bin/env bash
# Passes when the single bad entry is fixed, every other entry is intact and the file was edited
# in place (not rewritten or truncated).
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GOTOOLCHAIN=local GOFLAGS=-count=1 GOPROXY=off
changed="$(git diff --numstat -- codes/codes.go | awk '{print $1+$2}')"
if [ -z "$changed" ] || [ "$changed" -gt 2 ]; then
  echo "FAIL: expected a one-line edit of codes/codes.go, got ${changed:-no} changed lines" >&2
  exit 1
fi
if [ -n "$(git status --porcelain -- . ':!codes/codes.go' | grep -v '^??' || true)" ]; then
  echo "FAIL: files other than codes/codes.go were modified" >&2
  git status --porcelain >&2
  exit 1
fi
cp -R "$TASK_DIR/hidden/." .
go test ./...
