#!/usr/bin/env bash
# Puts a published release into a folder an administrator can carry into a network without internet access and serve as
# the update mirror (updateMirrorUrl, deploy/mdm/keys.md). docs/enterprise.md is the manual.
#
#   <out>/latest.json        the release's latest.json, with its download URLs pointing into the mirror
#   <out>/<tag>/...          every file of the release, as published: apps, packages, client archives, checksums, signatures
#   <out>/images/...         the server image as docker save tarballs, one per platform
#   <out>/SHA256SUMS         every file above, for copying it across
#
# Usage: offline-bundle.sh <tag> <out> --base-url <URL the folder will be served at>
#          [--from <folder with the release's files>]   instead of downloading them with gh
#          [--platform linux/amd64[,linux/arm64]]        image platforms (default linux/amd64)
#          [--no-image]                                  leave the image out
#          [--keep-latest]                               add the release without making it the one updates offer
# Several releases can go into one folder; <out>/latest.json names the one updates install.
#
# Verifies the release's checksums.txt (cosign, when installed, also checks its Sigstore signature and the image's), but
# what clients trust doesn't depend on that: the app and harness-client verify each download with the release key built
# into them, from a mirror as from GitHub. Needs node and shasum; gh without --from; docker for the image.
set -euo pipefail
repo="${HARNESS_RELEASE_REPO:-xlsft/harness}"
usage="usage: offline-bundle.sh <tag> <out> --base-url URL [--from DIR] [--platform P[,P]] [--no-image] [--keep-latest]"
tag="${1:?$usage}"
out="${2:?$usage}"
shift 2
base="" from="" platforms="linux/amd64" image=true keep_latest=false
while (($#)); do
  case "$1" in
    --base-url) base="${2:?$usage}"; shift 2 ;;
    --from) from="${2:?$usage}"; shift 2 ;;
    --platform) platforms="${2:?$usage}"; shift 2 ;;
    --no-image) image=false; shift ;;
    --keep-latest) keep_latest=true; shift ;;
    *) echo "$usage" >&2; exit 2 ;;
  esac
done
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "not a release tag: $tag" >&2; exit 2; }
[[ "$base" =~ ^https?://[^[:space:]]+$ ]] || { echo "--base-url must be the http(s) URL the folder will be served at" >&2; exit 2; }
base="${base%/}"
version="${tag#v}"
mkdir -p "$out/$tag"
out="$(cd "$out" && pwd)"
rel="$out/$tag"

if [[ -n "$from" ]]; then
  cp -R "$from"/. "$rel"/
else
  gh release download "$tag" --repo "$repo" --dir "$rel" --clobber
fi
[[ -f "$rel/checksums.txt" && -f "$rel/latest.json" ]] || { echo "$tag has no checksums.txt or latest.json: is it a release?" >&2; exit 1; }
(cd "$rel" && shasum -a 256 -c --quiet checksums.txt) || { echo "files of $tag don't match its checksums.txt" >&2; exit 1; }
identity=(--certificate-identity-regexp "^https://github.com/$repo/\\.github/workflows/" --certificate-oidc-issuer https://token.actions.githubusercontent.com)
if command -v cosign >/dev/null && [[ -f "$rel/checksums.txt.sigstore.json" ]]; then
  cosign verify-blob "$rel/checksums.txt" --bundle "$rel/checksums.txt.sigstore.json" "${identity[@]}" >&2
else
  echo "no cosign (or no Sigstore bundle): checksums.txt checked against its files only. Clients still verify every update with the release key." >&2
fi

if [[ "$image" == true ]]; then
  ref="ghcr.io/$repo:$version"
  if command -v cosign >/dev/null; then
    cosign verify "$ref" "${identity[@]}" >/dev/null
  fi
  mkdir -p "$out/images"
  IFS=, read -r -a list <<<"$platforms"
  for p in "${list[@]}"; do
    docker pull --platform "$p" "$ref" >&2
    docker save -o "$out/images/harness_${version}_${p//\//_}.tar" "$ref"
  done
fi

if [[ "$keep_latest" != true ]]; then
  node -e '
    const fs = require("fs"), path = require("path")
    const [file, base, tag, outFile] = process.argv.slice(1)
    const latest = JSON.parse(fs.readFileSync(file, "utf8"))
    for (const p of Object.values(latest.platforms || {})) {
      p.url = `${base}/${encodeURIComponent(tag)}/${path.posix.basename(new URL(p.url).pathname)}`
    }
    fs.writeFileSync(outFile, JSON.stringify(latest, null, 2) + "\n")
  ' "$rel/latest.json" "$base" "$tag" "$out/latest.json"
fi
[[ -f "$out/latest.json" ]] || echo "no $out/latest.json yet: updates find nothing until a release is added without --keep-latest" >&2

(
  cd "$out"
  find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do shasum -a 256 "$f"; done >SHA256SUMS
)
echo "bundled $tag in $out; serve it at $base and set updateMirrorUrl to it" >&2
