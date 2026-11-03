Returns the output a background task has written since you last read it, plus its status: running, exited with a code, or killed. Works for any task id you got from Bash (`run_in_background`, or a command that was moved to the background).

- Each call continues where the previous TaskOutput call for that task stopped, so you only see new lines. Pass `offset` (a byte position from an earlier result) to read from somewhere else; `offset: 0` reads from the start.
- Without `block` or `pattern` the call returns at once with whatever is there.
- `block: true` waits until the task finishes. `pattern` waits until a new output line matches the regular expression (for example `Listening on|ERROR`); only lines after the read position are checked. Either wait also ends when the task finishes or `timeout` passes.
- `timeout` is in milliseconds: default 30000, at most 600000. A timeout is not an error: the result says the task is still running and shows the output so far.
- Output longer than 30000 characters keeps its end; the result says how many earlier bytes were left out and where the full output file is.
- Use it instead of reading the output file with `cat` or `tail`, and instead of `sleep` loops. When you only need to know that a server is up, Wait on its port is often simpler.
