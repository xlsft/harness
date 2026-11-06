#!/usr/bin/env bash
# Tests of the release scripts, in throwaway copies: nothing here tags, pushes or publishes.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/harness-release-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
failures=0
ok() { echo "ok   $1"; }
no() { echo "FAIL $1"; failures=$((failures + 1)); }
expect() { if eval "$2" >/dev/null 2>&1; then ok "$1"; else no "$1"; fi; }
refuse() { if eval "$2" >/dev/null 2>&1; then no "$1"; else ok "$1"; fi; }

parse="$root/scripts/release/parse-version.sh"
for good in v0.1.0 v1.20.3 v0.2.0-rc.1 v1.0.0-beta.2.x; do expect "accepts $good" "$parse $good"; done
for bad in 0.1.0 v1 v1.2 v1.2.3.4 v01.2.3 v1.2.3- v1.2.3-rc..1 vX.Y.Z 'v1.2.3 x' v1.2.3+build; do refuse "refuses '$bad'" "$parse '$bad'"; done
[[ "$($parse v0.2.0-rc.1 | tail -1)" == prerelease=true ]] && ok "a tag with a suffix is a pre-release" || no "a tag with a suffix is a pre-release"
[[ "$($parse v0.2.0 | tail -1)" == prerelease=false ]] && ok "a plain tag is stable" || no "a plain tag is stable"

# stamp.mjs writes every package, and nothing else in the files.
stamped="$work/stamped"
mkdir -p "$stamped/packages"
for f in packages/core/package.json packages/ui/package.json packages/desktop/package.json packages/desktop/src-tauri/tauri.conf.json packages/desktop/src-tauri/Cargo.toml packages/desktop/src-tauri/Cargo.lock; do
  mkdir -p "$stamped/$(dirname "$f")"
  cp "$root/$f" "$stamped/$f"
done
refuse "stamp refuses a non-version" "node '$root/scripts/release/stamp.mjs' nonsense '$stamped'"
expect "stamp runs" "node '$root/scripts/release/stamp.mjs' 9.8.7-rc.1 '$stamped'"
for f in packages/core/package.json packages/ui/package.json packages/desktop/package.json packages/desktop/src-tauri/tauri.conf.json; do
  [[ "$(node -p "require('$stamped/$f').version")" == 9.8.7-rc.1 ]] && ok "stamped $f" || no "stamped $f"
done
expect "stamped Cargo.toml" "grep -q '^version = \"9.8.7-rc.1\"$' '$stamped/packages/desktop/src-tauri/Cargo.toml'"
expect "stamped Cargo.lock" "grep -A1 'name = \"harness-desktop\"' '$stamped/packages/desktop/src-tauri/Cargo.lock' | grep -q 9.8.7-rc.1"
[[ "$(grep -c 9.8.7-rc.1 "$stamped/packages/desktop/src-tauri/Cargo.lock")" == 1 ]] && ok "only our package in Cargo.lock" || no "only our package in Cargo.lock"

# latest-json.mjs
dist="$work/dist"
mkdir -p "$dist"
echo "- a change" >"$work/changes.md"
touch "$dist/harness_1.2.3_darwin_arm64.app.tar.gz" "$dist/harness_1.2.3_darwin_amd64.app.tar.gz"
echo "SIGARM" >"$dist/harness_1.2.3_darwin_arm64.app.tar.gz.sig"
refuse "latest.json refuses an archive without a signature" "node '$root/scripts/release/latest-json.mjs' v1.2.3 '$dist' '$work/changes.md'"
echo "SIGX64" >"$dist/harness_1.2.3_darwin_amd64.app.tar.gz.sig"
expect "latest.json is written" "GITHUB_REPOSITORY=o/r node '$root/scripts/release/latest-json.mjs' v1.2.3 '$dist' '$work/changes.md'"
latest() { node -p "require('$dist/latest.json').$1"; }
[[ "$(latest version)" == 1.2.3 ]] && ok "latest.json version" || no "latest.json version"
[[ "$(latest "platforms['darwin-aarch64'].signature")" == SIGARM ]] && ok "latest.json signature" || no "latest.json signature"
[[ "$(latest "platforms['darwin-x86_64'].url")" == https://github.com/o/r/releases/download/v1.2.3/harness_1.2.3_darwin_amd64.app.tar.gz ]] && ok "latest.json url" || no "latest.json url"

# ci-green.sh against a stand-in for gh: what each workflow's newest run on the commit answered.
stub="$work/bin"
mkdir -p "$stub"
cat >"$stub/gh" <<'EOF'
#!/usr/bin/env bash
# gh api repos/<repo>/actions/workflows/<name>.yml/runs?head_sha=<sha>&per_page=100 --jq ...
name="${2#*workflows/}"; name="${name%%.yml*}"
override="$(sed -n "s/^$name=//p" "$GH_STUB_FILE" 2>/dev/null)"
if [[ "$override" == error ]]; then exit 1; fi
echo "${override:-completed success}"
EOF
chmod +x "$stub/gh"
export GH_STUB_FILE="$work/gh-stub" HARNESS_CI_POLL_SECONDS=1
green="$root/scripts/release/ci-green.sh"
ci() { PATH="$stub:$PATH" "$green" abc123 "$@"; }
: >"$GH_STUB_FILE"
expect "ci-green passes when every workflow succeeded" "ci"
for state in "failed:completed failure" "cancelled:completed cancelled" "skipped:completed skipped" "still running:in_progress null" "queued:queued null" "missing:missing" "unreadable:error"; do
  echo "image=${state#*:}" >"$GH_STUB_FILE"
  refuse "ci-green refuses one that is ${state%%:*}" "ci"
done
echo "core=in_progress null" >"$GH_STUB_FILE"
refuse "ci-green gives up on a run that doesn't finish" "ci --wait 2"
echo "core=completed failure" >"$GH_STUB_FILE"
refuse "ci-green doesn't wait for a failure to turn" "ci --wait 600"
: >"$GH_STUB_FILE"
expect "ci-green has no way to skip: only --wait is an option" "! grep -nE 'skip|force|bypass' <(sed -e '/^#/d' -e '/  *# /d' '$green')"
for wf in cli client core desktop e2e image proto ui workflows; do
  expect "$wf can be run on demand for a commit" "grep -q workflow_dispatch '$root/.github/workflows/$wf.yml'"
done

# notes.sh and release.sh in a repository of their own.
repo="$work/repo"
mkdir -p "$repo/scripts/release" "$repo/packages/desktop/src-tauri" "$repo/deploy" "$repo/.github"
cp "$root/scripts/release.sh" "$repo/scripts/"
cp "$parse" "$green" "$root/scripts/release/notes.sh" "$repo/scripts/release/"
echo '{"plugins":{"updater":{"pubkey":"KEY"}}}' >"$repo/packages/desktop/src-tauri/tauri.conf.json"
# release.sh tags as whoever runs it; a CI runner has no git identity.
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
git init -q --bare "$work/origin.git"
g() { git -C "$repo" -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
git init -q -b main "$repo"
g add . && g commit -qm "First"
g remote add origin "$work/origin.git" && g push -q origin main
g tag -a v0.1.0 -m v0.1.0
g commit -q --allow-empty -m "Second" && g tag -a v0.2.0-rc.1 -m rc
g commit -q --allow-empty -m "Merge third thing" && g tag -a v0.2.0 -m v0.2.0
notes="$repo/scripts/release/notes.sh"
(cd "$repo" && $notes changes v0.2.0) >"$work/changes"
expect "notes list what came since the last stable tag" "grep -q '^- Second$' '$work/changes' && grep -q '^- third thing$' '$work/changes' && ! grep -q First '$work/changes'"
(cd "$repo" && $notes changes v0.1.0) >"$work/first"
expect "the first release lists from the beginning" "grep -q '^- First$' '$work/first'"
(cd "$repo" && $notes full v0.2.0 unsigned) >"$work/unsigned"
expect "unsigned notes say how to open it" "grep -q 'Open' '$work/unsigned' && grep -q 'com.apple.quarantine' '$work/unsigned' && grep -q 'Accessibility' '$work/unsigned'"
(cd "$repo" && $notes full v0.2.0 notarized) >"$work/notarized"
refuse "notarized notes don't" "grep -q 'com.apple.quarantine' '$work/notarized'"

release() { (cd "$repo" && PATH="$stub:$PATH" scripts/release.sh "$@"); }
g tag -d v0.1.0 v0.2.0-rc.1 v0.2.0 >/dev/null
g push -q origin main
refuse "release.sh refuses a bad tag" "release 0.1.0"
expect "release.sh prints the commands" "release v0.1.0 | grep -q 'git push origin v0.1.0'"
refuse "...and creates no tag by itself" "g rev-parse -q --verify refs/tags/v0.1.0"
g checkout -qb other
refuse "release.sh refuses another branch" "release v0.1.0"
g checkout -q main
echo x >"$repo/dirty"
refuse "release.sh refuses a dirty tree" "release v0.1.0"
rm "$repo/dirty"
g commit -q --allow-empty -m "Unpushed"
refuse "release.sh refuses unpushed commits" "release v0.1.0"
g push -q origin main
echo '{"plugins":{"updater":{"pubkey":""}}}' >"$repo/packages/desktop/src-tauri/tauri.conf.json"
g add . && g commit -qm "No key" && g push -q origin main
refuse "release.sh refuses an empty updater key" "release v0.1.0"
echo '{"plugins":{"updater":{"pubkey":"KEY"}}}' >"$repo/packages/desktop/src-tauri/tauri.conf.json"
echo "ENV HARNESS_MIN_CLIENT_VERSION=1.0.0" >"$repo/deploy/Dockerfile"
g add . && g commit -qm "Lockout" && g push -q origin main
refuse "release.sh refuses an image that locks out old clients" "release v0.1.0"
g rm -q deploy/Dockerfile && g commit -qm "Fine" && g push -q origin main
echo "e2e=completed failure" >"$GH_STUB_FILE"
refuse "release.sh refuses a commit whose CI isn't green" "release v0.1.0"
: >"$GH_STUB_FILE"
expect "release.sh --create-tag" "release v0.1.0 --create-tag"
expect "...creates the tag" "g rev-parse -q --verify refs/tags/v0.1.0"
refuse "...and pushes nothing" "git -C '$work/origin.git' rev-parse -q --verify refs/tags/v0.1.0"
refuse "release.sh refuses an existing tag" "release v0.1.0"

# The real image never sets a minimum client version.
refuse "the image sets no minimum client version" "grep -rq HARNESS_MIN_CLIENT_VERSION '$root/deploy/Dockerfile' '$root/deploy/rootfs'"

if ((failures)); then
  echo "$failures failed"
  exit 1
fi
echo "all passed"
