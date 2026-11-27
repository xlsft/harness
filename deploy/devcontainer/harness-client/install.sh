#!/bin/sh
# The dev container feature "harness-client": installs the release's harness-client for this container's platform and
# writes the container's machine policy (serverUrl, updateMirrorUrl, disableUpdates). docs/devcontainers.md is the guide.
#
# Options arrive as environment variables (VERSION, SERVERURL, MIRROR, DISABLEUPDATES). Verified like
# `harness-client update`: the archive against checksums.txt, and checksums.txt against the release key below when
# minisign is installed (without it, against what HTTPS gives). The key is the one in packages/desktop/src-tauri/
# tauri.conf.json; scripts/release/test.sh checks they're the same.
set -eu

PUBKEY="RWSc+dP9yrMsdu4dOEG4pYhU0sHgUB4QosIdNsbezJHpiMUByBmzPcv0"
releases="${HARNESS_RELEASES:-https://github.com/xlsft/harness/releases}"
bin_dir="${HARNESS_INSTALL_DIR:-/usr/local/bin}"
policy_dir="${HARNESS_POLICY_DIR:-/etc/harness/settings.d}"
version="${VERSION:-latest}"
server_url="${SERVERURL:-}"
mirror="${MIRROR:-}"
disable_updates="${DISABLEUPDATES:-true}"

say() { echo "harness-client feature: $*" >&2; }
die() { say "$*"; exit 1; }

# Plain URLs only: they go into JSON as they are. A mirror may also be a file:// folder (mounted into the build).
url_ok() { printf '%s' "$2" | grep -Eq "^($1)://[A-Za-z0-9.:@%/_~+-]+\$"; }
[ -z "$server_url" ] || url_ok 'https?' "$server_url" || die "serverUrl is not an http(s) URL: $server_url"
[ -z "$mirror" ] || url_ok 'https?|file' "$mirror" || die "mirror is not an http(s) or file URL: $mirror"
server_url="${server_url%/}"
mirror="${mirror%/}"

command -v curl >/dev/null || die "needs curl (add ghcr.io/devcontainers/features/common-utils, or curl to the image)"
command -v tar >/dev/null || die "needs tar"
if command -v sha256sum >/dev/null; then sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null; then sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else die "needs sha256sum"; fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

if [ "$version" = latest ]; then
  if [ -n "$mirror" ]; then
    version="$(curl -fsSL "$mirror/latest.json" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  else
    # GitHub's latest release, never a draft or a pre-release: where releases/latest redirects to.
    effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest")"
    version="${effective##*/tag/}"
  fi
  [ -n "$version" ] || die "can't find the latest release"
fi
version="${version#v}"
printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' || die "not a version: $version"
if [ -n "$mirror" ]; then base="$mirror/v$version"; else base="$releases/download/v$version"; fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
name="harness-client_${version}_${os}_${arch}.tar.gz"
curl -fsSL -o "$work/checksums.txt" "$base/checksums.txt" || die "no checksums.txt at $base: is v$version a release?"
want="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$work/checksums.txt")"
[ -n "$want" ] || die "v$version has no $name: harness-client for $os/$arch isn't released yet. docs/devcontainers.md says how to build it into the image instead."
curl -fsSL -o "$work/$name" "$base/$name" || die "can't download $base/$name"
[ "$(sha256 "$work/$name")" = "$want" ] || die "$name doesn't match checksums.txt"

if command -v minisign >/dev/null; then
  curl -fsSL -o "$work/checksums.txt.sig" "$base/checksums.txt.sig" || die "no checksums.txt.sig at $base"
  printf 'untrusted comment: harness release key\n%s\n' "$PUBKEY" >"$work/release.pub"
  base64 -d <"$work/checksums.txt.sig" >"$work/checksums.txt.minisig" 2>/dev/null || die "checksums.txt.sig isn't base64"
  minisign -Vq -p "$work/release.pub" -m "$work/checksums.txt" -x "$work/checksums.txt.minisig" || die "checksums.txt isn't signed with the release key"
else
  say "no minisign: checksums.txt is trusted as HTTPS delivered it (install minisign to check its signature)"
fi

mkdir -p "$work/x" "$bin_dir"
tar -xzf "$work/$name" -C "$work/x" harness-client
install -m 0755 "$work/x/harness-client" "$bin_dir/harness-client"
"$bin_dir/harness-client" version >/dev/null || die "the installed harness-client doesn't run here"

# The container's machine policy: the same file an installer puts on a workstation (deploy/mdm/keys.md).
entries=""
add() { if [ -n "$entries" ]; then entries="$entries, $1"; else entries="$1"; fi; }
[ -z "$server_url" ] || add "\"serverUrl\": \"$server_url\""
case "$mirror" in http*) add "\"updateMirrorUrl\": \"$mirror\"" ;; esac
[ "$disable_updates" != true ] || add '"disableUpdates": true'
if [ -n "$entries" ]; then
  mkdir -p "$policy_dir"
  printf '{%s}\n' "$entries" >"$policy_dir/00-devcontainer.json"
  chmod 644 "$policy_dir/00-devcontainer.json"
fi
say "installed $("$bin_dir/harness-client" version) to $bin_dir${server_url:+, server $server_url}"
