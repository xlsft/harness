#!/usr/bin/env bash
# Release notes from the commits on main since the previous stable tag (their subjects: merges name what landed).
#   notes.sh changes <tag>           the list alone; the desktop updater shows it
#   notes.sh full <tag> <signing>    what the GitHub Release says; signing is unsigned, signed or notarized
set -euo pipefail
mode="${1:?usage: notes.sh changes|full <tag> [signing]}"
tag="${2:?usage: notes.sh changes|full <tag> [signing]}"
signing="${3:-unsigned}"
repo="${GITHUB_REPOSITORY:-xlsft/harness}"
version="${tag#v}"
MAX=100

changes() {
  local previous range
  # Every release is a normal one, so the base is the previous release tag, suffix or not.
  previous="$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$tag^" 2>/dev/null || true)"
  range="$tag"
  [[ -n "$previous" ]] && range="$previous..$tag"
  local subjects total
  subjects="$(git log --first-parent --format=%s "$range" | sed -E 's/^Merge (branch |pull request )?//')"
  total="$(printf '%s\n' "$subjects" | grep -c . || true)"
  printf '%s\n' "$subjects" | head -n "$MAX" | sed 's/^/- /'
  if ((total > MAX)); then
    echo "- … and $((total - MAX)) earlier changes"
  fi
}

if [[ "$mode" == changes ]]; then
  changes
  exit 0
fi

cat <<EOF
## What changed

$(changes)

## Desktop app (macOS)

\`harness_${version}_darwin_arm64.dmg\` for Apple Silicon, \`harness_${version}_darwin_amd64.dmg\` for Intel. An installed
app offers this update by itself under Settings → About.
EOF

case "$signing" in
  notarized)
    echo
    echo "Signed with a Developer ID and notarized by Apple: it opens normally."
    ;;
  signed)
    cat <<EOF

Signed with a Developer ID but not notarized, so macOS blocks the first open. Move it to Applications (from the disk image
or Downloads it can't update itself) and open it once. Then go to **System Settings → Privacy & Security → Open Anyway**, or
run \`xattr -dr com.apple.quarantine "/Applications/xlsft\`s harness.app"\`.
EOF
    ;;
  *)
    cat <<EOF

This build has no Apple Developer ID signature (it is signed ad-hoc) and isn't notarized, so macOS blocks the first open.
Move it to Applications (from the disk image or Downloads it can't update itself) and open it once. Then go to **System
Settings → Privacy & Security → Open Anyway**, or run \`xattr -dr com.apple.quarantine "/Applications/xlsft\`s harness.app"\`.
Updates from inside the app don't ask this again.

Computer use only: macOS ties Screen Recording and Accessibility to the exact build of an ad-hoc app, so they stop applying
after every update, even if the switches still look on. Remove the app from those two lists in System Settings → Privacy &
Security, add it again, and restart it.
EOF
    ;;
esac

cat <<EOF

## Command-line client (macOS)

\`\`\`sh
curl -fsSL https://github.com/${repo}/releases/download/${tag}/harness-client_${version}_darwin_arm64.tar.gz | tar xz harness-client
\`\`\`

Use \`darwin_amd64\` on Intel. Already installed: \`harness-client update\`. The app's own sidecar is updated with the app.

## Server

\`\`\`sh
docker pull ghcr.io/${repo}:${version}
\`\`\`

How to upgrade: [deploy/README.md](https://github.com/${repo}/blob/${tag}/deploy/README.md#upgrading).

## Verify

\`checksums.txt\` covers every file here and is signed with [Sigstore](https://www.sigstore.dev) by this repository's release
workflow; the image is signed the same way and carries an SBOM and build provenance.

\`\`\`sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \\
  --certificate-identity-regexp '^https://github.com/${repo}/\\.github/workflows/' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify harness-client_${version}_darwin_arm64.tar.gz --repo ${repo}
cosign verify ghcr.io/${repo}:${version} \\
  --certificate-identity-regexp '^https://github.com/${repo}/\\.github/workflows/' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
\`\`\`
EOF
