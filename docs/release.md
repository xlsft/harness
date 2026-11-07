# Releasing harness

A release is one tag, `vX.Y.Z`, and one version for everything. It produces:

| What | Where | Platforms |
|---|---|---|
| Server image | `ghcr.io/xlsft/harness:X.Y.Z`, `X.Y`, `latest`; signed, with an SBOM and provenance | `linux/amd64`, `linux/arm64` |
| Desktop app | GitHub Release: `harness_X.Y.Z_darwin_<arch>.dmg`, and the `.app.tar.gz` the updater fetches | macOS, Apple Silicon and Intel |
| `harness-client` | GitHub Release: `harness-client_X.Y.Z_darwin_<arch>.tar.gz`, with an SBOM each | macOS, Apple Silicon and Intel |
| Metadata | `checksums.txt`, signed twice (`.sigstore.json`, `.sig`); `latest.json`; a source SBOM; `harness.intoto.jsonl` | |

Linux and Windows clients and the terminal frontend (`harness`) aren't released yet; CI keeps them compiling.

## Before the first release

Once, and it needs nothing from Apple:

1. **Generate the update signing key.** It is what installed apps and clients trust, so whoever holds it can ship them code.

   ```bash
   pnpm --dir packages/desktop tauri signer generate -w ~/.tauri/harness.key
   ```

   It asks for a password; use one. Keep the key and the password in your password manager. **If you lose either, installed
   apps can never update again**: people would have to reinstall by hand.
2. **Commit the public half** (`~/.tauri/harness.key.pub`) as `plugins.updater.pubkey` in
   `packages/desktop/src-tauri/tauri.conf.json`. It is public; it belongs in the repository. It is the single source: the
   release also stamps it into `harness-client`.
3. **Set the secrets** (Settings → Secrets and variables → Actions, or as below):

   ```bash
   gh secret set TAURI_SIGNING_PRIVATE_KEY < ~/.tauri/harness.key
   gh secret set TAURI_SIGNING_PRIVATE_KEY_PASSWORD
   ```

While `pubkey` is empty, everything still builds and runs for development (Settings → About says this app can't update
itself), and only a release refuses, in its first job and in `scripts/release.sh`, before it spends anything.

GHCR needs no setup: the workflow's own token publishes `ghcr.io/xlsft/harness`. After the first push, make the package
public (Packages → harness → Package settings) if it didn't inherit that from the repository.

## Cutting a release

```bash
scripts/release.sh v0.1.0               # checks a clean, pushed main; prints the commands
scripts/release.sh v0.1.0 --create-tag  # the same, and creates the local tag
git push origin v0.1.0                  # this is what starts it
```

**A release only comes from a commit whose CI is green.** Each of the workflows `cli`, `client`, `core`, `desktop`, `e2e`,
`image`, `proto`, `ui` and `workflows` needs a successful run on that exact commit: its newest run counts, a pull request's
doesn't, and a missing or unfinished result never passes (`scripts/release/ci-green.sh`). There is no way to skip this. Most
workflows run on a push only when their paths changed, so a commit usually lacks some; `scripts/release.sh` says which and
prints the command for each, such as `gh workflow run image.yml --ref main`. Run them with `main` at that commit, wait for them,
then tag.

Tags are `vX.Y.Z` or `vX.Y.Z-rc.N`; anything else is refused. The tag must be on `main`. The tag is the source of truth: the
workflow stamps its version into the client (`-ldflags`), core (a build arg, reported by `/api/health`), the UI, and
`tauri.conf.json` and `Cargo.toml` in its own checkout (`scripts/release/stamp.mjs`). Nothing is committed back, so those
files say `0.1.0` or `0.0.0-dev` in the repository and that's fine.

**Every release is a normal release.** A suffix (`v0.2.0-rc.1`) only names it: it is published like any other, moves the
image's `X.Y` and `latest` when it is the newest, and both updaters offer it. "Newest" is decided by semver
(`scripts/release/is-newest.sh`: `0.2.0-rc.1` < `0.2.0` < `0.2.1`), not by `sort -V`, which orders a suffix after the
plain version.

**A backport** (`v0.1.5` after `v0.2.0`) moves `0.1` but not `latest`, and isn't offered to anyone on `0.2.0`.

### What runs

`.github/workflows/release.yml`:

1. `preflight`, which everything else waits for, refuses a bad tag, a tag not on `main`, a commit whose CI isn't green (it waits
   up to 90 minutes for what's still running), a missing update key, or an image that sets `HARNESS_MIN_CLIENT_VERSION`.
2. `core`, `client`, `cli`, `ui`, `proto`, `e2e`: the normal checks, as reusable workflows, whatever their path filters say.
3. `image` (`image.yml`): builds amd64, starts it (`scripts/e2e/image-smoke.sh`), builds both architectures, pushes `X.Y.Z`
   and signs it.
4. `desktop` (`desktop.yml`) and `client-archives` (Goreleaser, `.goreleaser.yaml`) build for both architectures and keep the
   results as workflow artifacts.
5. `publish` assembles (`scripts/release/assemble.sh`), signs, creates a **draft** release, attaches provenance, moves the
   image's `X.Y` and `latest` to the same digest, and only then publishes the draft.

### If it fails

Nothing visible changes until the very end of `publish`. Until then:

| Failed in | What's left | What to do |
|---|---|---|
| `preflight`, CI not green | nothing | Get CI green on that commit (above), then re-run the failed job |
| `preflight` otherwise, or a check | nothing | Fix on `main`, delete the tag (`git push origin :v0.1.0; git tag -d v0.1.0`), tag again |
| `image`, `desktop`, `client-archives` | maybe the image under its exact `X.Y.Z`, not `latest` | If the cause was outside the code (a flaky runner), re-run the failed jobs. Otherwise fix, delete the tag, and tag again, or use the next patch version |
| `publish`, before "Move the image tags" | also a draft release, which nobody can see or be offered | Re-run the failed job: it replaces the draft |
| `publish`, "Move the image tags" or "Publish" | possibly `latest` on the new image with the release still a draft | Re-run the failed job, which is safe to repeat |

An exact image version that's already pushed gets overwritten when the tag is rebuilt, and that's fine as long as no release
pointed at it. Don't reuse the version of a published release: make the next one. To withdraw a bad release, delete it on GitHub (updaters then go back to the previous one for new checks) and retag the image's `latest`.

## Signing the macOS app

Three levels. The workflow picks one by which secrets exist; the release builds at all of them, and its notes tell people how
to open what they got.

| Level | Secrets | First open | Screen Recording, Accessibility after an update |
|---|---|---|---|
| **Ad-hoc**, the default | none | blocked; see below | **Lost** |
| **Developer ID** | `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY` | blocked; see below | Kept |
| **Developer ID, notarized** | also `APPLE_ID`, `APPLE_PASSWORD`, `APPLE_TEAM_ID` | opens normally | Kept |

Ad-hoc is a normal way to release, not a broken one: Apple Silicon requires every binary to carry a signature, and
`signingIdentity: "-"` gives the app, the sidecar and the computer-use helper one that identifies nothing. The Tauri update
signature is separate and works at every level: an update's origin is proven either way.

### The DMG's window

`bundle.macOS.dmg` in `tauri.conf.json` sets it: background, size, icon positions. They end up in the volume's `.DS_Store`,
written by Finder through AppleScript while the DMG is made. Two things go wrong with that. Tauri skips the step when `CI=true`,
so `packages/desktop/scripts/build-release.sh` sets `TAURI_BUNDLER_DMG_IGNORE_CI=true`. And Finder is unreliable: in about one
run in three, on any Mac, it saves the window with no background and 48 px icons, which look shifted, while every command
reports success. So the script checks each DMG on the mounted volume (`scripts/check-dmg.py`: background, icon size and
positions, window size, an executable that runs on this macOS) and makes it again, up to eight times, then fails the `desktop`
job before anything is public. If that happens on every run, the runner image has stopped allowing Finder automation: that
needs fixing in the workflow. A plain `pnpm build` has no such check, so judge a local DMG by running `check-dmg.py` on it.

### Opening an app that isn't notarized

Move it to Applications first: an app started from the disk image or from Downloads runs from a read-only place macOS picks for
it, and can't update itself. Open it once, dismiss the warning, then either:

- **System Settings → Privacy & Security**, scroll to Security, **Open Anyway**, enter your password. The button stays for about
  an hour after the attempt.
- Or in a terminal: `xattr -dr com.apple.quarantine "/Applications/xlsft\`s harness.app"`

This happens once per install. Updates from inside the app don't bring it back, because they aren't quarantined.

### Ad-hoc and computer use

macOS keeps Screen Recording and Accessibility grants against an app's code signature. An ad-hoc signature has no stable
identity: its designated requirement is the hash of that exact build (`cdhash H"..."`, see `codesign -d -r- <app>`), so each
version is a different app to macOS. After an update the old grant doesn't apply, and System Settings may still show the
switch as on. To restore it, select the app in the list, remove it with **−**, and add it again (or switch it off and on), then
restart the app. Settings → Computer use shows what the app actually has. Everything else in the app is unaffected.

A Developer ID's requirement names the team and the bundle id, not the build, so grants carry over. That is the concrete
difference paying for one makes, along with no warning at first open.

## Getting a Developer ID (optional)

Apple's current pages: [Developer ID](https://developer.apple.com/developer-id/),
[creating the certificate](https://developer.apple.com/help/account/certificates/create-developer-id-certificates/),
[a signing request](https://developer.apple.com/help/account/certificates/create-a-certificate-signing-request),
[app-specific passwords](https://support.apple.com/en-us/102654). Checked November 2026.

1. **Join the [Apple Developer Program](https://developer.apple.com/programs/).** It's a paid yearly membership; see that page
   for the current price. Only the team's **Account Holder** can create this certificate, which on a personal membership is you.
2. **Make a signing request**, on your Mac. Keychain Access (`/Applications/Utilities`, or Spotlight) → menu **Keychain Access →
   Certificate Assistant → Request a Certificate from a Certificate Authority**. Your email, a common name such as
   `harness Developer ID`, leave CA Email empty, choose **Saved to disk**, Continue.
3. **Create the certificate.** [Certificates, Identifiers & Profiles](https://developer.apple.com/account/resources) →
   **Certificates** → **+** → under Software choose **Developer ID**, Continue → **Developer ID Application** → **Choose File**,
   pick the `.certSigningRequest`, Continue → **Download**. Double-click the `.cer`; it appears under **My Certificates** in
   Keychain Access. Choose Developer ID *Application*: *Installer* is for `.pkg` files.
4. **Export it with its key.** In Keychain Access, login keychain, **My Certificates**: expand the certificate, right-click the
   private key, **Export**, format `.p12`, and set a strong password. Keep this file safe and don't commit it.
5. **Set the secrets**, without the values passing through your shell history:

   ```bash
   base64 -i ~/harness-developer-id.p12 | gh secret set APPLE_CERTIFICATE
   gh secret set APPLE_CERTIFICATE_PASSWORD     # the .p12 password; gh asks for it
   security find-identity -v -p codesigning     # shows "Developer ID Application: Your Name (TEAMID)"
   gh secret set APPLE_SIGNING_IDENTITY         # that whole quoted name
   rm ~/harness-developer-id.p12
   ```

   The next release is signed but not notarized: still the warning on first open, but grants survive updates.
6. **Add notarization.** Sign in to [account.apple.com](https://account.apple.com) (two-factor authentication must be on) →
   **Sign-In and Security** → **App-Specific Passwords** → create one named `harness notarization`. Your Team ID is on the
   [membership page](https://developer.apple.com/account#MembershipDetailsCard), and in the parentheses above.

   ```bash
   gh secret set APPLE_ID          # the Apple Account's email
   gh secret set APPLE_PASSWORD    # the app-specific password, not your account's password
   gh secret set APPLE_TEAM_ID
   ```

Moving from ad-hoc to Developer ID is a one-time change of identity: people's grants are lost once more on that update, and
then stay. The certificate lasts years, and a release already out stays valid after it expires, if it was notarized.

## All the secrets

| Secret | Required | What |
|---|---|---|
| `TAURI_SIGNING_PRIVATE_KEY` | **yes** | Contents of the key file from `tauri signer generate` |
| `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | if the key has one | Its password |
| `APPLE_CERTIFICATE` | no | Base64 of the Developer ID Application `.p12` |
| `APPLE_CERTIFICATE_PASSWORD` | with the certificate | The `.p12`'s password |
| `APPLE_SIGNING_IDENTITY` | with the certificate | `Developer ID Application: Your Name (TEAMID)` |
| `APPLE_ID` | to notarize | Apple Account email |
| `APPLE_PASSWORD` | to notarize | An app-specific password |
| `APPLE_TEAM_ID` | to notarize | 10-character Team ID |

## How things update

**The desktop app** looks for `latest.json` of GitHub's latest release at start and every four hours (Tauri's updater). When
there's a newer one, a quiet "Update available · vX" row appears in the sidebar and in Settings → About, with the notes and an
Update button. The download is verified against `plugins.updater.pubkey`, including the version it was signed for
(`requireSignedVersion`), so an old release can't be passed off as a new one. If something is running, the app warns before it
downloads and again before it restarts, and the user can say not now: the row then says "Restart to update". The sidecar and
the helper are inside the bundle and come with it.

**`harness-client update [--check]`** finds the latest release, verifies `checksums.txt` against the key stamped into the binary,
verifies the archive against it, runs the new binary once, and renames it over itself. On any failure the old binary stays. The
archive name carries the version, so an older signed `checksums.txt` lists nothing it would accept. The app's sidecar refuses
this command and says so. A build not from a release has no key and refuses; a development build also asks for `--force`. A
standalone client has no computer-use helper: that comes only with the app.

**The server** doesn't update itself. Settings → About shows its version and whether a newer one exists (the client asks GitHub;
the server isn't involved, so an air-gapped server stays quiet), and gives admins the commands.
[deploy/README.md](../deploy/README.md#upgrading) has the procedure.

## Client and server versions

`checkClientVersion` in `packages/core/src/rpc/context.ts` refuses a client older than `HARNESS_MIN_CLIENT_VERSION`, on every
authenticated call and on sign-in. It's unset by default, so nothing is refused, and a release must not set it: `preflight` and
`scripts/release.sh` stop if the image does. Only an admin sets it, for their own server. When one is set, the check reads the
number from the client's User-Agent and ignores any suffix, so `0.2.0-rc.1` counts as `0.2.0`, and a development build
(`0.0.0-dev`) counts as `0.0.0` and is refused. Non-harness user agents aren't checked.

What keeps a client one version behind working is `buf breaking`, which runs on pull requests against `main`: contracts stay
backward compatible within `v1`.

## Verifying a release

```bash
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/xlsft/harness/\.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify harness-client_0.1.0_darwin_arm64.tar.gz --repo xlsft/harness
cosign verify ghcr.io/xlsft/harness:0.1.0 \
  --certificate-identity-regexp '^https://github.com/xlsft/harness/\.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
docker buildx imagetools inspect ghcr.io/xlsft/harness:0.1.0 --format '{{json .SBOM}}'
```

## Testing the release machinery

Nothing here publishes:

```bash
scripts/release/test.sh                          # the scripts, in throwaway repositories
go -C packages/client test ./selfupdate          # the client's update against a fake GitHub
cargo test --manifest-path packages/desktop/src-tauri/Cargo.toml   # the app's update against a fake server
actionlint                                       # the workflows
goreleaser release --snapshot --clean --skip=sbom   # with HARNESS_UPDATE_PUBKEY set to anything
TAURI_SIGNING_PRIVATE_KEY= bash packages/desktop/scripts/build-release.sh aarch64-apple-darwin /tmp/out   # a real, ad-hoc bundle
```

`real_bundles` in `src-tauri/src/update_tests.rs` (`--ignored`) updates a real older bundle to a real newer one.
