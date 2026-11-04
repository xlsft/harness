Watches the output of a background task and tells you when a line matches a regular expression, so you can get on with other work or end your turn instead of polling. The call returns at once with a task id; each notification arrives later as a system reminder and starts a turn if the session is idle.

- Pass `task_id` to watch a task that is already running, or `command` to start one. A `command` belongs to its monitor: it is stopped when the monitor ends, by a first match, by its timeout or for being too noisy. To keep a server running after it says it is ready, start it with Bash `run_in_background` and watch its task id.
- `pattern` is matched line by line. Matching starts at output you have not read yet with TaskOutput, so a line printed just before the call still counts.
- By default the monitor ends at the first match. Set `persistent` to hear about every match: bursts are batched into one notification, and a monitor that matches too often stops itself and says so. Make the pattern specific enough that each notification is worth a turn, and match failure lines too, not only the success you hope for.
- You also hear when the watched task ends, as for any background task, after its last matches. Watching a task does not change how it runs: stopping its monitor leaves it running.
- `timeout` is how long to watch, in milliseconds (default one hour, at most 24 hours). When it elapses you are told.
- End a monitor early with TaskStop and its task id. Read everything it matched with TaskOutput. Monitors are listed with the session's background tasks.
- To block until something happens within this turn, use Wait instead. Starting a command through Monitor is permission-checked like the same command in Bash.
