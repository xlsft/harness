Shows a self-contained HTML document in the conversation: a chart, a diagram, a comparison table with controls, a small calculator or simulation. The user sees it rendered under your message and can open it full screen. It runs nothing on this machine and needs no approval.

How it is rendered:
- in a sandboxed frame with scripts allowed and no access to the app, its cookies or this machine;
- network access is limited to scripts, styles and fonts from cdnjs.cloudflare.com, cdn.jsdelivr.net and unpkg.com; fetch and XHR are blocked, so put the data in the document;
- the frame grows to the height of the content, up to the window height; design for a width of 600-800 px that also works at 360 px.

Follow the app's look. The document receives the app's theme as CSS variables and base styles in that look, so plain HTML already matches; build on them instead of inventing a style:
- colours: `--bg` (page), `--surface` (cards, panels), `--hover`, `--raised` (inputs, chips), `--line` and `--line-strong` (borders), `--text`, `--text-strong` (headings, values), `--muted` and `--dimmed` (labels, secondary text), `--accent` and `--accent-hover`, `--on-accent` (text on accent), `--ok`, `--warn`, `--error`, `--cyan`, `--violet`, `--pink`, `--orange`;
- chart series in this order: `--chart-1` ... `--chart-5` (checked for colour-blind separation; fold more series into "Other" in `--dimmed`); gridlines and axes in `--line` and `--muted`;
- shape and type: `--radius` for controls and `--radius-box` for cards (the theme's corners; 0 in square themes: use the variables, never fixed radii), `--border` (1px), `--font` and `--mono` (one monospace family; do not load other fonts), sizes `--text-xs` 11px, `--text-sm` 12px, `--text-md` 13px (body), `--text-lg` 15px, `--text-xl` 18px, `--leading` 1.6;
- spacing on a 4px grid: `--space`, e.g. `calc(var(--space) * 3)`; controls are `--control-h` (28px) tall;
- `button`, `button.primary`, `input`, `select`, `textarea`, `table`, `code`, `pre`, headings and links are already styled; write them plainly and only add layout. The body has a 12px padding and a transparent background: the card behind it is `--surface`.
- In canvas or SVG code read the values with `getComputedStyle(document.documentElement).getPropertyValue('--accent')`; never hard-code colours, so the visual follows the theme.
- No gradients, glows or drop shadows; flat surfaces separated by 1px `--line` borders, like the rest of the app.

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
