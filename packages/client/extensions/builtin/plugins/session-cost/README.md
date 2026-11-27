# Session cost

An example of plugin UI for xlsft\`s harness: a pane that shows what the session has cost so far, and a status-bar item that opens it.

- `.harness-plugin/plugin.json` declares the pane, the status-bar item, and the two capabilities the page uses: `session.read` and `theme.read`.
- `ui/meter.html` is the page. Its stylesheet and script are separate files; the client inlines them when the pane opens.
- `ui/meter.js` calls `harness.call('session.read')` and listens with `harness.on('session', ...)`.

The first time the pane reads the session, the app asks you to allow it on this device. Extensions shows what you allowed and can revoke it.

How panes work, what the page may do and the whole message API: [docs/design/plugin-ui.md](https://github.com/xlsft/harness/blob/main/docs/design/plugin-ui.md).

MIT, as the rest of the repository.
