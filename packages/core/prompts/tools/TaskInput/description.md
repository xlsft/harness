Writes to the standard input of a background command that was started with `stdin: true` on Bash, for programs that wait for input: a REPL, a prompt, a tool reading commands.

Send a whole line with its newline, then read what it answered with TaskOutput. `close: true` closes the input, which ends programs reading until end of input; it can go with `input` or alone. What you write is run by whatever is reading it, so it is checked like a command. For a full terminal application use TerminalRun instead.
