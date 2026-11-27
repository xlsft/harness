# Install and operations

Working notes for closing the install and operations rows of [the comparison](../comparison.md): install script, Homebrew,
`doctor`, shell completions, version pin or floor by policy, deep links, and the web UI in the server image. Releases are
macOS only for now (darwin arm64 and amd64); everything is written so a Linux build slots in by adding its name.

Everything here was tested against fakes: a fake release server, a stand-in `cosign`, a local bare repository as the tap,
fake POLICY documents, a fake core health endpoint, fake MCP inventories. No real GitHub release, tap repository, Homebrew
install, signed app or registered URL scheme was used. [What only fakes have seen](#what-only-fakes-have-seen) lists it.

## Decisions

- **The install script trusts `checksums.txt` and, when `cosign` is installed, its Sigstore bundle.** The archive must
  match its line in `checksums.txt`, always. With `cosign` on the PATH the bundle is verified against this repository's
  release workflow identity, and a failure stops the install; without it the script says the signature wasn't checked.
  `--require-signature` makes a missing `cosign` an error. The minisign signature (`checksums.txt.sig`) is what
  `harness-client update` checks later with the key stamped into the binary; a shell script has no portable way to verify
  it, so it doesn't pretend to.
- **The install script never uses sudo** and never writes outside its prefix (`~/.harness` by default, binaries in
  `<prefix>/bin`). It doesn't edit shell profiles: it prints the line to add. The whole script is one function called on
  the last line, so a download cut short runs nothing. Only HTTPS is allowed, redirects included, unless the base URL itself
  is plain HTTP (the tests' loopback server). A shell under Rosetta gets the arm64 build.
- **The release server is a parameter** (`HARNESS_INSTALL_BASE_URL`, default `https://github.com/xlsft/harness`), which is
  how the tests point it at a fake. The latest version comes from the `releases/latest` redirect, like `harness-client
  update`, so there is no API rate limit. The tests fake `uname`, so they run on Linux CI as a Mac would.
- **Homebrew files are generated from the release's own `checksums.txt`** by `scripts/release/homebrew.sh`, not by
  Goreleaser: Goreleaser builds only the client archives, the DMGs come from another job, and one script that reads the
  final checksums covers both the formula and the cask. The tap is `xlsft/homebrew-harness` (`HOMEBREW_TAP` variable to
  change). A separate `homebrew` job pushes only for the newest stable release and only with the `HOMEBREW_TAP_TOKEN`
  secret, after the release is public, so it can be re-run alone. The token is sent as an HTTP header for the one command,
  never written into a remote URL. The tap's creation is left to the owner (docs/release.md).
- **A Homebrew install isn't updated by `harness-client update`:** a binary under `/Cellar/harness-client/` refuses and
  says `brew upgrade`, as the app's sidecar already refuses. The cask is `auto_updates true`: the app updates itself.
- **Completions are generated from the commands' own flag sets** (`packages/client/completion`), shared by `harness-client`
  and `harness`, so a new flag shows up without editing a script. No completion framework: both binaries use plain pflag.
  The flag definitions of `-p`, `serve`, `update` and the TUI moved into constructors (`newPrintFlags`, ...) that both the
  command and the completion tree call. Value choices (`--output-format`, `--permission-mode`, `--effort`) and directory
  flags are annotations on the flag set. The bash script works on macOS's bash 3.2.
- **`doctor` only reads, except for MCP.** Sign-in, server, keychain, sandbox, disk, git and proxy are observed, never
  changed; the keychain is read, never written (no probe item). MCP servers a session would start are started, because
  "fails to start" is the question; `--no-mcp` and `skip_mcp` skip it, and Settings > About runs without MCP on open and
  with it on the button. `--folder` adds a folder's `.mcp.json`, which for a trusted folder registers its servers like a
  session does, so it is opt-in. git on a Mac is checked with `xcode-select -p` first, because `/usr/bin/git` without the
  Command Line Tools opens an installer dialog.
- **Version skew passes within one minor version behind**, the compatibility `buf breaking` keeps; more behind, or a
  client newer than its server, warns. Development builds aren't compared.
- **The version policy warns, it doesn't refuse.** `minimumClientVersion` and `maximumClientVersion` in the POLICY document
  (`ManagedPolicy.min_client_version`/`max_client_version`, fields 270 and 271, also in `ManagedPolicyView`). Several
  documents combine to the strictest bounds. Below the minimum is a doctor failure and a warning in `serve` and `update`;
  above the maximum is a warning. `HARNESS_MIN_CLIENT_VERSION`, which does refuse, stays the admin's own server setting
  that a release never sets (`preflight` and `scripts/release.sh` still stop on it).
- **Updates stop at the maximum.** `harness-client update` installs the newest release up to the maximum, and the maximum
  itself when the newest is above it (`selfupdate.Bounds.Target`). The app's updater only knows the newest release
  (`latest.json`), so above the maximum the app is offered nothing and Settings > About says why (`Check::Held`). The UI
  passes the maximum it read from the client's preferences; the comparison is in the shell.
- **`update` reads the policy without a session** (`bootstrap.ReadManagedPolicy`): a bounded catch-up of the POLICY
  documents into the cache the mode policy watcher uses, plus the machine's own policy, with no side effects.
- **A link only opens.** `harness://session/<id>`, `harness://new?prompt=&folder=`, `harness://settings/<section>`. The shell
  (Rust) parses and validates every part and drops a link that doesn't pass whole: no unknown or repeated parameter, ids of
  `[A-Za-z0-9_-]`, sections from a fixed list, prompts up to 8000 characters without control characters other than
  newline and tab. A folder must be an absolute path to an existing directory without `.` or `..`; a bad one is dropped
  and the UI says so. The prompt never goes into the page's address: it travels as state to the new-session composer,
  which shows it under a notice and waits for the user to send it. Links that come before the UI is ready are queued in
  the shell (16 at most) and taken once the app is signed in and connected.
- **The web UI is served at `/`, not under a path.** It already was (`HARNESS_WEB_DIR`); what was missing is a switch and
  a way for clients to know. `HARNESS_WEB_UI=off` turns it off; `/api/health` reports `"web": "/"` or `""`; a client with
  no `HARNESS_WEB_URL` reports the server's UI as `InitializeResponse.web_url`. A path prefix would need the Nuxt build
  made with that base path, a second build in the image; not worth it while nothing else wants the root.

## Status

| Row | Status | Where |
|---|---|---|
| Install script | closed | `scripts/install.sh`, `scripts/install-test.sh` |
| Homebrew | closed (tap not created) | `scripts/release/homebrew.sh`, `homebrew-publish.sh`, `release.yml` job `homebrew` |
| Health check command | closed | `packages/client/doctor`, `harness-client doctor`, `AgentService.RunDoctor`, Settings > About |
| Shell completions | closed | `packages/client/completion`, `harness-client completion`, `harness completion` |
| Version pin or floor by policy | closed | managed policy fields 270/271, `selfupdate.Bounds`, `update`, desktop `update_check` |
| Deep links into the app | closed | `packages/desktop/src-tauri/src/deeplink.rs`, `useDeepLinks`, `NewSession.vue` |
| Web client: server image doesn't serve it | closed (it did; switch and discovery added) | `routes/web.ts`, `routes/health.ts`, `deploy/README.md` |

## What only fakes have seen

- The install script against GitHub, a real release and a real `cosign` (only a stand-in that records its arguments).
- The formula and cask in a real tap with `brew install` and `brew audit` (only `ruby -c` and a local bare repository).
- The `homebrew` job and the workflow changes on GitHub Actions (actionlint wasn't available here; YAML parses).
- Deep links from macOS: the scheme registration happens when the bundle is built and installed; only the parser and the
  UI's routing are tested.
- The doctor against a real signed-in server and real MCP servers; the server test uses a fake health endpoint.
- Zsh and fish completions in an interactive shell: bash is driven functionally, zsh and fish only syntax-checked (fish
  isn't installed here).
