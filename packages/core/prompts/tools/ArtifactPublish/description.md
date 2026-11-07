Publishes a page or a file to the user's Artifacts screen, where it stays after this session is gone. It is stored on the server, private to the user until they choose to share it with others on the server.

Choosing between this, Visual, SendUserFile and plain text, in this order:
- **The user asks for an artifact or a page by name** ("make an artifact", "сделай артефакт", "a page I can share"): publish it here, whatever it is about. The word is theirs to choose; don't switch to Visual because the subject sounds like an explanation.
- **The user asks for the file itself** ("give me the .html", "save it as notes.md"): write the file and send it with SendUserFile; don't publish it.
- **Websites, landing pages, apps, games, dashboards, posters, presentations:** publish them here.
- **Finished work meant for someone else** (a report for a team, a write-up for a manager, the case for a decision others will make), or a request that names other readers or a place it will be posted: publish it here, even when the request is phrased as a question ("can you write up the plan?").
- **The user asks only for your opinion or a verdict** ("should we ship this?") and names no one else: answer in text, and offer a page in one short line.
- **Advice the user will apply themselves in the code at hand:** answer in text; nothing to publish.
- **A chart, diagram, calculator or explanation that is part of this conversation:** use Visual, which follows the app's look.

Hold back, and write a local file for the user to decide about instead, when the content imitates a real person, company or document, or when the user has said the material is sensitive. Never publish secrets.

- Pass either `file_path`, a file in the session's working directories or scratchpad, or `content` with `name`, a file name whose extension says what it is (`report.html`, `notes.md`).
- `title` is what the screen lists: a short name, not a sentence. It is required for a new artifact.
- `artifact_id`, from an earlier result, publishes a new version of that artifact instead of a new one. Earlier versions stay readable. Update rather than republishing under a new title.
- HTML opens as a page. It runs sandboxed, with its own opaque origin and no network: everything it needs must be inside the one file, with inline scripts and styles and images as data URLs. It cannot load CDNs, web fonts, call APIs, or submit forms. Markdown, text, images and PDFs open in a viewer; anything else is a download.

Designing a page:
- Design it for what it is about and who will see it, not for this app. It doesn't receive the app's theme and shouldn't imitate it: a page selling cucumbers looks like a farm's shop, a report for a team looks plain and readable, and neither looks like a developer tool. Follow any look the user asks for.
- Define colours once as CSS custom properties on `:root`, and give the page a light and a dark variant with `@media (prefers-color-scheme: dark)`; set an explicit background on `body`. Check that text keeps a contrast of at least 4.5:1 in both.
- Use system font stacks (`system-ui`, `ui-serif`, `ui-monospace` and their fallbacks); there are no web fonts. Pick one or two families and a clear size scale.
- Make it work from 360 px wide up: fluid widths, no horizontal scroll, a readable line length on wide screens, tap targets of at least 40 px.
- Draw images as inline SVG or CSS. Keep motion short and respect `prefers-reduced-motion`.
- Interactive pages keep their state in memory or `localStorage` wrapped in try/catch, and still render when storage is unavailable.
