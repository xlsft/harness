#!/usr/bin/env bash
# Builds harness-client for the host as the Tauri sidecar: src-tauri/binaries/harness-client-<target triple>.
# Tauri copies it next to the app executable (dev and bundle), where the shell starts it. On macOS, also the native
# helper of computer use (packages/client/computeruse/helper), listed in tauri.macos.conf.json.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
triple="${TAURI_TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}"
ext=""
[[ "$triple" == *windows* ]] && ext=".exe"
(cd "$here/../client" && CGO_ENABLED=0 go build -trimpath -o "$here/src-tauri/binaries/harness-client-$triple$ext" ./cmd/harness-client)
echo "sidecar: src-tauri/binaries/harness-client-$triple$ext"
helper=""
if [[ "$triple" == *apple-darwin* ]]; then
  helper="harness-computer-helper"
  case "$triple" in
    aarch64-*) swift_target="arm64-apple-macos12" ;;
    *) swift_target="x86_64-apple-macos12" ;;
  esac
  bash "$here/../client/computeruse/helper/build.sh" "$here/src-tauri/binaries/$helper-$triple" "$swift_target"
  echo "sidecar: src-tauri/binaries/$helper-$triple"
fi
# `tauri dev` copies external binaries only when the shell itself is rebuilt; refresh the copy it runs.
for dir in "$here/src-tauri/target/debug" "$here/src-tauri/target/$triple/debug"; do
  if [[ -d "$dir" ]]; then
    cp "$here/src-tauri/binaries/harness-client-$triple$ext" "$dir/harness-client$ext"
    echo "sidecar: refreshed ${dir#$here/}/harness-client$ext"
    if [[ -n "$helper" ]]; then
      cp "$here/src-tauri/binaries/$helper-$triple" "$dir/$helper"
    fi
  fi
done
