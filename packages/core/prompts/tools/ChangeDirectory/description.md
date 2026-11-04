Moves this session to another folder it may already work in: a working directory or a folder inside one. Your working directory and your shell follow from the next tool call on, the environment section and project instructions become those of the new folder, and the folder you left stays allowed, so you can move back.

Use it when the work has moved to another project in the session's working directories. To look at or edit a single file elsewhere in them you do not need to move: use its absolute path. A plain `cd` in Bash stays within the session and does not change this.

A folder outside the working directories is refused: ask for it with RequestDirectory. You cannot give yourself access any other way. A session in a worktree cannot move out of it with this tool: use ExitWorktree.

Do not call it together with other tools in one message: relative paths in the same batch could resolve either way.

- `path`: the folder, absolute or relative to the working directory.
