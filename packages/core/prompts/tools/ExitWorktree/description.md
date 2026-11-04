Moves this session out of its worktree, back to the folder it entered from, keeping the worktree or removing it. Your working directory and your shell follow from the next tool call on.

- `action: "keep"`: the worktree and its branch stay on disk, for the user or for later.
- `action: "remove"`: the worktree folder is deleted. Its branch is deleted only when the client named it and it has no commits of its own; a branch with commits stays, and the result says so. Committed work is therefore never lost.

Removing refuses when the worktree has uncommitted changes, when background tasks of this session are running, or when another session works there. Commit the changes first, or choose keep. Set `discard_changes: true` only when the user told you to throw those changes away; the user is asked to confirm it.

Fails when the session does not run in a worktree.
