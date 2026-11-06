# Security

## Reporting a vulnerability

Report it privately through [GitHub's vulnerability reporting](https://github.com/xlsft/harness/security/advisories/new), not in a public issue. Say what you found, how to reproduce it, and which version you ran. You should get an answer within a few days; the fix ships in the next release and the advisory is published with it.

## Supported versions

Only the latest release gets fixes.

## What counts

xlsft`s harness runs commands on your machine and holds provider keys on your server, so these matter most:

- a provider key, token or password reaching a log, an error message, a model request or a synced session;
- a way around the permission check, the Bash sandbox or a computer-use grant, including text in a file, web page or tool result that makes the agent act without approval;
- one user reaching another user's sessions, keys, files or limits on a shared server;
- anything on the server answering without authentication;
- content served from the object store or an artifact running with the app's origin;
- release archives, the app's updates or the container image that don't verify as described in the docs.

A command you approved doing what it says is not a vulnerability. The Bash sandbox has documented limits; a way around one of those limits is still worth reporting if it does more damage than they describe.
