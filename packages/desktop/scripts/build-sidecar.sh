#!/usr/bin/env bash
# Builds harness-client for the host as the Tauri sidecar: src-tauri/binaries/harness-client-<target triple>.
# Tauri copies it next to the app executable (dev and bundle), where the shell starts it.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
triple="${TAURI_TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}"
ext=""
[[ "$triple" == *windows* ]] && ext=".exe"
(cd "$here/../client" && CGO_ENABLED=0 go build -trimpath -o "$here/src-tauri/binaries/harness-client-$triple$ext" ./cmd/harness-client)
echo "sidecar: src-tauri/binaries/harness-client-$triple$ext"
