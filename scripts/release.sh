#!/usr/bin/env bash
# Cutting a release (docs/release.md). Checks that this checkout can be tagged, CI being green on it included, and prints the
# commands; changes nothing unless told, and even then only creates the local tag. Pushing it is what starts the release.
#
# Usage: scripts/release.sh vX.Y.Z [--create-tag]
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

create=false
tag=""
for arg in "$@"; do
  case "$arg" in
    --create-tag) create=true ;;
    -*) echo "unknown flag $arg" >&2; exit 2 ;;
    *) tag="$arg" ;;
  esac
done
if [[ -z "$tag" ]]; then
  echo "usage: scripts/release.sh vX.Y.Z [--create-tag]" >&2
  exit 2
fi

bad() { echo "Can't release: $*" >&2; exit 1; }

scripts/release/parse-version.sh "$tag" >/dev/null || exit 1
[[ "$(git rev-parse --abbrev-ref HEAD)" == main ]] || bad "not on main"
[[ -z "$(git status --porcelain)" ]] || bad "the working tree isn't clean"
git fetch --quiet --tags origin main || bad "can't fetch from origin"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || bad "main isn't the same as origin/main"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then bad "tag $tag already exists"; fi
pubkey="$(node -p "require('./packages/desktop/src-tauri/tauri.conf.json').plugins.updater.pubkey")"
[[ -n "$pubkey" ]] || bad "plugins.updater.pubkey in tauri.conf.json is empty, and the release would refuse it: see docs/release.md"
if grep -rqs HARNESS_MIN_CLIENT_VERSION deploy/Dockerfile deploy/rootfs; then
  bad "the image sets HARNESS_MIN_CLIENT_VERSION, which would lock out older clients"
fi

command -v gh >/dev/null || bad "gh is needed to read the CI results of this commit"
scripts/release/ci-green.sh "$(git rev-parse HEAD)" || exit 1

if [[ "$create" == true ]]; then
  git tag -a "$tag" -m "$tag"
  echo "Created the local tag $tag. To release it:"
  echo "  git push origin $tag"
else
  echo "$tag can be released from $(git rev-parse --short HEAD). To do it:"
  echo "  git tag -a $tag -m $tag"
  echo "  git push origin $tag"
fi
