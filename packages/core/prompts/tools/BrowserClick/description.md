Clicks in the active tab the way a user does with a mouse. Pass `ref` from BrowserSnapshot (preferred: the element is scrolled into view and clicked at its center), or `x` and `y` in CSS pixels of the viewport, read from a viewport BrowserScreenshot (its pixels are CSS pixels unless the result reports a scale).

- `button` is left (default), right or middle; `double: true` double-clicks; `modifiers` are keys held during the click, for example ["meta"] or ["shift"].
- The result says what was clicked and what followed: a navigation (the call waits for the new page) or a new tab, which becomes the active tab. When another element covers the target (an overlay, a cookie banner), the result says that element received the click.
- Native select menus do not open in the headless browser: choose an option with BrowserType on the select.
- Clicking acts on the site, so the first action on a new site asks the user. Do not click anything that buys, deletes, publishes, sends a message or changes account settings unless the user asked for exactly that; ask first when in doubt. JavaScript dialogs (alert, confirm) are accepted automatically and noted in BrowserConsole.
- It fails while the user has taken over the browser: wait for them to hand it back.
