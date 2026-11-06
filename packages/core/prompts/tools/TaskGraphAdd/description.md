Adds tasks to the session's task graph: work items with dependencies that this session's agents divide among themselves. The user sees the graph.

Use it when work splits into pieces that depend on each other or that several agents share, above all in a team. For your own running checklist use TodoWrite.

- `title` says what is to be done in a line; `description` adds what whoever takes it needs, since an agent sees nothing of your conversation.
- `blocked_by` lists tasks that must be done first: an id such as `t3`, or the `key` of another task in this call. A task that waits for itself, directly or not, is refused and nothing is added.
- `team` keeps a task to that team's agents and the main agent. Without it, anyone may take it.
- The call adds all the tasks or none, and returns their ids.
