Opens something in the user's interface so they can look at it next to the conversation. It only opens a view: it changes nothing and needs no approval.

Targets:
- `file`: a file in the project, in the Files pane. Pass `path`, and `line` (with `end_line` for a range) to scroll to and highlight those lines.
- `diff`: the changes made in this session, in the Diff pane. Pass `path` to jump to one file's changes, or leave it out for the whole diff.
- `url`: a web page in the Browser pane, for example the dev server you started (`http://localhost:5173/settings`). Only http and https URLs are accepted.

When it helps:
- you are pointing the user at a specific place in the code: the function you are asking about, the line that causes the bug, the config they need to edit;
- you finished a change and want the user to review it: show the diff, or the one file that matters most;
- you started or changed a running app and the user should see the page.

When not to use it:
- instead of explaining. Show the place and still say in your reply what is there and why it matters; the user may not be looking at the pane.
- for every file you read or edit. Show one target when it serves the user, not as a log of your work.
- to read anything yourself: the result does not contain the file or page. Use Read or WebFetch for that.

The result says whether the target was opened. When no interface is attached (a terminal or headless run), nothing is shown; mention the file, lines or URL in your reply instead.
