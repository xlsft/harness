Runs a PowerShell command on this Windows machine and returns its output (stdout and stderr) and exit code.

How it behaves:
- Every call starts a new PowerShell process, without the user's profile. The working directory carries over to the next call (a `Set-Location` sticks), as long as it stays inside the working directories; variables, functions and modules do not.
- The exit code is that of the last native program, or 1 when PowerShell itself reported a terminating error. Errors that are not terminating print and go on: add `-ErrorAction Stop` where a failure should stop the command.
- Each call asks the user unless a permission rule names `PowerShell`. Rules for Bash do not apply here, so do not suggest them.

Timeouts and output:
- The default timeout is 2 minutes (120000 ms). Set `timeout` in milliseconds, up to 10 minutes (600000 ms).
- A foreground command that reaches its timeout is not killed: it moves to the background and you get its task id and output file, as with `run_in_background`.
- Output longer than 30000 characters is saved to a file; you get the path and a preview. Read the file if you need the rest.

Background commands:
- Set `run_in_background` to true for servers, watchers and long builds or test runs whose result you do not need right away. The call returns at once with a task id and an output file path; the command keeps running.
- You get a system reminder with the exit status and the last lines of output when it finishes. Read new output with TaskOutput and stop the command with TaskStop.

Rules:
- Do not use PowerShell to read, write, edit or search files when Read, Write, Edit, Glob or Grep can do it, and do not use `Write-Output` to talk to the user.
- Quote paths containing spaces. Join dependent commands so that a failure stops the next: `cmd1; if ($?) { cmd2 }` works in every version, `&&` only in PowerShell 7.
- Do not run interactive commands (`Read-Host`, editors, pagers, `Get-Credential`); they fail or hang until the timeout.
