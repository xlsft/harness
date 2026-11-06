# IDE integration

How xlsft`s harness works inside an editor. VS Code and compatible editors (Cursor, Windsurf, VSCodium) come first, in `packages/vscode`. This page records the decisions as they were made; the JetBrains section says what a second host would take.

## Shape

```
 editor window
 +-----------------------------------------------+
 | extension host (packages/vscode)              |          +------------------+
 |   finds or starts the client  ----------------+--spawn-->| harness-client   |
 |   IdeService calls (Connect JSON) ------------+--------->| serve --listen   |
 |   editor bridge on 127.0.0.1:<port> <---------+----------| ide.Manager      |
 |                                               |          +------------------+
 | webview: packages/ui, built for the IDE ------+--Connect-->  (same listener)
 +-----------------------------------------------+
```

Three channels, each with one job:

| Channel | Direction | Carries |
|---|---|---|
| AgentService, IdeService | panel and extension to client | The whole session UI; session changes for review |
| Editor bridge (`harness.ide.v1.EditorBridgeService`) | client to extension | Show a diff, read diagnostics, open a file, read a live buffer |
| `postMessage` | extension and panel | Endpoint, draft context, open a file, current session |

## Decisions

**The client decides, the extension shows.** What needs review, what a rejection means, how a mention expands and what a file's baseline is all live in the client (`packages/client/ide`, `packages/client/server/ide*.go`). The extension renders and reports. The UI has one small adapter, `lib/ide.ts`.

**The panel is `packages/ui`, loaded directly in the webview.** It is not in an iframe on a local server. An iframe would need another port and would swallow keystrokes meant for the editor. The cost is that assets must load from webview URIs, which a dedicated build handles (`NUXT_PUBLIC_TRANSPORT=ide`: hash routing, no app manifest) together with `app.cdnURL`, set when the HTML is rendered.

**The webview's origin is allowed by scheme.** A webview's origin is `vscode-webview://<random>`, and the extension cannot name it in advance. The CORS allowlist therefore accepts one wildcard form, `<scheme>://*`, and only for non-web schemes. `http://*` and `https://*` are refused at startup, because they would admit any web page. `vscode-webview://*` is in the default list so that a client started by hand can serve the panel. No web page can carry that origin, and the bearer token is still required.

**Finding a client, in order.**

1. An explicit `harness.agentUrl` setting.
2. A running client announced in `$HARNESS_HOME/agent-endpoint`, with its token from `agent-token`. Both are written only by a client that generated its own token, which means one started by hand. It must answer `Initialize`.
3. Start one: the `harness.clientPath` setting, then `PATH`, then `~/.harness/bin`. The token goes through the environment, never the command line. The client exits with the editor window (`HARNESS_EXIT_WITH_PARENT`).

**The bridge is Connect unary JSON over loopback.** The contract is a proto file, so it follows the same rules as every other contract. On the wire it is `POST` with JSON and a bearer token, which the extension serves with `node:http` and no dependency. The extension's TypeScript types are written by hand, because generating the whole proto tree into the package for six messages is not worth it. Golden files in `packages/client/ide/testdata/bridge` are checked from both languages, so the two sides cannot drift apart silently.

**Bridge security.**

- It binds to `127.0.0.1` only, and the client only ever dials `127.0.0.1`.
- Each extension activation gets a new random 256-bit token.
- The lock file is mode 0600 in a 0700 directory, and the client ignores one that is readable by others.
- Requests with an `Origin` header or a non-loopback `Host` are refused, so a web page cannot reach it, including through DNS rebinding.

**Finding an editor: lock files.** The extension writes `$HARNESS_HOME/ide/<pid>-<port>.json` with its port, token and workspace folders. The client picks the lock in `HARNESS_IDE_LOCK` if set, else the live editor whose folder most closely contains the session's working directory. The extension sets that variable for the client it starts and for its integrated terminals, so `harness` in a terminal reaches the right window. Stale locks (dead pid) are removed by whichever client finds them.

**Three kinds of review.** New sessions start in `auto` mode, which approves edits inside the folder without asking, so a review tied only to the permission prompt would almost never show. This was found by running it in a real editor.

- *When the edit would ask.* When a file tool would ask for permission and an editor is attached, the client also opens the native diff. The first answer wins, from either the diff or the panel, and the other side closes. Closing the diff tab answers nothing: the prompt stays in the panel.
- *When it would not ask.* The edit runs, and the client reports it with `EditApplied`, saying whether a person already answered for it. If not, the extension opens the file's change in the native diff without taking the keyboard, in a preview tab the next one replaces (setting `harness.showAppliedEdits`, default on). Accept keeps the file; Reject restores it. The agent is not held up, which is what `auto` mode was chosen for.
- *Before every edit, if asked.* With `ide_review_all_edits` (default off) edits the mode would allow wait for the editor's diff first. Closing that diff brings up the panel's prompt: a turn never waits on nothing visible, and an edit is not applied because a tab was closed. A rule or hook that denies is never overridden.

"Review session changes" lists everything changed against HEAD (or the session's snapshot outside git), shell commands included. Restoring a file asks first, because the baseline comparison can include the user's own edits, and the client refuses when the file has no baseline it can restore.

**Rejecting from the diff stops the turn**, the same as denying in the panel without a comment. A rejection with a note lets the model continue with the note.

**The right side of a proposed diff is read-only.** Editing a proposal before accepting would need a temporary file and a rule for telling the model what changed. Left out for now.

**Selection context is a mention the client expands.** The keystroke adds `@path#L10-20` to the composer draft, in the same form as every other mention. On send, the client appends those lines as a context block, taken from the editor's live buffer when one is attached, so unsaved text is what the model sees, and from disk otherwise. A plain `@path` stays as it was: text the model reads with its tools.

**Diagnostics are a tool, `EditorDiagnostics`.** It is hidden from the model when no editor is attached. It reads the editor's whole problems panel, so linters and language servers the client does not run are included.

**One proto field per preference.** `ide_diffs` (36) and `ide_review_all_edits` (37) are ordinary synced preferences: they express how a user likes to work, not anything about one device. The lock files, tokens and ports are device-local and never synced.

**No relay commands.** The bridge reaches an editor on this machine only, and a viewer of a remote session answers through the existing permission commands. Relay Command numbers 240-249 stay unused. SessionEvent 240 (`EditorReview`) tells every frontend that a diff is open in the editor.

## JetBrains: what it would take

Not built. The client side needs no change: the bridge, lock files and IdeService do not depend on the editor. A plugin would need:

| Part | Work |
|---|---|
| Client process | Port the find-or-start logic to Kotlin (about 200 lines) |
| Bridge | Serve the same six methods with the platform's built-in HTTP server or Ktor, and write the same lock file |
| Panel | Host `packages/ui` in JCEF. JCEF has no `vscode-webview` origin, so serve the assets through a custom scheme handler and add that scheme to the allowlist. Replace `acquireVsCodeApi` with a `JBCefJSQuery`, behind the `post` function the page already receives |
| Diffs | `DiffManager` with `SimpleDiffRequest`, plus Accept and Reject actions in the diff toolbar |
| Diagnostics | No public equivalent of a global problems list. `DaemonCodeAnalyzerImpl.getHighlights` per open file, or `WolfTheProblemSolver` for files with errors. Expect less coverage than VS Code unless an inspection pass is run |
| Selection, mentions | `SelectionModel`, and an action with a keymap entry |
| Packaging | Gradle IntelliJ Platform plugin; the UI build is shared and only the base URL differs |

The likely trouble spots are JCEF on Linux and remote development (Gateway), where the backend and the UI are separate processes. Estimate two to three weeks for parity with this extension.

## Known limits

- Remote development (Remote-SSH, containers) is untested. The extension declares itself a workspace extension and sets `portMapping`, so the design should work, but nothing has exercised it.
- The panel keeps the UI's own themes, following the editor's light or dark kind. It does not use the editor's colors.
- Two edits to the same file in one model response are reviewed against what is on disk, so the second may have no diff to show. It then falls back to the panel's prompt.
- `NotebookEdit` has no native diff and keeps the panel's prompt.
