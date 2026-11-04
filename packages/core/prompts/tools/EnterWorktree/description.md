Creates a git worktree of the current repository, on a new branch, and moves this session into it. From the next tool call on, your working directory and your shell are in the worktree; the folder you left is no longer a working directory until you come back.

Use it when the work should stay apart from the checkout you are in: a risky change, an experiment, or work the user wants on its own branch while the checkout stays as it is. Do not use it for an ordinary edit.

A worktree starts from a commit, so uncommitted changes in the current checkout do not come along; the result tells you when some stayed behind. Files in the worktree are new paths to the file tools: read them before editing them there.

- `name`: optional name of the worktree folder and the end of its branch `harness/<name>`. Short, lowercase, hyphens. Left out, the client picks one.
- `base_ref`: optional branch or commit to start from. Left out, the branch or commit the checkout is on.

Fails outside a git repository and when the session already runs in a worktree.
