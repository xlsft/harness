Types text into a field of the active tab: an input, a textarea or an editable element given by `ref` from BrowserSnapshot, or the focused element when `ref` is left out. On a select element, `text` picks the option whose label (or value) matches.

- `clear` (default true) replaces what the field holds; `clear: false` appends at the end. `text: ""` with clear empties the field.
- `submit: true` presses Enter afterwards, which submits most forms; the call then waits for the page that follows.
- The text is inserted at once, like a paste. For key-by-key work (shortcuts, arrow keys, picking from an autocomplete list) use BrowserKey.
- Never type passwords, one-time codes, payment card numbers or identity numbers. Snapshots mark such fields `[sensitive]`, and typing into them is refused unless the user approves that very call. Ask the user to fill them in the Browser pane themselves, and do not ask them to share the value in the chat.
- Type only data the user gave you or the task plainly needs, and do not submit forms that send messages, payments or personal data unless the user asked for it.
