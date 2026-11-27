# Deploying xlsft\`s harness to managed machines

How an administrator puts the desktop app and `harness-client` on company machines, points them at the company's server,
sets policy on each machine, and keeps them updated from an internal mirror instead of GitHub. Releases ship for macOS
today; Windows has a policy template, and its packages come with the Windows releases.

Nothing here replaces the server's own [managed policy](../deploy/README.md#managed-policy) (Settings → Managed policy): that
one reaches every signed-in client through sync. Machine policy is for what has to be in place before anyone signs in (which
server), for machines that should obey without a server, and for what only a machine can decide (where updates come from).

## What a machine reads

`harness-client`, and the app through it, reads these places, in this order:

| Where | Written by |
|---|---|
| macOS: managed preferences of the domain `com.xlsft.harness` (`/Library/Managed Preferences/com.xlsft.harness.plist`) | A configuration profile, installed by device management |
| Windows: `HKLM\SOFTWARE\Policies\xlsft\harness` | Group Policy (the ADMX template), Intune, or a `.reg` file |
| The system folder: `settings.json`, then `settings.d/*.json` in name order. `/Library/Application Support/harness` on macOS, `C:\Program Files\harness` on Windows, `/etc/harness` on Linux | An installer package, a configuration tool, a dev container |

Each is a policy with the force of the server's managed policy: rules add up, and a restriction in any of them stands. Only
an administrator can write any of them. **[deploy/mdm/keys.md](../deploy/mdm/keys.md) lists every key.** Three of them are
**machine keys**, read only from these places, never from the server:

| Key | What |
|---|---|
| `serverUrl` | The server to sign in to. The app's sign-in screen shows it and nothing else; `harness-client login` takes it when given no URL |
| `disableUpdates` | No update checks or installs, in the app or `harness-client update`. For machines that get new versions from device management |
| `updateMirrorUrl` | Updates come from this mirror instead of GitHub ([below](#an-internal-mirror-and-offline-installs)) |

Check what a machine sees with `harness-client config machine` (each value with the place it came from) and
`harness-client config debug` (everything, policy included).

## The templates

[deploy/mdm](../deploy/mdm) holds templates generated from one schema, `packages/client/settings/policy.schema.json`, by
`scripts/policy-templates.sh`. A test fails when the schema changes and they weren't regenerated, so they always carry every
key the client reads, with the right types.

| File | For |
|---|---|
| `macos/com.xlsft.harness.mobileconfig` | A configuration profile: any MDM that uploads one (Kandji, Jamf, Intune, Mosyle, Workspace ONE, Fleet) |
| `macos/com.xlsft.harness.plist` | The plain preference file: Intune's "Preference file" profile, Jamf's "Application & Custom Settings → Upload" |
| `macos/jamf-custom-schema.json` | Jamf Pro's custom schema: a form with every key, its type and its description |
| `windows/harness.admx`, `windows/en-US/harness.adml` | Group Policy, or Intune's imported ADMX |
| `windows/harness.reg` | An example to import by hand |
| `keys.md` | The reference |

Each sets the same starting values: `serverUrl` to `https://harness.example.com` (change it), `disableUpdates` false, and
bypass mode turned off. Add the keys you want from `keys.md`.

**Deploy profiles to the device, not the user.** The client reads the computer-level managed preferences only. In Jamf that is a
computer configuration profile, in Intune a device-channel profile, in Kandji any library item (they are device-level).

### macOS: the profile

The template uses the `com.apple.ManagedClient.preferences` payload, which every macOS MDM accepts. Before uploading a copy:
set `serverUrl`, give `PayloadIdentifier` and both `PayloadUUID`s values of your own (`uuidgen`), and set `PayloadOrganization`.
`plutil -lint` checks the file. Don't install it with a double-click on a machine you use for this: let the MDM do it.

- **Jamf Pro**: Computers → Configuration Profiles → New → Application & Custom Settings → External Applications → Add →
  Source "Custom Schema", Preference Domain `com.xlsft.harness`, and paste `jamf-custom-schema.json`. Jamf then shows a form.
  Or upload the `.mobileconfig` (Upload in Configuration Profiles). Scope it to computers.
- **Kandji**: Library → Add New → Custom Profile, upload the `.mobileconfig`, assign it to a Blueprint.
- **Intune**: Devices → macOS → Configuration → Create → Templates → **Preference file**: domain `com.xlsft.harness`, upload
  `com.xlsft.harness.plist` after editing it. Or Templates → Custom, and upload the `.mobileconfig`.

MDM consoles change; the names above are those of their documentation as of November 2026.

### macOS: the installer package

Each release has `harness_X.Y.Z_darwin_arm64.pkg` and `_amd64.pkg`. It installs `xlsft\`s harness.app` into `/Applications`
(not wherever another copy happens to be), for Apple Silicon or Intel. Installed by the MDM agent, the app isn't quarantined,
so Gatekeeper doesn't ask at the first launch.

How it's signed depends on which secrets the release had ([docs/release.md](release.md#the-installer-package)); the release
notes say. MDM agents install packages as root without Gatekeeper's prompt, but some consoles accept only signed
packages for some app types (Intune's line-of-business apps, for one): check yours, and if it insists, use a signed release
or sign the package yourself (`productsign --sign "Developer ID Installer: ..." in.pkg out.pkg`).

**To put your server's URL into the package**, build your own from the release's app archive, on a Mac:

```bash
gh release download v0.2.0 --repo xlsft/harness --pattern 'harness_0.2.0_darwin_arm64.app.tar.gz'
scripts/release/build-pkg.sh harness_0.2.0_darwin_arm64.app.tar.gz harness-acme_0.2.0_arm64.pkg \
  --server-url https://harness.acme.example
```

Its postinstall writes `{"serverUrl": ...}` to `/Library/Application Support/harness/settings.d/00-server.json`. With
`APPLE_INSTALLER_IDENTITY` set to your Developer ID Installer identity (in your keychain), it's signed. The app inside keeps
the release's signature, so its updates still verify.

**Updates of a package-installed app.** The package installs the app owned by root, and the app's own updater would have to
replace it. Either set `disableUpdates` and push each release's package from the MDM (the usual way), or point
`updateMirrorUrl` at your mirror and leave updates on.

### Windows: the administrative template

Copy `harness.admx` to `C:\Windows\PolicyDefinitions` (or the domain's central store, `\\<domain>\SYSVOL\<domain>\Policies\
PolicyDefinitions`) and `en-US\harness.adml` next to its `en-US` folder. The policies appear under Computer Configuration →
Administrative Templates → xlsft\`s harness. In Intune: Devices → Configuration → Import ADMX, then a profile from the
imported template.

A single value (a switch, a string, a choice) is its own policy, written as a registry value named by the key's path
(`sandbox.enabled`). Lists and dictionaries (rules, allowlists) go in the **Settings (JSON)** policy: a JSON object of those
keys. A single value wins over the same key in the JSON. `harness.reg` shows both.

Windows releases of the app and the client don't exist yet; the template and the client's registry reader do, and the
reader is tested on `reg query` output, not on a Windows machine.

## An internal mirror and offline installs

`scripts/release/offline-bundle.sh` turns a published release into a folder you can carry into a network without internet
access and serve from any web server:

```bash
scripts/release/offline-bundle.sh v0.2.0 ./harness-mirror --base-url https://mirror.acme.example/harness \
  --platform linux/amd64,linux/arm64
```

| In the folder | What |
|---|---|
| `latest.json` | The release the mirror offers, its download links pointing into the mirror |
| `v0.2.0/` | Every file of the release: DMGs, packages, the app's update archives and signatures, client archives and SBOMs, `checksums.txt` and its signatures |
| `images/harness_0.2.0_linux_amd64.tar`, ... | The server image, for `docker load -i` |
| `SHA256SUMS` | Every file above, to check the copy on the other side: `shasum -a 256 -c SHA256SUMS` |

It needs `gh` (or `--from <folder>` with the files already downloaded), `docker` for the image (or `--no-image`), `node` and
`shasum`. With `cosign` installed it also checks the Sigstore signatures of `checksums.txt` and the image; either way it checks
every file against `checksums.txt`. Add later releases to the same folder: each goes under its tag, and `latest.json` moves
to it (`--keep-latest` adds one without offering it yet, to test it first).

Then set `updateMirrorUrl` to the URL the folder is served at. The app reads `<mirror>/latest.json` instead of GitHub's, and
`harness-client update` reads it for the version and fetches `<mirror>/<tag>/checksums.txt` and its archive. **The mirror is
trusted with nothing but availability**: the app verifies each update against the release key built into it, and the client
verifies `checksums.txt` with the same key, then the archive against it, exactly as from GitHub. A tampered mirror can only
fail to offer an update, or offer an older signed one, which neither accepts as new. The app needs HTTPS for the mirror;
the client takes HTTP too.

On the server side, load the image and run it as in [deploy/README.md](../deploy/README.md), with
`HARNESS_MODEL_PRICES=off` so it doesn't try to fetch prices. The server never contacts GitHub. Settings → About asks GitHub
whether a newer server exists, not the mirror; without internet access it just says nothing about it.

## Linux

Not released yet. The system folder is `/etc/harness`, and nothing else is needed: a configuration tool writes
`settings.d/*.json` there. [docs/devcontainers.md](devcontainers.md) does exactly that inside a container.
