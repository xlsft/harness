#!/usr/bin/env bash
# Tests of scripts/install.sh against a fake release server on loopback: nothing here talks to GitHub. Needs python3.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install="$root/scripts/install.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/harness-install-test.XXXXXX")"
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT
failures=0
ok() { echo "ok   $1"; }
no() {
  echo "FAIL $1"
  failures=$((failures + 1))
}
expect() { if eval "$2" >"$work/out" 2>&1; then ok "$1"; else no "$1"; sed 's/^/     /' "$work/out"; fi; }
refuse() { if eval "$2" >"$work/out" 2>&1; then no "$1"; sed 's/^/     /' "$work/out"; else ok "$1"; fi; }
# says <name> <pattern> <command>: what the command prints, failing or not, has the pattern.
says() {
  eval "$3" >"$work/said" 2>&1 || true
  if grep -q -- "$2" "$work/said"; then ok "$1"; else no "$1"; sed 's/^/     /' "$work/said"; fi
}

# The script runs as on an Apple Silicon Mac whatever this machine is: uname is a stand-in (below), so this runs in Linux CI too.
arch=arm64

# A release tree as GitHub serves it: releases/download/<tag>/<asset>, and releases/latest redirecting to releases/tag/<tag>.
site="$work/site"
release() { # release <tag> [corrupt]
  local tag="$1" v="${1#v}" dir="$site/releases/download/$1"
  mkdir -p "$dir" "$work/build/$tag"
  for a in arm64 amd64; do
    printf '#!/bin/sh\necho "harness-client/%s (fake %s)"\n' "$v" "$a" >"$work/build/$tag/harness-client"
    chmod +x "$work/build/$tag/harness-client"
    tar -czf "$dir/harness-client_${v}_darwin_${a}.tar.gz" -C "$work/build/$tag" harness-client
  done
  (cd "$dir" && shasum -a 256 ./*.tar.gz | sed 's# \./# #' >checksums.txt)
  echo '{"fake":"bundle"}' >"$dir/checksums.txt.sigstore.json"
  if [[ "${2:-}" == corrupt ]]; then echo garbage >>"$dir/harness-client_${v}_darwin_${arch}.tar.gz"; fi
}
release v1.2.2
release v1.2.3
release v1.3.0-rc.1
release v1.1.0 corrupt
mkdir -p "$site/releases/download/v1.0.0"
echo "0000  harness-client_1.0.0_darwin_${arch}.tar.gz" >"$site/releases/download/v1.0.0/checksums.txt"

cat >"$work/server.py" <<'EOF'
import http.server, os, sys
site, portfile = sys.argv[1], sys.argv[2]
class H(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k): super().__init__(*a, directory=site, **k)
    def log_message(self, *a): pass
    def do_GET(self):
        latest = open(os.path.join(site, "latest")).read().strip() if os.path.exists(os.path.join(site, "latest")) else ""
        if self.path == "/releases/latest":
            self.send_response(302)
            self.send_header("Location", f"/releases/tag/{latest}" if latest else "/releases")
            self.end_headers()
            return
        if self.path.startswith("/releases/tag/") or self.path == "/releases":
            self.send_response(200); self.end_headers(); self.wfile.write(b"a release page"); return
        super().do_GET()
s = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
open(portfile, "w").write(str(s.server_address[1]))
s.serve_forever()
EOF
python3 "$work/server.py" "$site" "$work/port" &
server_pid=$!
for _ in $(seq 50); do [[ -s "$work/port" ]] && break; sleep 0.1; done
base="http://127.0.0.1:$(cat "$work/port")"
echo v1.2.3 >"$site/latest"

# A PATH with only what a Mac has, and no cosign unless a test adds its stand-in.
bin="$work/bin"
mkdir -p "$bin"
for t in curl tar shasum awk grep head mktemp sed mkdir cp mv chmod rm id basename cat dirname gzip; do
  p="$(command -v "$t" || true)"
  [[ -n "$p" ]] && ln -s "$p" "$bin/$t"
done
printf '#!/bin/sh\ncase "$1" in -s) echo Darwin ;; -m) echo "${FAKE_MACHINE:-arm64}" ;; esac\n' >"$bin/uname"
printf '#!/bin/sh\necho "${FAKE_TRANSLATED:-0}"\n' >"$bin/sysctl"
chmod +x "$bin/uname" "$bin/sysctl"
cosign_ok="$work/cosign-ok"
cosign_bad="$work/cosign-bad"
mkdir -p "$cosign_ok" "$cosign_bad"
printf '#!/bin/sh\necho "$@" >"%s/cosign-args"\nexit 0\n' "$work" >"$cosign_ok/cosign"
printf '#!/bin/sh\nexit 1\n' >"$cosign_bad/cosign"
chmod +x "$cosign_ok/cosign" "$cosign_bad/cosign"

run() { # run <prefix> [args...]: the script as curl | sh runs it
  local prefix="$1"
  shift
  env -i HOME="$work/home" PATH="${EXTRA_PATH:-}$bin:/bin" SHELL=/bin/zsh FAKE_MACHINE="${FAKE_MACHINE:-arm64}" FAKE_TRANSLATED="${FAKE_TRANSLATED:-0}" TMPDIR="$work" HARNESS_INSTALL_BASE_URL="$base" \
    sh -s -- --prefix "$prefix" "$@" <"$install"
}
installed() { [[ "$("$1/bin/harness-client" version)" == "harness-client/$2 (fake ${3:-$arch})" ]]; }

expect "installs the latest release" "run '$work/p1'"
expect "...the binary of that version and architecture" "installed '$work/p1' 1.2.3"
says "...says how to put it on PATH" "export PATH=\"$work/p1/bin:\$PATH\"" "run '$work/p1'"
says "...and that the signature wasn't checked without cosign" "cosign isn't installed" "run '$work/p1'"
refuse "...and runs no sudo" "grep -v '^ *#' '$install' | grep -Eq '(^|[;&|(]|then|do|else) *sudo( |$)'"
expect "a version by flag" "run '$work/p2' --version v1.2.2 && installed '$work/p2' 1.2.2"
expect "a version without its v" "run '$work/p3' --version 1.2.2 && installed '$work/p3' 1.2.2"
expect "a pre-release by flag" "run '$work/p4' --version v1.3.0-rc.1 && installed '$work/p4' 1.3.0-rc.1"
expect "the default prefix is ~/.harness" "env -i HOME='$work/home' PATH='$bin:/bin' TMPDIR='$work' HARNESS_INSTALL_BASE_URL='$base' sh <'$install' && installed '$work/home/.harness' 1.2.3"
expect "a prefix with ~" "run '~/custom' && installed '$work/home/custom' 1.2.3"
expect "an Intel Mac gets the amd64 build" "FAKE_MACHINE=x86_64 run '$work/p12' && installed '$work/p12' 1.2.3 amd64"
expect "a shell under Rosetta gets the native arm64 build" "FAKE_MACHINE=x86_64 FAKE_TRANSLATED=1 run '$work/p13' && installed '$work/p13' 1.2.3 arm64"
expect "an upgrade replaces the binary" "run '$work/p2' && installed '$work/p2' 1.2.3"

refuse "an archive that doesn't match checksums.txt is refused" "run '$work/p5' --version v1.1.0"
refuse "...and nothing is installed" "test -e '$work/p5/bin/harness-client'"
run "$work/p6" --version v1.2.2 >/dev/null 2>&1
expect "...and a failed upgrade keeps the old binary" "! run '$work/p6' --version v1.1.0; installed '$work/p6' 1.2.2"
refuse "a release without the archive is refused" "run '$work/p7' --version v1.0.0"
refuse "a release that doesn't exist is refused" "run '$work/p7' --version v9.9.9"
refuse "a version that isn't one is refused" "run '$work/p7' --version 'v1.2;rm -rf /'"
refuse "an unknown option is refused" "run '$work/p7' --frobnicate"

says "with cosign the signature is checked" "Signature ok" "EXTRA_PATH='$cosign_ok:' run '$work/p8'"
expect "...against the release workflow's identity" "grep -q 'certificate-identity-regexp ^https://github.com/xlsft/harness/' '$work/cosign-args' && grep -q 'checksums.txt.sigstore.json' '$work/cosign-args'"
refuse "a signature that doesn't verify is refused" "EXTRA_PATH='$cosign_bad:' run '$work/p9'"
refuse "...and nothing is installed" "test -e '$work/p9/bin/harness-client'"
refuse "--require-signature without cosign is refused" "run '$work/p10' --require-signature"
expect "--require-signature with cosign installs" "EXTRA_PATH='$cosign_ok:' run '$work/p10' --require-signature && installed '$work/p10' 1.2.3"

chmod 0555 "$work/p1/bin"
refuse "a prefix that isn't writable is refused, not escalated" "run '$work/p1'"
chmod 0755 "$work/p1/bin"

rm "$site/latest"
refuse "no published release is an error" "run '$work/p11'"
says "...that says so" "no release" "run '$work/p11'"

if ((failures)); then
  echo "$failures failed"
  exit 1
fi
echo "all passed"
