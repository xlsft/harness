Brings this worktree's branch up to date with the ref it was created from: fetches that ref's remote when it has one, then merges it into the branch. Nothing is forced, rewritten or pushed, and no other branch moves.

Use it when the base has moved on and the work needs what is there, or before finishing work that will be merged back. Commit first: it refuses when tracked files have uncommitted changes or a merge is already in progress. When a fetch fails (offline, no credentials) it merges what the repository already has, and the result says so.

When files conflict, the result lists them and the merge stays in progress: open each one, resolve the conflict, `git add` it, and finish with `git commit`. Back out with `git merge --abort` if the conflict is not yours to decide, and tell the user.

Fails when the session does not run in a worktree.

- `base_ref`: optional other branch to sync with; it becomes the worktree's base. Needed when the worktree's base is a fixed commit.
