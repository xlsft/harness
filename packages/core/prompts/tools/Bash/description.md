Runs a shell command and returns its output (stdout and stderr) and exit code.

How the shell behaves:
- Every call starts a new shell process from the user's profile. The working directory carries over to the next call (a `cd` sticks), as long as it stays inside the working directories; otherwise it is reset. Environment variables, functions and aliases do not carry over: set them in the same command that needs them. Prefer absolute paths to `cd`.
- Because each call is a new process, shell job control does not work across calls: `jobs`, `kill %1`, `fg` and `wait` see nothing from earlier calls, and a process started with `&`, `nohup` or `disown` is not tracked and TaskStop cannot stop it. Never background a command that way; use `run_in_background`.

Timeouts and output:
- The default timeout is 2 minutes (120000 ms). Set `timeout` in milliseconds, up to 10 minutes (600000 ms).
- A foreground command that reaches its timeout is not killed: it moves to the background and you get its task id and output file, as with `run_in_background`.
- Output longer than 30000 characters is saved to a file; you get the path and a preview. Read the file if you need the rest.
- Exit code 1 from `grep`, `rg`, `find`, `diff`, `test` / `[` and `git diff` means "no match" or "differences found", not failure.

Background commands:
- Set `run_in_background` to true for servers, watchers and long builds or test runs whose result you do not need right away. The call returns at once with a task id and an output file path; the command keeps running.
- You get a system reminder with the exit code when it finishes. Do not poll the output file or `sleep` while waiting; do other work, or end your turn. Read the output file when you need to check on it.
- Stop a background task with TaskStop. If the user asked you to stop a server or process when you are done, stop it before your final answer and confirm it is gone (for example, the port no longer answers).

Rules:
- Do not use Bash to read, write, edit or search files when Read, Write, Edit, Glob or Grep can do it, and do not use `echo` to talk to the user.
- Quote paths that contain spaces with double quotes.
- Join dependent commands with `&&`. Send independent commands as separate calls in the same response so they run in parallel.
- Before creating files or directories, make sure the parent directory exists and is the right place.
- Do not run interactive commands (editors, pagers, prompts, `git rebase -i`, `git add -i`); they hang until the timeout. Use non-interactive flags.
- Do not `sleep` between commands that can run right away. If you must wait on an external system, use a command that reports its status instead of sleeping first.
- `description`: say what the command does in active voice, in about 5 to 10 words ("Run unit tests", "List files changed on this branch"). For pipelines or unusual flags, say what the whole thing achieves.

Git safety:
- Never change git config.
- Never run destructive git commands (`push --force`, `reset --hard`, `checkout .`, `restore .`, `clean -f`, `branch -D`) unless the user asked for that. Look for a safer way first.
- Never force-push to main or master; warn the user if they ask for it.
- Never skip hooks or signing (`--no-verify`, `--no-gpg-sign`) unless the user asked. If a hook fails, fix the cause.
- Prefer a new commit to `--amend`. When a pre-commit hook fails, the commit did not happen, so amending would change the previous commit: fix the problem, stage again and make a new commit.
- Never push unless the user asked.

Making a commit (only when the user asks):
1. In parallel, run `git status` (without `-uall`), `git diff` for staged and unstaged changes, and `git log` for recent messages.
2. Draft a message of one or two sentences that says why the change was made, in the style of the repository. Do not commit files that look like secrets (`.env`, credential files); warn the user if they ask you to.
3. Stage the relevant files by name rather than `git add -A` or `git add .`. Pass the message through a heredoc so formatting survives, ending it with the attribution lines if a reminder gave you any:
   git commit -m "$(cat <<'EOF'
   Short reason for the change.
   EOF
   )"
4. Run `git status` afterwards to confirm. If there is nothing to commit, do not create an empty commit.

Opening a pull request (only when the user asks):
1. Look at the whole branch, not only the last commit: `git status`, `git diff`, whether the branch tracks a remote and is pushed, and `git log` plus `git diff <base>...HEAD`.
2. Write a title under 70 characters and a body with a short summary and a test plan.
3. Create a branch if needed, push with `-u` if needed, then run `gh pr create` with the body in a heredoc, ending with the attribution lines if a reminder gave you any.
4. Reply with the pull request URL.
Use `gh` for GitHub work (issues, pull requests, checks, releases). In anything you post, write `@name` only for people who should be notified.
