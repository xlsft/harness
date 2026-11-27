# Turn snapshots

Rewind used to restore only the files the file tools (`Edit`, `Write`, `MultiEdit`, `NotebookEdit`, `DocumentEdit`)
had backed up before writing. Agents often change files another way: a Python script, a heredoc, `sed -i`, a code
generator, `git checkout`. Those edits were invisible to rewind. Turn snapshots close the gap for every working
directory, in git or not, and keep the per-file backups for what they leave out.

Code: `packages/client/session/gitcheckpoint.go` (capture, restore, refs), `session/filehistory.go` (`Restore`,
`ChangedCheckpoints`), the wait in `agent.runTool`, the wiring in `bootstrap.Open`.

## What is captured

At the start of every turn started by a prompt, the client takes the per-file checkpoint as before
(`FileHistory.MakeCheckpoint`) and, in the background, a **git checkpoint** of every repository that holds one of the
session's working directories (`Env.WorkingDirs`: the cwd, a worktree it entered, added directories). One per
repository, however many working directories are inside it.

A git checkpoint is a tree object of the working tree as `git add -A` would stage it:

- tracked files as they are on disk (staged or not), deletions included;
- untracked files that `.gitignore` does not exclude, up to 8 MiB each;
- never ignored files, `node_modules` (ignored or not), nested repositories or submodule contents. A submodule is a
  gitlink in the tree and is skipped on restore: its own repository is its history.

It is written without touching the user's index, `HEAD`, branches or stash:

1. a private index file, `~/.harness/file-history/<session>/git/<hash of the repository root>.index`, seeded once
   from the real index (`git rev-parse --git-path index`, so worktrees and submodules find their own) so the stat
   cache makes it fast. It stays between turns, so later turns only hash what changed since the last one. It lives in
   the file history rather than the scratch directory, so the Changes pane (which lists new scratch files) never
   shows it;
2. `GIT_INDEX_FILE=<that file> git add -u` for the tracked files, then the untracked files from
   `git ls-files --others --exclude-standard` under the size cap through `git update-index --add -z --stdin` (linear
   in the number of files, where pathspecs given to `git add` are matched against every file; the same clean filters
   apply);
3. `git write-tree --missing-ok` (the blobs were just written, so it skips checking each one).

All git commands run with `gc.auto=0`, `GIT_LITERAL_PATHSPECS=1`, and without the caller's `GIT_DIR`,
`GIT_WORK_TREE` or `GIT_INDEX_FILE`. None of them runs hooks.

The tree id goes into the turn's `file-history-snapshot` record (`gitTrees`, an update record written when the
capture finishes) together with the repository root. The trees are kept reachable from one private ref per session,
`refs/harness/checkpoints/<session>`, pointing at a tree whose entries are the checkpoints (`<message id>` →
checkpoint tree). A ref to a tree is ignored by `git log --all`, adds no commits to anyone's graph, needs no author
identity and writes no reflog. It is rebuilt with `git mktree` after every capture from the checkpoints the history
keeps (the last 100), so older ones become unreachable and `git gc` drops them in its own time. `git push` does not
send it (only `--mirror` would).

A working directory outside git (when it is not the home directory or the file system root, and not inside the
client's own home) gets the same capture into a private repository,
`~/.harness/file-history/<session>/git/<hash>-folder.git`, with that directory as its work tree (`GIT_DIR` +
`GIT_WORK_TREE`, so no `.git` appears in the folder). Its `info/exclude` leaves out `.git`, `node_modules` and
`.DS_Store`; the folder's own `.gitignore` files apply. Nothing runs gc there, so it needs no ref.

## Bounds

The capture starts with the turn and runs while the first model request is in flight. A tool call waits for it, so
no edit, by any tool, can happen before its turn's snapshot, but only up to the capture budget (5 s from the start of
the turn). Past it, or past the caps, that repository has no git checkpoint for the turn and the session gets a
warning notice (kind `checkpoint_skipped`); rewind then falls back to the per-file backups there.

- An untracked file larger than 8 MiB: never captured (and never deleted by a rewind, see below).
- More than 20 000 new untracked files, or more than 128 MiB of them, since the last capture (new to the private
  index, so files captured in an earlier turn do not count again).
- `node_modules`, `.git` and `.DS_Store` path parts, ignored or not, and the client's own home.
- git missing: no git checkpoint, nothing reported. `HARNESS_NO_GIT_CHECKPOINTS=1` turns the feature off.

A repository over a cap or over the budget is off for the rest of the session, with one notice, rather than costing
the budget on every turn. Another failure is reported once and retried on the next turn.

Cost, measured with `TestGitCheckpointPerformance` (`HARNESS_SNAPSHOT_PERF=20000`) on an Apple Silicon laptop under
heavy load (load average above 100, so these are upper bounds): on a 20 000-file repository the first capture took
0.3-0.6 s, a later turn with 20 changed files and one new file 0.5-0.8 s, the rewind menu's change check 0.3-0.5 s,
and a restore 0.6-1.3 s, against 0.1-0.3 s for `git status` in the same runs. A capture is about seven short git
processes (`rev-parse`, `add -u`, `ls-files`, `update-index`, `write-tree`, `mktree`, `update-ref`), each reading
the index once, so most of the cost is process start-up and index reads, and it runs while the model is answering.

## Rewind

`FileHistory.Restore(checkpoint)`:

1. waits for a capture still running;
2. for each repository with a git checkpoint at that turn: takes the current state into a tree with the same rules
   but no caps (T2), and `git diff-tree -r --no-renames T1 T2`:
   - added since (in T2 only): deleted from disk, with the directories that held only it and did not exist in T1;
   - modified, deleted or changed type: written back from T1 with `git checkout-index -f` on a second private index
     read from T1, so filters (line endings, LFS smudge), the executable bit and symlinks come back as git writes
     them;
   - gitlinks (submodules): skipped.
   A file in neither tree (ignored, or an untracked file over the cap) is not touched by this step;
3. restores the per-file backups for every file the git step does not cover: outside the repositories, in a
   repository without a checkpoint for that turn, or in neither tree (ignored files `Edit` touched).

`ChangedCheckpoints` (the rewind menu's "code changes" flag, `HasChanges` for one) compares each T1 with the current
tree id per repository, computing the current tree once for the whole list, then checks the per-file backups.

The confirmation is the same as before: the UI asks whether to restore code when the checkpoint reports code changes;
the restored and skipped counts come back in `RewindResult`.

## Lifetime

- The repositories that have a checkpoint ref are listed in `~/.harness/file-history/<session>/git/roots`, touched
  on every capture.
- Deleting a session deletes its ref in each of them, then the file history (private repositories included).
- Forking a session copies the ref (`git update-ref refs/harness/checkpoints/<new> <same tree>`), the roots file, the
  private indexes and the private repositories.
- The periodic worktree cleanup also drops `refs/harness/checkpoints/*` whose session has no roots file on this
  client, or whose roots file has not been touched within `cleanupPeriodDays`.

A rewind to a checkpoint whose tree is gone (pruned ref, another device, a re-cloned repository) falls back to the
per-file backups for that repository.

## The Changes pane

The pane reads the disk, so edits made through `Bash` already appear: `git diff` for tracked files, the untracked
listing for new files, and the folder snapshot (`folderbaseline.go`) for working directories outside git. One gap was
fixed with this change: a git-ignored file that a file tool edited inside a repository was in neither list (git
ignores it, and the history pass skipped everything inside a repository). Those files now come from the file
history, checked with one `git check-ignore --stdin` per repository. Ignored files changed only through `Bash` stay
out of the pane, as they stay out of the checkpoints.

## Not covered

- Ignored files changed only through `Bash` (build outputs, `.env` files no file tool touched).
- Files outside the working directories.
- Turns started by a notification rather than a prompt have no checkpoint of their own (as before); their changes
  are undone by rewinding to the prompt turn before them.
