Publishes a page or a file to the user's Artifacts screen, where it stays after this session is gone. It is stored on the server, private to the user until they choose to share it with others on the server.

Use it for finished work someone will come back to or pass on: a report, a dashboard, a generated document, an interactive page. To show the user a file right now, use SendUserFile; for a chart, diagram or small tool that's part of the conversation, use Visual, which follows the app's look.

- Pass either `file_path`, a file in the session's working directories or scratchpad, or `content` with `name`, a file name whose extension says what it is (`report.html`, `notes.md`).
- `title` is what the screen lists. It is required for a new artifact.
- `artifact_id`, from an earlier result, publishes a new version of that artifact instead of a new one. Earlier versions stay readable. Update rather than republishing under a new title.
- HTML opens as a page. It runs sandboxed, with its own opaque origin and no network: everything it needs must be inside the one file, with inline scripts and styles and images as data URLs. It cannot load CDNs, call APIs, or submit forms. Markdown, text, images and PDFs open in a viewer; anything else is a download.

Design a page for what it is about and who will see it, not for this app. It doesn't receive the app's theme and shouldn't imitate it: a page selling cucumbers looks like a farm's shop, a report for a team looks plain and readable, and neither looks like a developer tool. Choose colors, type, imagery and layout to suit, and follow any look the user asks for. Draw what images you need as inline SVG or CSS.

Do not publish anything the user would not want on the server, such as secrets, without asking.
