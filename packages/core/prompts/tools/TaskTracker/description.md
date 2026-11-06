The user's task tracker: their own list of work across sessions and machines, shown on the Task tracker screen. Tasks stay until someone closes them, and only this user sees them.

TodoWrite is your plan for this piece of work and ends with it. Use TaskTracker for what has to outlive the session: follow-ups the user asks you to note, work you could not finish, things to pick up later. Do not copy your todo list here, and add a task the user did not ask for only when it would otherwise be lost, then tell them.

When the user asks what is left or what to do next, `list`. When you finish or start something that has a task, `update` its status: a stale tracker is worse than none.

- `action`: `list`, `get`, `create`, `update` or `delete`. Delete only when the user asks; otherwise a task ends as `done` or `cancelled`.
- `id`: the task, for `get`, `update` and `delete`.
- `title`, `description`: what the work is, enough for someone with no memory of this session to pick it up.
- `status`: `todo`, `in_progress`, `blocked`, `done` or `cancelled`. With `list`, it picks those; by default `list` shows what is still open.
- `priority`: `low`, `normal` or `high`.
- `project`: which repository or area it belongs to. With `list`, shows only that one.
- `blocked_by`: ids of tasks that must finish first. On `update` it replaces the list; an empty list clears it.
- `query`: with `list`, words in the title or description, over open and closed tasks.
