Hands files to the user as cards in the transcript. Images, HTML, PDF, SVG and Markdown open in a side pane; anything else is offered as a download. Use it for something the user should look at or keep (a report, a chart, an export, a screenshot), not for files they can see in their editor anyway; to point at a place in the code, use Show.

Paths must name existing regular files in the session's working directories or its scratchpad, absolute or relative to the working directory. A path that is outside them, missing, a directory, larger than 50 MB or denied by a permission rule refuses the whole call, and nothing is sent.

The result says what happened. Small files can also be opened on the user's other devices; one too large for that (over 512 KB in one call) is viewable on this machine only, and the result names it.

- `files`: the paths, up to 10.
- `caption`: optional, one or two sentences on what they are looking at.
- `status`: `normal` when this answers what the user asked for; `proactive` when you send it unasked, for instance work that finished while they were away. A proactive file also notifies them, under the same limits as PushNotification.
- `display`: `auto` (the default) opens what can be rendered and offers the rest as downloads; `attachment` offers everything as downloads; `inline` asks for rendering wherever the type allows it.
