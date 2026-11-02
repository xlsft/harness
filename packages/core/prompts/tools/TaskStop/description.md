Stops a running background task, such as a Bash command started with `run_in_background`.

- Pass the `task_id` you got when the task started.
- The task's process and its child processes are terminated. The output written so far stays in the task's output file, which you can still read.
- Use it when you no longer need a task, when it is stuck, must be restarted with different arguments, or when the user asked you to stop it. If the user asked you to stop a server when you are done, call this before your final answer.
- It only knows tasks started with `run_in_background`. A process started with `&` in a Bash command is not a task and cannot be stopped here.
- Do not stop tasks the user started unless they asked you to.
