Runs a shell command and returns its output (stdout and stderr) and exit code.

Session behaviour:
- Commands run in a persistent shell session for this conversation: the working directory carries over between calls, so a `cd` affects later commands. Environment variables, shell functions and aliases you set do NOT carry over; set them in the same command that needs them. Prefer absolute paths over `cd`.
- The shell starts from the user's profile and project environment.

Timeouts and output:
- The default timeout is 2 minutes (120000 ms). Pass `timeout` in milliseconds to change it, up to 10 minutes (600000 ms). Commands that exceed it are killed.
- If the output is longer than 30000 characters, the full output is saved to a file and you receive its path plus a 2000-character preview. Read that file if you need more.
- Exit code 1 from `grep`, `rg`, `find`, `diff`, `test` / `[` and `git diff` means "no match" or "differences found" and is not treated as an error.

Usage rules:
- Do not use Bash to read, create, edit or search files when Read, Write, Edit, Glob or Grep can do it.
- Quote paths that contain spaces.
- Chain dependent commands with `&&`; issue independent commands as separate tool calls in the same response so they can run in parallel.
- Avoid interactive commands (editors, pagers, prompts, `-i` flags); they will hang until the timeout. Pass non-interactive flags instead.
- Give a short `description` of what the command does, in active voice (for example "Run unit tests").
- Never run destructive commands (such as `rm -rf`, `git reset --hard`, `git push --force`) unless the user asked for that outcome.
