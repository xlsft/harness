Changes a task of the graph. Set `status` to `done`, `failed` or `cancelled` when you are through with it, or to `pending` to give it back, and say what became of it in `note`: others read it to go on from there. Only the agent holding a task, or the main agent, can change it while it's held. A task waiting for one that failed stays waiting until someone decides: cancel it or change what it waits for. TaskGraphClaim takes a task.

`add_blocked_by` adds dependencies; `title` and `description` rewrite it.
