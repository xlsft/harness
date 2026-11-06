#!/usr/bin/env bash
# Puts a release together from what the builds left in a directory: notes, latest.json, a source SBOM, checksums.txt and
# its signature with the updater key (what `harness-client update` verifies). Publishes nothing.
#
# Usage: assemble.sh <tag> <directory> <work directory>   Leaves notes.md, changes.md and signing in the work directory.
# Needs TAURI_SIGNING_PRIVATE_KEY (and _PASSWORD, if the key has one), node, npx; syft for the SBOM.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tag="${1:?usage: assemble.sh <tag> <directory> <work directory>}"
dist="${2:?usage: assemble.sh <tag> <directory> <work directory>}"
work="${3:?usage: assemble.sh <tag> <directory> <work directory>}"
version="${tag#v}"

# Each desktop build says how it was signed. Both ran with the same secrets, so a disagreement is a bug worth stopping for.
shopt -s nullglob
signing=""
for f in "$dist"/signing*.txt; do
  s="$(cat "$f")"
  [[ -z "$signing" || "$signing" == "$s" ]] || { echo "desktop builds disagree on signing: $signing, $s" >&2; exit 1; }
  signing="$s"
  rm "$f"
done
[[ -n "$signing" ]] || { echo "no signing.txt from a desktop build in $dist" >&2; exit 1; }
echo "$signing" >"$work/signing"

for need in "harness_${version}_darwin_arm64.dmg" "harness_${version}_darwin_amd64.dmg" "harness-client_${version}_darwin_arm64.tar.gz" "harness-client_${version}_darwin_amd64.tar.gz"; do
  [[ -f "$dist/$need" ]] || { echo "$need is missing from $dist" >&2; exit 1; }
done

"$root/scripts/release/notes.sh" changes "$tag" >"$work/changes.md"
"$root/scripts/release/notes.sh" full "$tag" "$signing" >"$work/notes.md"
node "$root/scripts/release/latest-json.mjs" "$tag" "$dist" "$work/changes.md"

# The lockfiles of everything that went in: the app has no single binary an SBOM tool can read its dependencies from.
if command -v syft >/dev/null; then
  syft scan "dir:$root/packages" --exclude '**/node_modules/**' --exclude '**/target/**' -o spdx-json >"$dist/harness_${version}_source.sbom.json"
else
  echo "no syft: no source SBOM" >&2
  [[ -z "${CI:-}" ]] || exit 1
fi

# checksums.txt lists everything else, with bare names, so `shasum -c` works in a folder of downloads.
(
  cd "$dist"
  rm -f checksums.txt checksums.txt.sig
  files=(*)
  shasum -a 256 "${files[@]}" >checksums.txt
)
cli_version="$(node -p "require('$root/packages/desktop/package.json').devDependencies['@tauri-apps/cli']")"
npx --yes "@tauri-apps/cli@$cli_version" signer sign --app-version "$version" "$dist/checksums.txt"
[[ -s "$dist/checksums.txt.sig" ]] || { echo "checksums.txt wasn't signed" >&2; exit 1; }
ls -la "$dist"
