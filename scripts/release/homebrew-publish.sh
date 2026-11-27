#!/usr/bin/env bash
# Pushes the files homebrew.sh wrote into the tap repository, as one commit on its default branch. Only the release workflow
# runs this, and only when the HOMEBREW_TAP_TOKEN secret exists (docs/release.md, Homebrew). It never creates the tap.
#
# Usage: homebrew-publish.sh <tag> <directory from homebrew.sh>
# Environment: HOMEBREW_TAP_TOKEN (contents: write on the tap), HOMEBREW_TAP (default xlsft/homebrew-harness),
#   HOMEBREW_TAP_URL (default https://github.com/$HOMEBREW_TAP.git; the tests point it at a local repository).
set -euo pipefail
tag="${1:?usage: homebrew-publish.sh <tag> <directory>}"
files="${2:?usage: homebrew-publish.sh <tag> <directory>}"
tap="${HOMEBREW_TAP:-xlsft/homebrew-harness}"
url="${HOMEBREW_TAP_URL:-https://github.com/$tap.git}"
[[ -f "$files/Formula/harness-client.rb" && -f "$files/Casks/harness.rb" ]] || { echo "no Homebrew files in $files" >&2; exit 1; }

work="$(mktemp -d "${TMPDIR:-/tmp}/harness-tap.XXXXXX")"
trap 'rm -rf "$work"' EXIT
git_() { git -C "$work/tap" -c user.name="xlsft's harness release" -c user.email="noreply@harness.xlsft.com" -c commit.gpgsign=false "$@"; }

# The token goes in a header for this one command, never into the remote URL that git would keep in .git/config.
auth=()
if [[ -n "${HOMEBREW_TAP_TOKEN:-}" ]]; then
  auth=(-c "http.extraHeader=Authorization: Basic $(printf 'x-access-token:%s' "$HOMEBREW_TAP_TOKEN" | base64 | tr -d '\n')")
fi
if ! git ${auth[@]+"${auth[@]}"} clone --quiet --depth 1 "$url" "$work/tap"; then
  echo "can't clone the tap $tap: create it first (docs/release.md, Homebrew), and give the token contents: write on it" >&2
  exit 1
fi
mkdir -p "$work/tap/Formula" "$work/tap/Casks"
cp "$files/Formula/harness-client.rb" "$work/tap/Formula/"
cp "$files/Casks/harness.rb" "$work/tap/Casks/"
git_ add Formula/harness-client.rb Casks/harness.rb
if git_ diff --cached --quiet; then
  echo "The tap already has $tag"
  exit 0
fi
git_ commit --quiet -m "harness-client and harness $tag"
git_ ${auth[@]+"${auth[@]}"} push --quiet origin HEAD
echo "Published $tag to $tap"
