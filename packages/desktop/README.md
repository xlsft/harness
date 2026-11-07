# harness desktop

The shared UI (`packages/ui`) in a native window (Tauri 2), with `harness-client` as a sidecar.

The shell owns the sidecar: it checks the sign-in (`harness-client whoami --json`), runs the device-flow
sign-in (`login --json`, the page opens in the browser), then starts `harness-client serve --listen` on a
free loopback port with a random token and hands the endpoint to the UI. Native extras: the folder picker
for new sessions, Show in Finder and Open in default app for changed files.

```bash
pnpm install                 # once
pnpm dev                     # builds the sidecar, then runs the app against the UI dev server (:3210)
pnpm build                   # a bundle (.app / .dmg, .msi, .deb / .AppImage), unsigned and without updater artifacts
```

## Links

The app opens `harness://` links (`src-tauri/src/deeplink.rs`, the deep-link plugin; macOS registers the scheme when the
app is installed):

| Link | Opens |
|---|---|
| `harness://session/<id>` | that session |
| `harness://new?prompt=<text>&folder=<absolute path>` | the new-session screen, the prompt in its text box and the folder chosen |
| `harness://settings/<section>` | a section of Settings (`about`, `models`, `sandbox`, ...) |

A link never runs anything. The prompt waits in the composer under a notice that it came from a link, until the user sends
it. The shell checks every part first and drops a link that doesn't pass whole: an unknown form or parameter, a repeated
one, a session id with other than letters, digits, `-` and `_`, a section not in its list, a prompt over 8000 characters or
with control characters. A folder that isn't an absolute path to a directory that exists, without `.` or `..`, is left out
and the notice says so. Links that arrive before the UI is ready wait in the shell until it is signed in and connected.

To try one in development: `open 'harness://settings/about'` (macOS routes it to an installed bundle; `pnpm dev` alone
doesn't register the scheme).

Releases are built by the release workflow through `scripts/build-release.sh`, signed if signing is set up, and update
themselves through Tauri's updater: [docs/release.md](../../docs/release.md).

`pnpm dev` reuses a UI dev server already running on :3210, otherwise it starts one. `HARNESS_HOME` is
passed through to the sidecar, so `HARNESS_HOME=/tmp/fresh pnpm dev` starts signed out.
