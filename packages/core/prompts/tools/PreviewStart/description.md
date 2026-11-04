Runs the project's dev server and brings it up in the Browser pane, where you and the user see the same page and the Browser* tools can act on it. Prefer it to starting a dev server with Bash whenever you want to look at the result.

Give exactly one of:
- `name`: a server from `.harness/launch.json` in the working directory. The client starts it, waits until its port answers, then opens the page. If that server already runs in this session, it is reused, so calling this twice is safe.
- `url`: just open that page in the browser; nothing is started. A site other than this machine may need the user's approval.

If the launch file is missing or has no such server, the error shows the format. Look at how the project runs (package.json scripts, Makefile, README), write the file with Write, then call this again:

```json
{
  "servers": [
    {"name": "web", "command": "pnpm dev", "args": ["--port", "5173"], "port": 5173}
  ]
}
```

`command` is a shell command line, run from the working directory; `args` are optional and appended with quoting; `port` is what the server really listens on, so pin it in the command when the tool would otherwise pick another one; `url` is optional, the page to open on this machine (default `http://localhost:<port>`). Writing to `.harness` needs the user's approval.

Starting a server goes through the same permission check as running its command line with Bash.

The result gives a `server_id` for PreviewStop and PreviewLogs. If the server exits or does not answer in time, the error includes its last output; a server that is merely slow keeps running, and calling this again waits further. If the port is taken by something else, nothing is started and nothing is stopped: tell the user or choose another port. The command must stay in the foreground: when it exits, the server counts as ended, and whatever it left running is stopped.

Servers keep running across turns and when the session is resumed; they end when you stop them, the session is deleted or the client exits.
