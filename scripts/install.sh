#!/bin/sh
# Installs harness-client, the xlsft`s harness agent client, from a GitHub Release.
#
#   curl -fsSL https://raw.githubusercontent.com/xlsft/harness/main/scripts/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.2.0 --prefix ~/.local/share/harness
#
# It downloads harness-client_<version>_<os>_<arch>.tar.gz and checksums.txt, checks the archive against checksums.txt, and
# checks checksums.txt's Sigstore signature when cosign is installed. Then it puts the binary in <prefix>/bin
# (~/.harness/bin by default) and says how to add that to PATH. It never asks for sudo and writes nothing outside the
# prefix and a temporary directory. `harness-client update` keeps it current afterwards.
#
# Options:
#   --version vX.Y.Z      a release instead of the latest stable one
#   --prefix DIR          install into DIR/bin (default ~/.harness, or $HARNESS_INSTALL_PREFIX)
#   --require-signature   fail when cosign isn't installed, instead of checking the checksums only
#   -h, --help
#
# Environment: HARNESS_INSTALL_BASE_URL is the repository's web address (default https://github.com/xlsft/harness); the
# tests point it at a fake release server.
#
# The whole script is one function called on the last line, so a download cut short runs nothing.

set -eu

main() {
  base="${HARNESS_INSTALL_BASE_URL:-https://github.com/xlsft/harness}"
  base="${base%/}"
  # Only HTTPS, redirects included, unless the base itself is plain HTTP (a local fake release server in the tests).
  case "$base" in
    http://*) protos='=http,https' ;;
    *) protos='=https' ;;
  esac
  prefix="${HARNESS_INSTALL_PREFIX:-$HOME/.harness}"
  version=""
  require_sig=false

  while [ $# -gt 0 ]; do
    case "$1" in
      --version) [ $# -ge 2 ] || die "--version needs a value"; version="$2"; shift 2 ;;
      --version=*) version="${1#*=}"; shift ;;
      --prefix) [ $# -ge 2 ] || die "--prefix needs a value"; prefix="$2"; shift 2 ;;
      --prefix=*) prefix="${1#*=}"; shift ;;
      --require-signature) require_sig=true; shift ;;
      -h | --help) usage; return 0 ;;
      *) die "unknown option $1 (see --help)" ;;
    esac
  done

  if [ "$(id -u)" = 0 ]; then
    warn "running as root: harness-client is meant to be installed per user, in your home directory"
  fi
  need curl
  need tar
  need uname
  sha_tool

  os="$(detect_os)"
  arch="$(detect_arch "$os")"

  if [ -z "$version" ]; then
    version="$(latest_tag)"
  fi
  case "$version" in
    v*) ;;
    *) version="v$version" ;;
  esac
  valid_version "$version" || die "not a release version: $version (expected vX.Y.Z or vX.Y.Z-rc.N)"
  number="${version#v}"

  case "$prefix" in
    /*) ;;
    "~"/*) prefix="$HOME/${prefix#"~/"}" ;;
    *) prefix="$(pwd)/$prefix" ;;
  esac
  bindir="$prefix/bin"

  archive="harness-client_${number}_${os}_${arch}.tar.gz"
  download="$base/releases/download/$version"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/harness-install.XXXXXX")"
  # shellcheck disable=SC2064 # expand now: the variable is local to this run
  trap "rm -rf '$tmp'" EXIT INT TERM

  say "Installing harness-client $version for $os/$arch into $bindir"
  fetch "$download/checksums.txt" "$tmp/checksums.txt" || die "no checksums.txt in release $version (is $version released?)"
  fetch "$download/$archive" "$tmp/$archive" || die "release $version has no $archive"

  verify_signature "$download" "$tmp"
  verify_checksum "$tmp" "$archive"

  mkdir -p "$tmp/x"
  tar -xzf "$tmp/$archive" -C "$tmp/x" harness-client || die "$archive has no harness-client"
  chmod 0755 "$tmp/x/harness-client"
  # A binary that doesn't run here (wrong architecture, a broken download that passed somehow) never replaces one that does.
  "$tmp/x/harness-client" version >/dev/null 2>&1 || die "the downloaded harness-client doesn't run on this machine"

  mkdir -p "$bindir" || die "can't create $bindir; choose a folder you own with --prefix"
  [ -w "$bindir" ] || die "$bindir isn't writable; choose a folder you own with --prefix (this script never uses sudo)"
  # Written next to the old one and renamed over it: a running client keeps its file, and a failure leaves the old one.
  cp "$tmp/x/harness-client" "$bindir/.harness-client.new"
  mv -f "$bindir/.harness-client.new" "$bindir/harness-client"
  say "Installed $("$bindir/harness-client" version 2>/dev/null || echo harness-client) at $bindir/harness-client"

  path_advice "$bindir"
  say ""
  say "Next: harness-client login https://your-harness-server   (then: harness-client doctor)"
}

usage() {
  cat <<'EOF'
Installs harness-client from a GitHub Release into <prefix>/bin, checked against the release's checksums.txt.

Usage: install.sh [--version vX.Y.Z] [--prefix DIR] [--require-signature]
  --version vX.Y.Z      a release instead of the latest stable one
  --prefix DIR          install into DIR/bin (default ~/.harness, or $HARNESS_INSTALL_PREFIX)
  --require-signature   fail when cosign isn't installed, instead of checking the checksums only
EOF
}

say() { printf '%s\n' "$*"; }
warn() { printf 'Warning: %s\n' "$*" >&2; }
die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is needed and isn't installed"; }

sha_tool() {
  if command -v shasum >/dev/null 2>&1; then
    sha="shasum -a 256"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha="sha256sum"
  else
    die "shasum or sha256sum is needed to check the download"
  fi
}

# The release names: Go's GOOS and GOARCH, as .goreleaser.yaml uses them.
detect_os() {
  case "$(uname -s)" in
    Darwin) echo darwin ;;
    # Linux builds compile in CI but aren't released yet; the name is ready for when they are.
    Linux) die "there is no Linux release of harness-client yet; build it from source (packages/client)" ;;
    *) die "unsupported system $(uname -s): releases are for macOS" ;;
  esac
}

detect_arch() {
  m="$(uname -m)"
  # A shell running under Rosetta reports x86_64 on Apple Silicon: install the native build.
  if [ "$1" = darwin ] && [ "$m" = x86_64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    m=arm64
  fi
  case "$m" in
    arm64 | aarch64) echo arm64 ;;
    x86_64 | amd64) echo amd64 ;;
    *) die "unsupported architecture $m" ;;
  esac
}

valid_version() {
  printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'
}

fetch() {
  curl -fsSL --proto "$protos" --proto-redir "$protos" --retry 3 --connect-timeout 20 -o "$2" "$1"
}

# The releases/latest redirect names the newest stable release (never a draft or a pre-release) and has no API limit.
latest_tag() {
  final="$(curl -fsSL --proto "$protos" --proto-redir "$protos" --retry 3 --connect-timeout 20 -o /dev/null -w '%{url_effective}' "$base/releases/latest")" ||
    die "can't reach $base/releases/latest"
  case "$final" in
    */releases/tag/*) tag="${final##*/releases/tag/}" ;;
    *) die "no release of harness-client is published yet" ;;
  esac
  valid_version "$tag" || die "the latest release has an unexpected tag: $tag"
  echo "$tag"
}

verify_checksum() {
  want="$(awk -v n="$2" '$2 == n || $2 == "*" n { print $1 }' "$1/checksums.txt" | head -n1)"
  [ -n "$want" ] || die "checksums.txt doesn't list $2"
  got="$($sha "$1/$2" | awk '{ print $1 }')"
  [ "$want" = "$got" ] || die "$2 doesn't match checksums.txt (expected $want, got $got); nothing was installed"
  say "Checksum ok"
}

# checksums.txt is signed keylessly by this repository's release workflow (docs/release.md, Verifying a release).
verify_signature() {
  if ! command -v cosign >/dev/null 2>&1; then
    if [ "$require_sig" = true ]; then
      die "--require-signature: cosign isn't installed (https://docs.sigstore.dev/cosign/system_config/installation/)"
    fi
    warn "cosign isn't installed, so the signature of checksums.txt wasn't checked; the archive is still checked against it"
    return 0
  fi
  fetch "$1/checksums.txt.sigstore.json" "$2/checksums.txt.sigstore.json" || die "the release has no checksums.txt.sigstore.json to verify"
  cosign verify-blob "$2/checksums.txt" --bundle "$2/checksums.txt.sigstore.json" \
    --certificate-identity-regexp '^https://github.com/xlsft/harness/\.github/workflows/' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1 ||
    die "the signature of checksums.txt doesn't verify; nothing was installed"
  say "Signature ok"
}

path_advice() {
  case ":${PATH:-}:" in
    *":$1:"*) return 0 ;;
  esac
  say ""
  say "$1 isn't on your PATH. Add it:"
  case "$(basename "${SHELL:-sh}")" in
    zsh) say "  echo 'export PATH=\"$1:\$PATH\"' >> ~/.zshrc && exec zsh" ;;
    bash) say "  echo 'export PATH=\"$1:\$PATH\"' >> ~/.bash_profile && exec bash" ;;
    fish) say "  fish_add_path $1" ;;
    *) say "  export PATH=\"$1:\$PATH\"   (in your shell's startup file)" ;;
  esac
  say "Shell completions: harness-client completion --help"
}

main "$@"
