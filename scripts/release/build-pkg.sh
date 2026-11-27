#!/usr/bin/env bash
# Builds the macOS installer package: the app in /Applications and, if given, a server URL in the machine's policy folder
# (/Library/Application Support/harness/settings.d/00-server.json, read like the profile's serverUrl, written by the
# package's postinstall script). The release builds
# it without a URL next to each DMG; an administrator builds their own from a release's .app.tar.gz with one
# (docs/enterprise.md). Needs macOS: pkgbuild, productbuild, codesign. Installs nothing.
#
# Usage: build-pkg.sh <the .app, or its .app.tar.gz> <output .pkg> [--server-url URL] [--version X.Y.Z] [--notarize]
#
# Signing, like the app's (docs/release.md), by which values exist:
#   APPLE_INSTALLER_CERTIFICATE (base64 .p12), APPLE_INSTALLER_CERTIFICATE_PASSWORD, APPLE_INSTALLER_IDENTITY
#       sign with a Developer ID Installer certificate; without them the package is unsigned.
#   --notarize with APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID: notarize and staple the signed package. Only for an app that
#       is itself notarized; Apple refuses a package with an ad-hoc app inside.
# Logs go to stderr; the last line on stdout is how the package came out: unsigned, signed or notarized.
set -euo pipefail
usage="usage: build-pkg.sh <app or .app.tar.gz> <output .pkg> [--server-url URL] [--version X.Y.Z] [--notarize]"
src="${1:?$usage}"
out="${2:?$usage}"
shift 2
server_url="" version="" notarize=false
while (($#)); do
  case "$1" in
    --server-url) server_url="${2:?$usage}"; shift 2 ;;
    --version) version="${2:?$usage}"; shift 2 ;;
    --notarize) notarize=true; shift ;;
    *) echo "$usage" >&2; exit 2 ;;
  esac
done
for tool in pkgbuild productbuild codesign plutil; do
  command -v "$tool" >/dev/null || { echo "build-pkg.sh needs macOS: no $tool" >&2; exit 1; }
done
if [[ -n "$server_url" ]]; then
  # It goes into JSON as it is: a plain http(s) URL, nothing that would need escaping.
  [[ "$server_url" =~ ^https?://[A-Za-z0-9.:@%/_~+-]+$ ]] || { echo "--server-url must be an http(s) URL: $server_url" >&2; exit 2; }
  server_url="${server_url%/}"
fi
for name in APPLE_INSTALLER_CERTIFICATE APPLE_INSTALLER_CERTIFICATE_PASSWORD APPLE_INSTALLER_IDENTITY APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID; do
  [[ -n "${!name:-}" ]] || unset "$name"
done

work="$(mktemp -d "${TMPDIR:-/tmp}/harness-pkg.XXXXXX")"
keychain=""
cleanup() {
  if [[ -n "$keychain" ]]; then
    security delete-keychain "$keychain" >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

# The payload is the app alone, installed into /Applications: a payload holding /Applications or /Library itself would
# set their owners and modes.
root="$work/root"
mkdir -p "$root"
case "$src" in
  *.tar.gz) tar -xzf "$src" -C "$root" ;;
  *.app | *.app/) ditto "$src" "$root/$(basename "${src%/}")" ;;
  *) echo "$usage" >&2; exit 2 ;;
esac
app="$(find "$root" -mindepth 1 -maxdepth 1 -name '*.app' -print -quit)"
[[ -n "$app" ]] || { echo "no .app in $src" >&2; exit 1; }
[[ "$(find "$root" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" == 1 ]] || { echo "$src holds more than the app" >&2; exit 1; }
codesign --verify --deep --strict "$app" >&2

info="$app/Contents/Info.plist"
plist() { plutil -extract "$1" raw -o - "$info"; }
bundle_id="$(plist CFBundleIdentifier)"
[[ -n "$version" ]] || version="$(plist CFBundleShortVersionString)"
min_os="$(plist LSMinimumSystemVersion 2>/dev/null || echo 10.13)"
exe="$app/Contents/MacOS/$(plist CFBundleExecutable)"
case "$(lipo -archs "$exe" 2>/dev/null || file -b "$exe")" in
  *arm64*x86_64* | *x86_64*arm64*) hosts="arm64,x86_64" ;;
  *arm64*) hosts="arm64" ;;
  *x86_64*) hosts="x86_64" ;;
  *) echo "can't tell the architecture of $exe" >&2; exit 1 ;;
esac

# The server URL is written by a script after the payload, into the system folder of the machine policy, so that the
# folders above it keep their owners. $3 is the target volume.
scripts=()
if [[ -n "$server_url" ]]; then
  mkdir -p "$work/scripts"
  cat >"$work/scripts/postinstall" <<EOF
#!/bin/sh
set -e
dir="\${3%/}/Library/Application Support/harness/settings.d"
mkdir -p "\$dir"
printf '%s\n' '{"serverUrl": "$server_url"}' >"\$dir/00-server.json"
chmod 644 "\$dir/00-server.json"
EOF
  chmod 755 "$work/scripts/postinstall"
  scripts=(--scripts "$work/scripts")
fi

# The app goes where the package says, not to wherever another copy of the same bundle id happens to be.
pkgbuild --analyze --root "$root" "$work/component.plist" >&2
python3 -I -c '
import plistlib, sys
path = sys.argv[1]
with open(path, "rb") as f:
    items = plistlib.load(f)
for item in items:
    item["BundleIsRelocatable"] = False
with open(path, "wb") as f:
    plistlib.dump(items, f)
' "$work/component.plist"

id="$bundle_id.pkg"
mkdir -p "$work/packages"
pkgbuild --root "$root" --component-plist "$work/component.plist" --identifier "$id" --version "$version" \
  --install-location /Applications --ownership recommended ${scripts[@]+"${scripts[@]}"} "$work/packages/harness.pkg" >&2

cat >"$work/distribution.xml" <<EOF
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>xlsft\`s harness</title>
  <options customize="never" hostArchitectures="$hosts" />
  <domains enable_localSystem="true" enable_anywhere="false" enable_currentUserHome="false" />
  <volume-check>
    <allowed-os-versions>
      <os-version min="$min_os" />
    </allowed-os-versions>
  </volume-check>
  <choices-outline>
    <line choice="default">
      <line choice="$id" />
    </line>
  </choices-outline>
  <choice id="default" />
  <choice id="$id" visible="false">
    <pkg-ref id="$id" />
  </choice>
  <pkg-ref id="$id" version="$version" onConclusion="none">harness.pkg</pkg-ref>
</installer-gui-script>
EOF

sign=()
signing=unsigned
if [[ -n "${APPLE_INSTALLER_IDENTITY:-}" ]]; then
  if [[ -n "${APPLE_INSTALLER_CERTIFICATE:-}" ]]; then
    # A keychain of this run only, as tauri does for the app's certificate.
    keychain="$work/installer.keychain-db"
    pass="$(openssl rand -hex 24)"
    security create-keychain -p "$pass" "$keychain"
    security set-keychain-settings -lut 3600 "$keychain"
    security unlock-keychain -p "$pass" "$keychain"
    printf '%s' "$APPLE_INSTALLER_CERTIFICATE" | base64 --decode >"$work/installer.p12"
    security import "$work/installer.p12" -k "$keychain" -P "${APPLE_INSTALLER_CERTIFICATE_PASSWORD:-}" -T /usr/bin/productbuild -T /usr/bin/productsign >&2
    rm "$work/installer.p12"
    security set-key-partition-list -S apple-tool:,apple: -s -k "$pass" "$keychain" >/dev/null
    sign=(--sign "$APPLE_INSTALLER_IDENTITY" --keychain "$keychain")
  else
    sign=(--sign "$APPLE_INSTALLER_IDENTITY")
  fi
  signing=signed
fi
mkdir -p "$(dirname "$out")"
productbuild --distribution "$work/distribution.xml" --package-path "$work/packages" ${sign[@]+"${sign[@]}"} "$out" >&2

if [[ "$notarize" == true ]]; then
  if [[ "$signing" != signed ]]; then
    echo "not notarizing: the package isn't signed (no APPLE_INSTALLER_IDENTITY)" >&2
  elif [[ -z "${APPLE_ID:-}" || -z "${APPLE_PASSWORD:-}" || -z "${APPLE_TEAM_ID:-}" ]]; then
    echo "not notarizing: APPLE_ID, APPLE_PASSWORD and APPLE_TEAM_ID are needed" >&2
  else
    xcrun notarytool submit "$out" --apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID" --wait >&2
    xcrun stapler staple "$out" >&2
    signing=notarized
  fi
fi
pkgutil --check-signature "$out" >&2 || true
echo "built $out: $bundle_id $version for $hosts ($signing)${server_url:+, server $server_url}" >&2
echo "$signing"
