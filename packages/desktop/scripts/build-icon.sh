#!/usr/bin/env bash
# Compiles the Icon Composer icon (src-tauri/icons/icon.icon) into Assets.car, which the bundle ships so macOS 26
# draws the app icon in the light, dark, tinted and clear styles. The bundler's own compile step passes
# --notices/--warnings, which makes actool 26.6 crash on .icon files, so the catalog is built here and committed.
# Needs Xcode 26. Run after changing the icon.
set -euo pipefail
cd "$(dirname "$0")/../src-tauri/icons"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp -R icon.icon "$tmp/Icon.icon"
mkdir "$tmp/out"
xcrun actool "$tmp/Icon.icon" --compile "$tmp/out" --platform macosx --minimum-deployment-target 26.0 \
  --target-device mac --app-icon Icon --include-all-app-icons \
  --output-partial-info-plist "$tmp/out/info.plist" >/dev/null
cp "$tmp/out/Assets.car" Assets.car
echo "wrote $(pwd)/Assets.car"
