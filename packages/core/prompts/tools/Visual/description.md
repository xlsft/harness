Shows a self-contained HTML document in the conversation: a chart, a diagram, a comparison table with controls, a small calculator or simulation. The user sees it rendered under your message and can open it full screen. It runs nothing on this machine and needs no approval.

How it is rendered:
- in a sandboxed frame with scripts allowed and no access to the app, its cookies or this machine;
- network access is limited to scripts, styles and fonts from cdnjs.cloudflare.com, cdn.jsdelivr.net and unpkg.com; fetch and XHR are blocked, so put the data in the document;
- the frame grows to the height of the content, up to the window height; design for a width of 600-800 px that also works at 360 px;
- the app's colours are available as CSS variables: `--bg`, `--surface`, `--raised`, `--line`, `--muted`, `--text`, `--accent`, `--ok`, `--warn`, `--error`, and `--font` / `--mono`. Use them instead of fixed colours so the visual matches the app in light and dark themes. The page background is transparent by default.

Talking back: the document can call `harness.send("text")` to send a message to you as the user, for example from a button ("Use the 80% threshold") or a small form. Such messages start with `[from visual <id>]`. Use it for choices that you then act on; do not send messages without a click.

Versions: `id` names the visual. Calling Visual again with the same `id` adds a new version of the same card (the user can flip back to earlier ones), for example after "make the line thinner". Use a new `id` for a different visual.

When it helps:
- data that reads better as a chart or a diagram than as a table or a list;
- an explanation that benefits from interaction: a slider that changes a formula, a step-through of an algorithm;
- a choice between options the user should see side by side.

When not to use it:
- for plain text, code, or a short table: write it in your reply;
- for files that belong in the project: write them with Write; Visual does not save anything;
- instead of a reply: still say in text what the visual shows and what follows from it.

Write a complete document (`<!doctype html>` with inline `<style>` and `<script>`), keep it under 512 KB, and keep animations short and optional.
