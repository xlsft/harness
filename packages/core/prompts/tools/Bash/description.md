Runs a shell command and returns its output (stdout and stderr) and exit code.

Session behaviour:
- Commands run in a persistent shell session for this conversation: the working directory carries over between calls, so a `cd` affects later commands. Environment variables, shell functions and aliases you set do NOT carry over; set them in the same command that needs them. Prefer absolute paths over `cd`.
- The shell starts from the user's profile and project environment.

Timeouts and output:
- The default timeout is 2 minutes (120000 ms). Pass `timeout` in milliseconds to change it, up to 10 minutes (600000 ms). See below for what happens when a command exceeds it.
- If the output is longer than 30000 characters, the full output is saved to a file and you receive its path plus a 2000-character preview. Read that file if you need more.

Background commands:
- Set `run_in_background` to true for long-running commands whose result you do not need right away (dev servers, watchers, long builds or test suites). The call returns immediately with a task id and the path of the file the output is written to; the command keeps running.
- Read the output file with Read whenever you want to check progress. Do not poll it in a tight loop and do not use `sleep` to wait; continue with other work instead.
- When a background command finishes, you are told in a system reminder with its task id, exit code and output file. Stop a background command you no longer need with TaskStop.
- A foreground command that reaches its timeout is not killed: it is moved to the background automatically and you get its task id and output file, as if you had set `run_in_background`.
- Do not append `&`, `nohup` or `disown` to run something in the background; use `run_in_background` instead. Each call runs in a new shell process, so shell job control (`&`, `jobs`, `kill %1`, `fg`, `wait`) does not carry over between calls, and a process started with `&` is not tracked or stopped by TaskStop.
- If the user asks you to stop a server or process when you are done, stop it before your final answer (TaskStop for background tasks) and confirm it is gone.
- Exit code 1 from `grep`, `rg`, `find`, `diff`, `test` / `[` and `git diff` means "no match" or "differences found" and is not treated as an error.

Usage rules:
- Do not use Bash to read, create, edit or search files when Read, Write, Edit, Glob or Grep can do it.
- Quote paths that contain spaces.
- Chain dependent commands with `&&`; issue independent commands as separate tool calls in the same response so they can run in parallel.
- Avoid interactive commands (editors, pagers, prompts, `-i` flags); they will hang until the timeout. Pass non-interactive flags instead.
- Give a short `description` of what the command does, in active voice (for example "Run unit tests").
- Never run destructive commands (such as `rm -rf`, `git reset --hard`, `git push --force`) unless the user asked for that outcome.
