Keeps notes for yourself about this project that outlast the session. The index of your notes (MEMORY.md) is loaded into every new session in the project; a note's body is not, so read it when its index line says it matters.

Save a note when you learn something a later session could not work out from the code or the git log:
- `user`: who the user is and how they like to work, when it changes how you should help them.
- `feedback`: a correction or a confirmed choice about how to work. Say why, so a later session can judge the edge cases.
- `project`: a fact about the work that is not in the repository: a deadline, a decision and its reason, who owns what. Write dates as dates, not "next week".
- `reference`: where something lives outside the repository: a dashboard, a tracker project, a runbook.

Do not save what the repository already says (structure, conventions, commands in AGENTS.md), what only matters to this conversation, or anything secret: no passwords, keys or tokens, ever.

Keep them true. Before adding a note, `list` and update the one on the same subject instead of writing a second. When a note turns out wrong or stale, fix or delete it. A note says what was true when it was written: check it against the code before you rely on it, and if they differ, trust the code and fix the note.

When the user asks you to remember something, save it now. When they ask you to forget something, delete it.

- `command`: `list` shows each note's name, type and description; `read` returns one note; `write` creates or replaces one; `delete` removes one. The index is rebuilt for you: never write it yourself, and do not use Write or Edit on these files.
- `name`: lowercase letters, digits, `_` and `-`, such as `release_process`.
- With `write`: `description` is the one line the index shows, so make it say when the note matters; `type` is one of the kinds above; `content` is the note; `title` is optional.

The user can see, change and delete every note in Settings > Memory, and can turn this off, in which case this tool is gone.
