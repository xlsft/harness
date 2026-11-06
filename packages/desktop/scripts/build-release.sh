#!/usr/bin/env bash
# Builds the macOS app as a release: sidecar and computer-use helper, bundle, signing, updater archive. Publishes nothing.
#
# Usage: build-release.sh <target triple> <output directory>
#   HARNESS_VERSION                 the version; the app's own (tauri.conf.json, stamped by scripts/release/stamp.mjs) if unset
#   TAURI_SIGNING_PRIVATE_KEY[_PASSWORD]  signs the updater archive; without a key there is no archive (a pull request's build)
#   APPLE_CERTIFICATE[_PASSWORD], APPLE_SIGNING_IDENTITY   a real signature with a Developer ID
#   APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID   notarization on top of a Developer ID
#
# Whichever Apple values are missing, the release still builds. With no certificate, the whole bundle is signed ad-hoc
# (signingIdentity "-"), which Apple Silicon requires of every binary to run at all. docs/release.md says what each level
# means for whoever opens the app. Leaves <output>/signing.txt saying which one it got.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
triple="${1:?usage: build-release.sh <target triple> <output directory>}"
out="${2:?usage: build-release.sh <target triple> <output directory>}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
conf="$here/src-tauri/tauri.conf.json"
version="${HARNESS_VERSION:-$(node -p "require('$conf').version")}"
case "$triple" in
  aarch64-apple-darwin) arch=arm64 ;;
  x86_64-apple-darwin) arch=amd64 ;;
  *) echo "only macOS targets are released, not $triple" >&2; exit 1 ;;
esac

# An empty value (an unset secret, in a workflow) counts as missing: tauri would try to use it.
for name in APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_SIGNING_IDENTITY APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD; do
  [[ -n "${!name:-}" ]] || unset "$name"
done

config='{}'
if [[ -n "${APPLE_SIGNING_IDENTITY:-}" ]]; then
  if [[ -n "${APPLE_ID:-}" && -n "${APPLE_PASSWORD:-}" && -n "${APPLE_TEAM_ID:-}" ]]; then
    signing=notarized
  else
    signing=signed
    unset APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID
  fi
else
  signing=unsigned
  unset APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID
  config='{"bundle":{"macOS":{"signingIdentity":"-"}}}'
fi

updater=false
if [[ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]]; then
  if [[ -z "$(node -p "require('$conf').plugins.updater.pubkey")" ]]; then
    echo "TAURI_SIGNING_PRIVATE_KEY is set, but plugins.updater.pubkey in tauri.conf.json is empty: see docs/release.md" >&2
    exit 1
  fi
  updater=true
  config="$(node -p "JSON.stringify({...$config, bundle: {...$config.bundle, createUpdaterArtifacts: true}})")"
  # tauri asks for the password even when the key has none.
  export TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
fi

HARNESS_VERSION="$version" TAURI_TARGET_TRIPLE="$triple" bash "$here/scripts/build-sidecar.sh"

# The DMG's window (background, icon size and positions) is laid out by Finder through AppleScript, and tauri skips that when
# CI=true unless told otherwise. It's also unreliable: about one run in three, on any Mac, Finder saves the window without its
# background and with 48 px icons, and nothing reports it. So: check every result, and make the DMG again until it's right.
export TAURI_BUNDLER_DMG_IGNORE_CI=true
bundle="$here/src-tauri/target/$triple/release/bundle"
(cd "$here" && pnpm exec tauri build --target "$triple" --bundles app,dmg --config "$config")
built=false
for attempt in 1 2 3 4 5 6 7 8; do
  dmg="$(find "$bundle/dmg" -maxdepth 1 -name '*.dmg' -print -quit)"
  if [[ -n "$dmg" ]] && python3 -I "$here/scripts/check-dmg.py" "$dmg" "$conf"; then
    built=true
    break
  fi
  echo "attempt $attempt didn't make a proper DMG; making it again" >&2
  find "$bundle/dmg" -maxdepth 1 -name '*.dmg' -delete
  (cd "$here" && pnpm exec tauri bundle --target "$triple" --bundles dmg --config "$config") || true
done
[[ "$built" == true ]] || { echo "no DMG with its layout after 8 attempts: see docs/release.md" >&2; exit 1; }

app="$(find "$bundle/macos" -maxdepth 1 -name '*.app' -print -quit)"
[[ -n "$app" ]] || { echo "no .app in $bundle/macos" >&2; exit 1; }

# Every executable has to be signed, the two sidecars included, and the bundle has to hold together.
codesign --verify --deep --strict "$app"
for bin in "$app"/Contents/MacOS/*; do
  codesign --verify --strict "$bin"
  echo "signed: $(basename "$bin"): $(codesign -dvv "$bin" 2>&1 | grep -E '^(Signature|Authority)=' | head -n1)"
done
[[ -x "$app/Contents/MacOS/harness-client" ]] || { echo "no sidecar in the bundle" >&2; exit 1; }
[[ -x "$app/Contents/MacOS/harness-computer-helper" ]] || { echo "no computer-use helper in the bundle" >&2; exit 1; }
if [[ "$(uname -m)" == "${triple%%-*}" || "$(uname -m)-$arch" == arm64-arm64 ]]; then
  "$app/Contents/MacOS/harness-client" version | grep -qF "harness-client/$version" || { echo "the sidecar isn't version $version" >&2; exit 1; }
fi

# Names of our own: the product name has a backtick and a space, which GitHub would mangle.
base="harness_${version}_darwin_${arch}"
find "$bundle/dmg" -maxdepth 1 -name '*.dmg' -exec cp {} "$out/$base.dmg" \;
[[ -f "$out/$base.dmg" ]] || { echo "no .dmg in $bundle/dmg" >&2; exit 1; }
if [[ "$updater" == true ]]; then
  find "$bundle/macos" -maxdepth 1 -name '*.app.tar.gz' -exec cp {} "$out/$base.app.tar.gz" \;
  find "$bundle/macos" -maxdepth 1 -name '*.app.tar.gz.sig' -exec cp {} "$out/$base.app.tar.gz.sig" \;
  [[ -s "$out/$base.app.tar.gz.sig" ]] || { echo "no signed updater archive in $bundle/macos" >&2; exit 1; }
fi
echo "$signing" >"$out/signing.txt"
echo "built $version for $triple ($signing): $out"
