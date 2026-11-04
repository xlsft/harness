Starts another session of the user's, with its own transcript, for a task that deserves one. The new session has a short name, is listed under this one, and runs with this session's permission mode and rules.

Pick `initiation` by who wants the session:
- `user_asked`: the user asked for a separate session. It starts now.
- `offer`: you think the current request would be better off in its own session. The user gets a card with "New session" and "Do it here", and the call waits for the answer.
- `own_initiative`: you noticed something the user did not ask about. It leaves a card they may start later, and the call returns a proposal id at once. Do not wait for it. It expires after a week; withdraw it with SessionDismiss if it stops making sense.

Pick `context` by what the session needs to know:
- `fresh`: starts with nothing but `prompt`. Write the prompt so that it stands alone: the goal, the paths and facts it needs, and what done looks like.
- `fork`: a copy of this conversation so far, with `prompt` as the next message. Use it when the work depends on what was said here.

Set `worktree` when it will edit files while this session or another also works in the same checkout.

After it starts, do not do its task here and do not poll it: you get a note with its latest report when it ends a turn.
