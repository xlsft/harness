# harness desktop

The shared UI (`packages/ui`) in a native window (Tauri 2), with `harness-client` as a sidecar.

The shell owns the sidecar: it checks the sign-in (`harness-client whoami --json`), runs the device-flow
sign-in (`login --json`, the page opens in the browser), then starts `harness-client serve --listen` on a
free loopback port with a random token and hands the endpoint to the UI. Native extras: the folder picker
for new sessions, Show in Finder and Open in default app for changed files.

```bash
pnpm install                 # once
pnpm dev                     # builds the sidecar, then runs the app against the UI dev server (:3210)
pnpm build                   # release bundle (.app / .dmg, .msi, .deb / .AppImage)
```

`pnpm dev` reuses a UI dev server already running on :3210, otherwise it starts one. `HARNESS_HOME` is
passed through to the sidecar, so `HARNESS_HOME=/tmp/fresh pnpm dev` starts signed out.
