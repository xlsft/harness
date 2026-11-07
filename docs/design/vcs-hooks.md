# Version control through hooks

xlsft\`s harness isolates work in git worktrees and reads the Changes pane from git. A folder under Mercurial, Jujutsu or Perforce gets both through four hook events instead. The user writes the hooks once for their version control; the client calls them wherever it would call git. They are ordinary hooks ([hooks.md](hooks.md)): the same settings key, the same handlers, the same gates.

The code is `packages/client/vcshooks` (the contract), `hooks` (the events), `tools/places.go` (`EnterWorktree`, `ExitWorktree`), `server/worktree.go` (`CreateSession`), `subagent/isolation.go` (isolated agents) and `server/sessiondiff_vcs.go` (the Changes pane). A working example for Jujutsu is in [`docs/examples/vcs-hooks/jujutsu`](../examples/vcs-hooks/jujutsu).

## The events

All four take no matcher. Input has the common fields of every hook (`session_id`, `transcript_path`, `cwd`, `permission_mode`, `hook_event_name`, and `agent_id` for an agent) plus the fields below. A command handler gets it on stdin and runs in `cwd`; an http handler gets it as the POST body.

Output may be plain text where the table says so, or JSON. JSON fields go under `hookSpecificOutput` with `hookEventName` set to the event, like every event-specific answer; the same fields at the top level are read too, for short scripts. Exit code 2 is a failure here like any other code but 0: none of these events can be refused, only fail.

| Event | Input adds | Output |
|---|---|---|
| `WorktreeCreate` | `name` (one path segment: letters, digits, `.`, `_`, `-`), `source_dir` (the folder to copy, also `cwd`), `worktree_kind` (`session` or `agent`), `base_revision` when the user or model named one | The path of the new working copy: plain text (the last line counts, so chatter before it is fine), or JSON `worktreePath`, with optional `baseRevision` (a label for what it started from, shown as the base) and `cwd` (the folder in the copy to work in; default the copy's root). Nothing at all declines (below) |
| `WorktreeRemove` | `worktree_path`, `name`, `source_dir`, `worktree_kind`, `force` | Nothing. `force: true` means the user or model agreed to lose what the copy holds; without it, a hook that would lose work should fail, with the reason on stderr |
| `VcsStatus` | (`cwd` is the folder asked about) | JSON: `root` (absolute; default `cwd`), `base` (a label: `@-`, `tip`, `have`), `files`: `[{path, status, oldPath, additions, deletions, binary}]`. `path` is relative to `root` (or absolute inside it); `status` is `added`, `modified`, `deleted` or `renamed` (or `A`, `M`, `D`, `R`). Counts are optional. Nothing at all: not this hook's folder |
| `VcsDiff` | `paths` (relative to `root`; none means every change) | The unified diff, git style (`diff --git a/x b/x`, `---`/`+++`, `@@` hunks): plain text, or JSON `diff` |

Defaults: `VcsStatus` and `VcsDiff` time out after 30 seconds unless the handler sets `timeout`; `VcsDiff` may print up to 64 MiB, the others 1 MiB. Output past that is a failure for these events, not a truncated answer.

## Where they replace git

When a `WorktreeCreate` hook runs for the session, it makes every isolated copy:

- **A new session with worktree options** (`CreateSession`). The hooks come from the folder's settings before the session exists, under the trust that session would have. `name` and `base_ref` pass through as `name` and `base_revision`; a branch or a pull request needs git and is refused, unless the folder is in git and the hook declines.
- **`EnterWorktree`**. `name` and `base_ref` as above; `pull_request` is refused the same way.
- **Agents with `isolation: worktree`** (`worktree_kind: agent`, `name: agent-<id>`, `agent_id`).

Best-of-N does not exist in the client yet; when it does, it should go through the same `vcshooks.Hooks.Create`.

`WorktreeRemove` takes the copy away where git would remove a worktree: `ExitWorktree` with `remove`, a session or agent that failed to start (with `force`), and an isolated agent's copy when the session closes and `VcsStatus` reports it has no changes (without `force`; without `VcsStatus` the copy stays, since nothing can tell). Without a `WorktreeRemove` hook a copy stays where it is, and the tool says so.

A session in such a copy records it as a worktree with `hook: true`, no branch, the hook's `baseRevision` as its base ref and the source folder as its repository path, so it resumes there like any worktree session. `SyncWithBase` refuses it (that is the version control's own job, through `Bash`), and the daily cleanup of stale worktrees does not touch it.

When `VcsStatus` hooks run, every working directory outside git asks them first (except under the COMMIT base, where folders outside git never take part). The hook that answers decides the list: its files against its base, counted from its numbers or from a `VcsDiff` of every listed file when it gave none, and hunks from `VcsDiff` for the files small enough to show. The file tools' checkpoints and the folder snapshots then stay out of that folder. A folder no hook claims keeps the snapshot rules. A failing hook fails the measurement, which the pane shows: an empty list would read as "nothing changed".

`ExitWorktree` uses `VcsStatus` for what git's status gave: a copy with listed changes needs `discard_changes: true` to be removed, and the user is asked for that whatever the mode. Without `VcsStatus` the client cannot tell, so removing passes `force` only when the model said `discard_changes`, and a careful hook refuses otherwise.

## Decisions

- **Four events, not one `Vcs` event with an operation field.** Each has its own input, output and timeout, and a user can configure only some (isolation without the Changes pane, or the reverse). A matcher on the operation would have been the only gain, and the four names are clearer in Settings > Hooks and the transcript.
- **Plain output where it is natural.** A path and a diff are what a `jj workspace add` script and `hg diff --git` print anyway. JSON is there for the optional fields.
- **Empty output declines.** One user-level script can serve every folder: in a git repository it prints nothing and git keeps its worktrees; outside its version control it prints nothing and the snapshot rules stay. A hook that fails, by contrast, stops creation with its message.
- **A failure stops; starting in place is the user's choice.** `CreateSession` returns the hook's message (`FAILED_PRECONDITION`); the new-session form shows it and offers to start in the folder itself, which only a click does. `EnterWorktree` and the `Agent` tool return it to the model with the instruction to ask the user before working in place.
- **Git keeps git folders, except for isolation.** `VcsStatus` is asked only about folders outside git: git's own diff is richer (bases, branches, commits). `WorktreeCreate` replaces git worktrees everywhere it runs, as the task asks, so a colocated Jujutsu repository gets jj workspaces; a script that wants git there declines.
- **The gates are the hooks' gates.** These events come from the same set as every hook: a project's hooks need the folder trusted, what the server delivered needs this device's trust, a policy with `allowManagedHooksOnly` leaves only its own, `disableAllHooks` and `HARNESS_NO_HOOKS=1` turn them off (and git comes back). Skill and agent frontmatter hooks never replace git. Before a session exists, when the server's documents cannot be read, the policy cannot be checked either, so creating through hooks is refused rather than run unchecked.
- **A hook's copy has its source's trust.** The copy sits wherever the hook put it, usually outside the trusted folder, and holds the same project. Without this the session in it would lose the project's own `VcsStatus` and `WorktreeRemove`. Only a copy recorded as made by a hook, of a folder trusted on this device, gets it.
- **Quiet while polling.** `VcsStatus` and `VcsDiff` run after tools and while the pane polls. A run that answered does not show in the transcript; a failure does.
- **Rewind needs nothing from the version control.** Code rewinds restore the file checkpoints the file tools keep, in any folder, so nothing changed there. In a hook's copy the checkpoints work as in any folder outside git.
- **A one-time notice.** A session opening in a folder whose nearest marker is `.hg`, `.jj` or a Perforce config file (the name in `P4CONFIG`, else `.p4config`), with no `.git` nearer, and with neither `WorktreeCreate` nor `VcsStatus` running, publishes `SessionEvent.vcs_hooks_suggested` (field 410) once per working copy on this device (`HARNESS_HOME/vcs-notices.json`, never synced: it lists folders of this machine). It is not stored in the transcript. The UI shows it with a pointer to this page.
- **Proto.** `SessionEvent` 410 is used; 411-419 are free. `FileDiffSource.FILE_DIFF_SOURCE_VCS` marks files a hook listed. `UserPreferences` 66-67 are unused: nothing here is a preference.

## Jujutsu

[`jj-hooks.py`](../examples/vcs-hooks/jujutsu/jj-hooks.py) handles all four events (Python 3 and `jj`, nothing else). Copy it to `~/.harness/hooks/` and the block of [`settings.json`](../examples/vcs-hooks/jujutsu/settings.json) into `~/.harness/settings.json`, or into a repository's `.harness/settings.json` with the script path changed.

- `WorktreeCreate` runs `jj workspace add --name <name> [--revision <base>]` into `<repo>-workspaces/<name>` (or `$HARNESS_JJ_WORKSPACES/<name>`), keeps the session in the same subfolder, and labels the base with the short change id of the workspace's `@-`.
- `WorktreeRemove` snapshots the workspace (`jj status`), forgets it and deletes the folder. The workspace's last commit stays in the repository, so `force` changes nothing.
- `VcsStatus` reads `jj diff --git -r @` for the files and counts against `@-`; `VcsDiff` runs the same for the paths asked (`root-file:` filesets).
- Outside a Jujutsu repository it prints nothing, so it can be configured for every folder.

The client's tests run it with a stand-in `jj` always, and against the real `jj` when it is on `PATH` (never installed for them).

Mercurial and Perforce have no example yet. For Mercurial, `hg share` makes the copy and `hg diff --git` the diff; Perforce needs a client workspace per copy (`p4 client`, `p4 sync`) and `p4 diff -du` turned into git-style headers.
