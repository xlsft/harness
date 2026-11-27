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
[[ "$($parse v0.2.0-rc.1)" == version=0.2.0-rc.1 ]] && ok "a tag with a suffix is a normal release" || no "a tag with a suffix is a normal release"
newest="$root/scripts/release/is-newest.sh"
for pair in "v0.1.0 '' true" "v0.1.0-rc.2 v0.1.0-rc.1 true" "v0.1.0 v0.1.0-rc.2 true" "v0.1.0-rc.3 v0.1.0 false" "v0.1.5 v0.2.0 false" "v0.2.0 v0.1.9 true" "v0.1.0-rc.10 v0.1.0-rc.9 true"; do
  eval "set -- $pair"
  [[ "$($newest "$1" "$2")" == "$3" ]] && ok "is-newest $1 over '$2' is $3" || no "is-newest $1 over '$2' is $3"
done

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
expect "notes list what came since the previous release tag" "grep -q '^- third thing$' '$work/changes' && ! grep -q '^- Second$' '$work/changes' && ! grep -q First '$work/changes'"
(cd "$repo" && $notes changes v0.1.0) >"$work/first"
expect "the first release lists from the beginning" "grep -q '^- First$' '$work/first'"
(cd "$repo" && $notes full v0.2.0 unsigned) >"$work/unsigned"
expect "unsigned notes say how to open it" "grep -q 'Open' '$work/unsigned' && grep -q 'com.apple.quarantine' '$work/unsigned' && grep -q 'Accessibility' '$work/unsigned'"
(cd "$repo" && $notes full v0.2.0 notarized) >"$work/notarized"
refuse "notarized notes don't" "grep -q 'com.apple.quarantine' '$work/notarized'"
expect "notes name the installer packages" "grep -q 'harness_0.2.0_darwin_arm64.pkg' '$work/unsigned' && grep -q 'It is unsigned' '$work/unsigned'"
(cd "$repo" && $notes full v0.2.0 notarized signed) >"$work/pkg-signed"
expect "...and how the package is signed" "grep -q 'Developer ID Installer certificate, not notarized' '$work/pkg-signed'"

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

# offline-bundle.sh from a folder of release files: no download, no image.
bundle="$root/scripts/release/offline-bundle.sh"
rel="$work/rel"
mkdir -p "$rel"
for f in harness_1.2.3_darwin_arm64.dmg harness_1.2.3_darwin_arm64.pkg harness_1.2.3_darwin_arm64.app.tar.gz harness-client_1.2.3_darwin_arm64.tar.gz; do
  echo "$f" >"$rel/$f"
done
echo '{"version":"1.2.3","platforms":{"darwin-aarch64":{"signature":"S","url":"https://github.com/xlsft/harness/releases/download/v1.2.3/harness_1.2.3_darwin_arm64.app.tar.gz"}}}' >"$rel/latest.json"
(cd "$rel" && sums="$(shasum -a 256 ./* | sed "s|  \./|  |")" && printf "%s\n" "$sums" >checksums.txt && echo SIG >checksums.txt.sig)
off() { PATH="$stub:$PATH" "$bundle" "$@" 2>/dev/null; }
refuse "offline-bundle needs the URL it will be served at" "off v1.2.3 '$work/mirror' --from '$rel' --no-image"
refuse "offline-bundle refuses a bad tag" "off 1.2.3 '$work/mirror' --from '$rel' --no-image --base-url https://m.example.com"
expect "offline-bundle copies a release" "off v1.2.3 '$work/mirror' --from '$rel' --no-image --base-url https://m.example.com/harness/"
expect "...every file under its tag" "cmp '$rel/checksums.txt' '$work/mirror/v1.2.3/checksums.txt' && test -f '$work/mirror/v1.2.3/harness_1.2.3_darwin_arm64.pkg'"
[[ "$(node -p "require('$work/mirror/latest.json').platforms['darwin-aarch64'].url")" == https://m.example.com/harness/v1.2.3/harness_1.2.3_darwin_arm64.app.tar.gz ]] && ok "...latest.json points into the mirror" || no "...latest.json points into the mirror"
[[ "$(node -p "require('$work/mirror/latest.json').platforms['darwin-aarch64'].signature")" == S ]] && ok "...with the release's signatures" || no "...with the release's signatures"
expect "...and SHA256SUMS covers the folder" "(cd '$work/mirror' && shasum -a 256 -c --quiet SHA256SUMS) && grep -q 'v1.2.3/latest.json' '$work/mirror/SHA256SUMS'"
echo '{"version":"1.2.2","platforms":{}}' >"$rel/latest.json"
(cd "$rel" && sums="$(shasum -a 256 ./* | grep -v checksums | sed "s|  \./|  |")" && printf "%s\n" "$sums" >checksums.txt)
rm -rf "$work/mirror/v1.2.2"
expect "--keep-latest adds a release without offering it" "off v1.2.2 '$work/mirror' --from '$rel' --no-image --keep-latest --base-url https://m.example.com/harness && [[ \$(node -p \"require('$work/mirror/latest.json').version\") == 1.2.3 ]]"
echo tampered >"$rel/harness_1.2.3_darwin_arm64.dmg"
refuse "offline-bundle refuses files that don't match checksums.txt" "off v1.2.2 '$work/mirror2' --from '$rel' --no-image --base-url https://m.example.com"

# The dev container feature's install.sh, from a mirror folder over file://, into folders of the test's own.
feature="$root/deploy/devcontainer/harness-client/install.sh"
case "$(uname -s)" in Linux) fos=linux ;; *) fos=darwin ;; esac
case "$(uname -m)" in x86_64 | amd64) farch=amd64 ;; *) farch=arm64 ;; esac
fm="$work/fmirror"
mkdir -p "$fm/v3.4.5" "$work/fbin"
printf '#!/bin/sh\necho harness-client/3.4.5\n' >"$work/fbin/harness-client"
chmod 755 "$work/fbin/harness-client"
tar -czf "$fm/v3.4.5/harness-client_3.4.5_${fos}_${farch}.tar.gz" -C "$work/fbin" harness-client
(cd "$fm/v3.4.5" && shasum -a 256 ./*.tar.gz | sed 's|  \./|  |' >checksums.txt)
echo '{"version":"3.4.5","platforms":{}}' >"$fm/latest.json"
finstall() { env -u VERSION HARNESS_INSTALL_DIR="$work/fout/bin" HARNESS_POLICY_DIR="$work/fout/policy" PATH="/usr/bin:/bin:/usr/sbin:/sbin" "$@" sh "$feature" 2>/dev/null; }
expect "the feature installs the latest release of a mirror" "finstall MIRROR='file://$fm' SERVERURL=https://harness.example.com/"
expect "...a client that runs" "'$work/fout/bin/harness-client' version | grep -q 3.4.5"
fpolicy="$work/fout/policy/00-devcontainer.json"
[[ "$(node -p "const p=require('$fpolicy'); [p.serverUrl, p.disableUpdates, p.updateMirrorUrl].join(' ')")" == "https://harness.example.com true " ]] && ok "...and the container's machine policy" || no "...and the container's machine policy"
refuse "the feature refuses a serverUrl that isn't a plain URL" "finstall MIRROR='file://$fm' SERVERURL='https://x/\"y'"
refuse "the feature refuses a version the mirror doesn't have" "finstall MIRROR='file://$fm' VERSION=9.9.9"
echo tampered >>"$fm/v3.4.5/harness-client_3.4.5_${fos}_${farch}.tar.gz"
refuse "the feature refuses an archive that doesn't match checksums.txt" "finstall MIRROR='file://$fm' VERSION=3.4.5"
fkey="$(sed -n 's/^PUBKEY="\(.*\)"$/\1/p' "$feature")"
[[ "$(node -p "require('$root/packages/desktop/src-tauri/tauri.conf.json').plugins.updater.pubkey" | base64 -d | tail -n1)" == "$fkey" ]] && ok "the feature trusts the release key of tauri.conf.json" || no "the feature trusts the release key of tauri.conf.json"

# build-pkg.sh on a stand-in app (macOS only: pkgbuild). Nothing is installed: the package is taken apart, and its
# postinstall script runs against a folder of the test's own.
if command -v pkgbuild >/dev/null && command -v productbuild >/dev/null; then
  pkg="$root/scripts/release/build-pkg.sh"
  fake="$work/pkg/Fake.app"
  mkdir -p "$fake/Contents/MacOS"
  cp /usr/bin/true "$fake/Contents/MacOS/fake"
  info="$fake/Contents/Info.plist"
  plutil -create xml1 "$info"
  for kv in CFBundleIdentifier=dev.xlsft.harness CFBundleShortVersionString=1.2.3 CFBundleExecutable=fake LSMinimumSystemVersion=11.0 CFBundlePackageType=APPL; do
    plutil -insert "${kv%%=*}" -string "${kv#*=}" "$info"
  done
  codesign -s - --force --deep "$fake" 2>/dev/null
  (cd "$work/pkg" && tar -czf Fake.app.tar.gz Fake.app)
  for bad in "ftp://x" "https://a.example.com/'quoted'" "not a url"; do
    refuse "build-pkg refuses the server URL '$bad'" "APPLE_INSTALLER_IDENTITY= '$pkg' '$fake' '$work/pkg/bad.pkg' --server-url \"$bad\""
  done
  expect "build-pkg makes a package from the app" "APPLE_INSTALLER_IDENTITY= '$pkg' '$fake' '$work/pkg/plain.pkg' >'$work/pkg/plain.out'"
  [[ "$(tail -n1 "$work/pkg/plain.out")" == unsigned ]] && ok "...unsigned without an installer certificate" || no "...unsigned without an installer certificate"
  expect "...and from the release's .app.tar.gz, with a server URL" "'$pkg' '$work/pkg/Fake.app.tar.gz' '$work/pkg/server.pkg' --server-url https://harness.example.com/ --version 1.2.4 >/dev/null 2>&1"
  pkgutil --expand-full "$work/pkg/plain.pkg" "$work/pkg/plain" >/dev/null
  pkgutil --expand-full "$work/pkg/server.pkg" "$work/pkg/server" >/dev/null
  expect "the payload is the app alone, into /Applications" "[[ \$(ls '$work/pkg/plain/harness.pkg/Payload') == Fake.app ]] && grep -q 'install-location=\"/Applications\"' '$work/pkg/plain/harness.pkg/PackageInfo'"
  expect "...and it isn't relocated to another copy" "grep -q 'relocatable=\"false\"' '$work/pkg/plain/harness.pkg/PackageInfo'"
  expect "the package names the app's architectures and macOS" "grep -q 'hostArchitectures=\"arm64,x86_64\"' '$work/pkg/plain/Distribution' && grep -q 'os-version min=\"11.0\"' '$work/pkg/plain/Distribution'"
  expect "the version is the app's, or the one given" "grep -q 'version=\"1.2.3\"' '$work/pkg/plain/harness.pkg/PackageInfo' && grep -q 'version=\"1.2.4\"' '$work/pkg/server/harness.pkg/PackageInfo'"
  refuse "a package without a URL has no scripts" "test -e '$work/pkg/plain/harness.pkg/Scripts'"
  mkdir -p "$work/pkg/target"
  expect "the postinstall script writes the server URL" "sh '$work/pkg/server/harness.pkg/Scripts/postinstall' x / '$work/pkg/target'"
  written="$work/pkg/target/Library/Application Support/harness/settings.d/00-server.json"
  expect "...as the machine policy the client reads" "[[ \"\$(node -p \"require('$written').serverUrl\")\" == https://harness.example.com ]]"
else
  echo "skip build-pkg.sh: no pkgbuild (not macOS)"
fi

# The real image never sets a minimum client version.
refuse "the image sets no minimum client version" "grep -rq HARNESS_MIN_CLIENT_VERSION '$root/deploy/Dockerfile' '$root/deploy/rootfs'"

if ((failures)); then
  echo "$failures failed"
  exit 1
fi
echo "all passed"
