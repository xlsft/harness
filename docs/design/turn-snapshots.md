# Turn snapshots

Rewind used to restore only the files the file tools (`Edit`, `Write`, `MultiEdit`, `NotebookEdit`, `DocumentEdit`)
had backed up before writing. Agents often change files another way: a Python script, a heredoc, `sed -i`, a code
generator, `git checkout`. Those edits were invisible to rewind. Turn snapshots close the gap for folders in git and
keep the per-file backups for everything else.

## What is captured

At the start of every turn started by a prompt, the client takes the per-file checkpoint as before
(`FileHistory.MakeCheckpoint`) and, in the background, a **git checkpoint** of every repository that holds one of the
session's working directories (`Env.WorkingDirs`: the cwd, a worktree it entered, added directories). One per
repository, however many working directories are inside it.

A git checkpoint is a tree object of the working tree as `git add -A` would stage it:

- tracked files as they are on disk (staged or not), deletions included;
- untracked files that `.gitignore` does not exclude, up to 8 MiB each;
- never ignored files (`node_modules`, build outputs), nested repositories or submodule contents. A submodule is a
  gitlink in the tree and is skipped on restore.

It is written without touching the user's index, `HEAD`, branches or stash:

1. a private index file, `~/.harness/file-history/<session>/git/<hash of the repository root>.index`, seeded once
   from the real index (`git rev-parse --git-path index`, so worktrees and submodules find their own) so the stat
   cache makes it fast. It stays between turns, so later turns only hash what changed since the last one. It lives in
   the file history, not the scratch directory, so the Changes pane never lists it;
2. `GIT_INDEX_FILE=<that file> git add -u` for the tracked files, then the untracked files from
   `git ls-files --others --exclude-standard` under the size cap through `--pathspec-from-file`;
3. `git write-tree`.

All git commands run with `gc.auto=0` and `core.hooksPath` left alone (none of these commands runs hooks).

The tree id goes into the turn's `file-history-snapshot` record (`gitTrees`, an update record written when the
capture finishes) together with the repository root. The trees are kept reachable from one private ref per session,
`refs/harness/checkpoints/<session>`, pointing at a tree whose entries are the checkpoints (`<message id>` →
checkpoint tree). A ref to a tree is ignored by `git log --all`, adds no commits to anyone's graph, needs no author
identity and writes no reflog. It is rebuilt with `git mktree` after every capture from the checkpoints the history
keeps (the last 100), so older ones become unreachable and `git gc` drops them in its own time.

## Bounds

The capture starts with the turn and runs while the first model request is in flight. A tool call waits for it, so
an edit can never happen before its turn's snapshot, but only up to the capture budget (5 s from the start of the
turn). Past it, or past the caps, that repository has no git checkpoint for the turn and the session gets a warning
notice ("Turn checkpoint skipped ..."); rewind then falls back to the per-file backups there.

- Untracked file larger than 8 MiB: never captured (and never deleted by a rewind, see below).
- More than 20 000 new untracked files, or more than 128 MiB of them since the last capture: the capture is skipped
  for that turn.
- git missing, or the working directory not in a repository: no git checkpoint, nothing reported.

Measured on an Apple Silicon laptop (see `TestGitCheckpointPerformance`, `HARNESS_SNAPSHOT_PERF=1` for the larger
run): the first capture of a 20 000-file repository costs one `git add` over a warm stat cache; a later turn with a
few changed files costs about as much as `git status`.

## Rewind

`FileHistory.Restore(checkpoint)`:

1. waits for a capture still running;
2. for each repository with a git checkpoint at that turn: takes the current state into a tree with the same rules
   (T2), and `git diff-tree -r --no-renames T1 T2`:
   - added since (in T2 only): deleted from disk, with the directories that held only it and did not exist in T1;
   - modified, deleted or changed type: written back from T1 with `git checkout-index -f` on a second private index
     read from T1, so filters (line endings, LFS smudge), the executable bit and symlinks come back as git writes
     them;
   - gitlinks (submodules): skipped.
   A file in neither tree (ignored, or an untracked file over the cap) is not touched by this step;
3. restores the per-file backups for every file the git step does not cover: outside the repositories, in a
   repository without a checkpoint for that turn, or in neither tree (ignored files `Edit` touched).

`HasChanges` (the rewind menu's "code changes" flag) compares T1 with the current tree id per repository, computing
the current tree once for the whole list.

The confirmation is the same as before: the UI already asks whether to restore code when the checkpoint reports code
changes; the restored and skipped counts come back in `RewindResult`.

## Lifetime

- The roots that have a checkpoint ref are listed in `~/.harness/file-history/<session>/git/roots`.
- Deleting a session deletes its ref in each of them, then the file history as before.
- Forking a session copies the ref (`git update-ref refs/harness/checkpoints/<new> <same tree>`) and the roots file.
- The periodic worktree cleanup also drops `refs/harness/checkpoints/*` whose session has no roots file on this
  client, or whose roots file has not been touched within `cleanupPeriodDays`.

A rewind to a checkpoint whose tree is gone (pruned ref, another device, a re-cloned repository) falls back to the
per-file backups for that repository.

## The Changes pane

The pane reads the disk, so edits made through `Bash` already appear: `git diff` for tracked files, the untracked
listing for new files, and the folder snapshot (`folderbaseline.go`) for working directories outside git. One gap was
fixed with this change: a git-ignored file that a file tool edited inside a repository was in neither list (git
ignores it, and the history pass skipped everything inside a repository). Those files now come from the file
history. Ignored files changed only through `Bash` stay out of the pane, as they stay out of the checkpoints.
