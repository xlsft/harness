#!/usr/bin/env bash
# Copies catalog/ into the client, which embeds it as the built-in marketplace (Go can't embed from outside its module).
# packages/client/extensions/builtin_test.go fails when the copy is stale.
set -euo pipefail
cd "$(dirname "$0")/.."
dest=packages/client/extensions/builtin
rm -rf "$dest"
mkdir -p "$dest"
cp -R catalog/. "$dest/"
