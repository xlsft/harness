Stops a running background task, such as a shell command started with `run_in_background`.

- Pass the `task_id` that was returned when the task was started.
- The task's process and its child processes are terminated. Output produced so far stays in the task's output file and can still be read with Read.
- Use it when a background task is no longer needed, is stuck, or has to be restarted with different arguments. Do not stop tasks the user started unless they asked you to.
