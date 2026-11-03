Waits until something happens in the active tab, instead of sleeping or polling with snapshots. Give exactly one condition: `text` (appears in the page's visible text), `text_gone` (disappears, such as a loading label), `selector` (a CSS selector matches a visible element), `url` (the address contains this text, for example after a redirect) or `navigation: true` (the next page load finishes). `timeout` is in milliseconds (default 10000, at most 60000).

- A timeout is a normal result, not an error: decide what to do next (snapshot, screenshot, console) rather than waiting again blindly.
- Clicks, typing with submit and navigations already wait for the page they start; BrowserWait is for what comes after, such as data the page fetches or a job finishing on the server.
- Read-only: needs no permission.
