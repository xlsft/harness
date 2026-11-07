# Install and operations

Working notes for closing the install and operations rows of [the comparison](../comparison.md): install script, Homebrew,
`doctor`, shell completions, version pin or floor by policy, deep links, and the web UI in the server image. Releases are
macOS only for now (darwin arm64 and amd64); everything is written so a Linux build slots in by adding its name.

Everything here was tested against fakes: a fake release server, a stand-in `cosign`, fake POLICY documents, a fake core.
No real GitHub release, tap repository, Homebrew install or signed app was used.

## Decisions

- **The install script trusts `checksums.txt` and, when `cosign` is installed, its Sigstore bundle.** The archive must
  match its line in `checksums.txt`, always. With `cosign` on the PATH the bundle is verified against this repository's
  release workflow identity, and a failure stops the install; without it the script says the signature wasn't checked and
  how to check it. `--require-signature` makes a missing `cosign` an error. The minisign signature (`checksums.txt.sig`) is
  what `harness-client update` checks later with the key stamped into the binary; a shell script has no portable way to
  verify it, so it doesn't pretend to.
- **The install script never uses sudo** and never writes outside its prefix (`~/.harness` by default, binaries in
  `<prefix>/bin`). It doesn't edit shell profiles either: it prints the line to add. The whole script is one function called
  on the last line, so a download cut short runs nothing.
- **The release server is a parameter** (`HARNESS_INSTALL_BASE_URL`, default `https://github.com/xlsft/harness`), which is
  how the tests point it at a fake. The latest version comes from the `releases/latest` redirect, like `harness-client
  update`, so there is no API rate limit.
- **Homebrew files are generated from the release's own `checksums.txt`** by `scripts/release/homebrew.sh`, not by
  Goreleaser: Goreleaser builds only the client archives, the DMGs come from another job, and one script that reads the
  final checksums covers both the formula and the cask. The tap is `xlsft/homebrew-harness`; the workflow pushes to it only
  when the `HOMEBREW_TAP_TOKEN` secret exists and the release is the newest stable one.
- **Completions are generated from the commands' own flag sets** (`packages/client/completion`), shared by `harness-client`
  and `harness`, so a new flag shows up without editing a script. No completion framework: both binaries use plain pflag.

## Status

| Row | Status | Where |
|---|---|---|
| Install script | in progress | `scripts/install.sh`, `scripts/install-test.sh` |
| Homebrew | open | |
| Health check command | open | |
| Shell completions | open | |
| Version pin or floor by policy | open | |
| Deep links into the app | open | |
| Web client: server image doesn't serve it | open | |
