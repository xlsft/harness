Waits until something happens, then returns what happened and how long it took. Use it instead of `sleep` or retry loops in Bash. It only observes and changes nothing.

Set exactly one target:
- `task_id`: a background task finishes. With `pattern`, it waits until a line of the task's output matches the regular expression instead; lines printed before the call count too. This does not move the TaskOutput read position.
- `port`: a TCP port accepts connections, for example a dev server you started in the background. `host` defaults to this machine (localhost); a remote host may need the user's permission.
- `path`: a file exists. With `pattern`, it waits until a line of the file matches. With `changed: true`, it waits until the file is created, modified or deleted after the call starts (combined with `pattern`, until it changes and then matches).

`timeout` (milliseconds) is required, at most 600000. Pick a realistic limit: a few seconds for a port that should open soon, minutes for a long build. Running out of time is not an error; the result says the condition was not met, so check the task output or the logs to find out why before waiting again.
