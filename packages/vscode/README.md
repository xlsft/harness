# xlsft\`s harness for VS Code

The xlsft\`s harness agent in VS Code and compatible editors. The session panel is the same UI as the desktop app (`packages/ui`); what the extension adds is what an editor can give. The design and its decisions are in [docs/design/ide.md](../../docs/design/ide.md).

## What it does

| | |
|---|---|
| Client | Uses the client at `harness.agentUrl`, else one you started with `harness-client serve --listen`, else starts its own: `harness.clientPath`, then `PATH`, then `~/.harness/bin` |
| Panel | `xlsft`s harness: Open Panel` (`Cmd/Ctrl+Alt+H`) |
| Selection and open file | `Add Selection or File to Context` (`Cmd/Ctrl+Alt+K`) adds `@path#L10-20`, or `@path` without a selection. The client expands the lines from the live buffer, so unsaved text counts |
| `@`-mentions | Type `@` in the composer, or use `Add File to Context` in the explorer or a tab's menu |
| Edits | When a file tool would ask, its change also opens in the native diff. Accept (`Cmd/Ctrl+Enter`), reject (`Cmd/Ctrl+Backspace`) or reject with a note from the title bar; or answer in the panel, and the diff closes. Closing the diff answers nothing |
| Edits nobody was asked about | Sessions start in `auto` mode, which approves edits inside the folder. Such an edit opens in the native diff after it is applied, without taking the keyboard: keep it, or restore the file. `harness.showAppliedEdits` turns this off |
| Every edit first | Preference `ideReviewAllEdits` (default off) holds edits the mode would allow until the diff is answered |
| Session changes | `Review Session Changes`, or the count in the status bar: each changed file in the native diff against HEAD, or the session's snapshot outside git. Restoring a file asks first |
| Problems panel | The agent's `EditorDiagnostics` tool, while an editor is attached |
| Files from the transcript | The Open button on a file tool's row, and inline code that names a file, such as `src/a.ts:12`, open at the line. So does the agent's `Show` and `OpenInEditor` |
| Terminal | Terminals of this window reach this window's bridge. `Open the Terminal Interface` runs `harness` in one; `Add Last Terminal Command to Context` needs shell integration |

Not signed in? `Sign In` runs the device flow in a terminal.

## Developing

Node 24, pnpm and Go. `--ui` needs `pnpm install` in `packages/ui`.

```bash
pnpm install --ignore-workspace
pnpm typecheck
pnpm test                       # plain Node: the bridge, finding a client, the page
node scripts/build.mjs          # dist/extension.cjs
node scripts/build.mjs --ui     # also the panel, into media/ui
pnpm package                    # the panel and dist/harness-<version>.vsix
pnpm test:editor                # a real VS Code, a real client, the fake gateway; after --ui
```

`pnpm test:editor` downloads VS Code into `.vscode-test`, git-ignored, about 500 MB unpacked. On Linux without a display, run it under `xvfb-run -a`. `HARNESS_TEST_VSIX=dist/harness-0.0.0-dev.vsix` runs it on what was packed. Build outputs and `.vsix` files are never committed, and nothing here publishes to a marketplace.

To try it, install the `.vsix` with `code --install-extension dist/harness-0.0.0-dev.vsix`.
