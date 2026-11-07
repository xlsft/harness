Creates a git worktree of the current repository, on a new branch, and moves this session into it. From the next tool call on, your working directory and your shell are in the worktree; the folder you left is no longer a working directory until you come back.

Use it when the work should stay apart from the checkout you are in: a risky change, an experiment, or work the user wants on its own branch while the checkout stays as it is. Do not use it for an ordinary edit.

A worktree starts from a commit, so uncommitted changes in the current checkout do not come along; the result tells you when some stayed behind. Files in the worktree are new paths to the file tools: read them before editing them there.

- `name`: optional name of the worktree folder and the end of its branch `harness/<name>`. Short, lowercase, hyphens. Left out, the client picks one.
- `base_ref`: optional branch or commit to start from. Left out, the branch or commit the checkout is on.
- `pull_request`: optional number or URL of a pull or merge request to start from instead of `base_ref`. It is fetched from the repository's remote onto a new branch; pushing that branch does not update the pull request.

Git-ignored files named by a `.worktreeinclude` at the repository root are copied in; the result says how many.

When the project configures WorktreeCreate hooks (for Mercurial, Jujutsu or Perforce), a hook makes the copy instead: there is no branch, `base_ref` is passed to it as the revision to start from, and `pull_request` is refused. If the hook fails, the session stays where it was; ask the user before doing the work there instead.

Fails outside a git repository (unless such a hook makes the copy) and when the session already runs in a worktree.
