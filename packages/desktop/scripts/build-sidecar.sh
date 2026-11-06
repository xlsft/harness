#!/usr/bin/env bash
# Builds harness-client for the host as the Tauri sidecar: src-tauri/binaries/harness-client-<target triple>.
# Tauri copies it next to the app executable (dev and bundle), where the shell starts it. On macOS, also the native
# helper of computer use (packages/client/computeruse/helper), listed in tauri.macos.conf.json.
# TAURI_TARGET_TRIPLE builds for another architecture; HARNESS_VERSION stamps a release version (the app's own, see
# docs/release.md); the stamped Distribution makes `harness-client update` refuse, as the app updates the sidecar.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
triple="${TAURI_TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}"
ext=""
[[ "$triple" == *windows* ]] && ext=".exe"
case "$triple" in
  aarch64-*) goarch=arm64 ;;
  x86_64-*) goarch=amd64 ;;
  *) echo "unsupported target $triple" >&2; exit 1 ;;
esac
case "$triple" in
  *apple-darwin*) goos=darwin ;;
  *windows*) goos=windows ;;
  *linux*) goos=linux ;;
  *) echo "unsupported target $triple" >&2; exit 1 ;;
esac
module=github.com/xlsft/harness/packages/client
ldflags="-X $module/config.Distribution=desktop"
[[ -n "${HARNESS_VERSION:-}" ]] && ldflags="-s -w -X $module/config.Version=$HARNESS_VERSION $ldflags"
(cd "$here/../client" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$here/src-tauri/binaries/harness-client-$triple$ext" ./cmd/harness-client)
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
# Remove first: copying over a binary that has already run keeps its inode, macOS still holds the old
# code signature for that inode, and kills every later run of the new file (exit 137, no output).
for dir in "$here/src-tauri/target/debug" "$here/src-tauri/target/$triple/debug"; do
  if [[ -d "$dir" ]]; then
    rm -f "$dir/harness-client$ext"
    cp "$here/src-tauri/binaries/harness-client-$triple$ext" "$dir/harness-client$ext"
    echo "sidecar: refreshed ${dir#$here/}/harness-client$ext"
    if [[ -n "$helper" ]]; then
      rm -f "$dir/$helper"
      cp "$here/src-tauri/binaries/$helper-$triple" "$dir/$helper"
    fi
  fi
done
