# Plugin UI

A plugin can add panes to the right side of a session and items to its status bar. A pane is an HTML page from the plugin's folder. It runs in a sandboxed frame like a Visual, and reaches the app only through a small, versioned message API. Each capability of that API is declared in the manifest and approved on the device the first time it is used. The rules of [plugins](plugins.md) still apply: nothing of a plugin shows until this device approved its content, the folder is trusted, it is on, and policy allows it.

The client decides (`packages/client/plugins/ui.go`, `uidoc.go`, `packages/client/server/pluginui.go`). The frontend frames the page, asks the person, and shows what the client answered (`packages/ui/app/lib/plugins/ui.ts`, `components/PluginPane.vue`, `components/composer/PluginStatusItems.vue`).

## For authors

### The manifest

`ui` in `.harness-plugin/plugin.json`. Other agents' formats have no such field, so a plugin that has it still installs there and shows nothing.

```json
{
  "name": "session-cost",
  "ui": {
    "panes": [
      { "id": "meter", "title": "Session cost", "icon": "chart", "page": "./ui/meter.html" }
    ],
    "statusItems": [
      { "id": "cost", "text": "Cost", "icon": "chart", "tooltip": "What this session has cost so far", "pane": "meter" },
      { "id": "why", "text": "Why so much?", "prompt": "Explain what made this session expensive." }
    ],
    "capabilities": ["session.read", "session.prompt", "theme.read"],
    "network": ["https://api.example.com"]
  }
}
```

| Field | Rule |
|---|---|
| `panes[].id`, `statusItems[].id` | Lower case letters, digits and `-`, up to 40, unique in the plugin |
| `panes[].title` | Up to 60 characters; it heads the pane |
| `panes[].page` | An `.html` file in the plugin, starting `./` |
| `icon` | A name of the app's icon set (`chart`, `layout`, `box`, `clock`...); anything else shows the default |
| `statusItems[].text` | Up to 32 characters |
| `statusItems[]` | Exactly one of `pane` (a pane of the same plugin) and `prompt` (up to 2000 characters, needs `session.prompt`) |
| `capabilities` | Any of `session.read`, `session.prompt`, `files.open`, `theme.read` |
| `network` | Origins: `https://host`, `https://*.host`, `wss://host`, a port allowed; up to 16 |

At most 8 panes and 4 status-bar items. What breaks a rule is listed under the plugin's problems in Extensions and left out; the rest stands. Panes and status-bar items are part of what the user approves, and are listed with the plugin's contents.

### The page

The client reads the page and inlines the plugin's own files it names by a relative path: `<script src>`, `<link rel="stylesheet">`, and `<img src>` for PNG, JPEG, GIF, SVG and WebP. A file must stay inside the plugin's folder, links included; one that doesn't stops the pane with an error. A page is at most 1 MB, a file 2 MB, all of it 4 MB. Comments, `<base>`, `<meta http-equiv>` and `<link>`s that aren't stylesheets are removed. `url()` in a stylesheet isn't followed: use a `data:` URL.

The page's CSP:

```
default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob: <network>;
font-src data:; media-src data: blob:; connect-src <network or 'none'>; object-src 'none'; frame-src 'none';
worker-src 'none'; base-uri 'none'; form-action 'none'
```

So nothing loads from a CDN: bundle what the page needs. `fetch` and `WebSocket` reach only the declared origins, and only once the device allowed them.

The app's look comes in as CSS variables, the same as a Visual's: `--bg`, `--surface`, `--text`, `--text-strong`, `--muted`, `--line`, `--accent`, `--ok`, `--warn`, `--error`, `--chart-1`...`--chart-5`, `--font`, `--mono`, `--space`, `--text-sm`... Base styles for headings, buttons, inputs and tables come at zero specificity, so any rule of the page wins. The variables follow the theme while the pane is open.

### The message API, version 1

`window.harness` is there before the page's own scripts run.

```js
harness.version                       // 1
harness.call(method, params) → Promise
harness.on('session', fn) → unsubscribe
harness.on('theme', fn) → unsubscribe
```

| Method | Capability | Params | Result |
|---|---|---|---|
| `session.read` | `session.read` | none | `{ id, title, model, mode, cwd, status, costUsd, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, lastTurn?, messages }` |
| `session.prompt` | `session.prompt` | `{ text }`, up to 4000 characters | `{ sent: true }` |
| `files.open` | `files.open` | `{ path }`, absolute or from the session's folder | `{ path }` |
| `theme.read` | `theme.read` | none | `{ "--accent": "#2b7fff", ... }` |

- `status` is `idle`, `working` or `needs_input`. `costUsd` and the tokens count from when this client opened the session, as the gateway reported them. `lastTurn` is `{ costUsd, inputTokens, outputTokens, durationMs }` of the last finished turn. `messages` is the last six user and assistant messages with text, each cut at 400 characters; tool calls, tool results, attached files and reminders are left out.
- `session.prompt` reaches the session as a message starting `[from plugin <name>] `, shown with a chip naming the plugin. It never runs a slash command. It must come right after a click or a key press in the pane, and one plugin sends at most one every two seconds to a session.
- `files.open` opens the Files pane at a file inside the session's working folders.
- `on('session', fn)` calls `fn` with what `session.read` returns, each time the session's title, model, mode or status changes, once the page has read the session. `on('theme', fn)` calls `fn` with the tokens when the theme changes, once the page has read them.

A refused call rejects with an `Error` whose `code` is `denied` (the person said no, or the capability was revoked), `needs_user_action`, `unsupported_version` or `failed`. A capability the plugin didn't declare fails too. The first call of a capability waits while the app asks; a "no" holds until the pane is reloaded.

### Trying it

`harness-client plugin install ./my-plugin`, approve it, open a session in a trusted folder: the status-bar item is in the composer footer, and a click opens the pane. `harness-client plugin validate ./my-plugin` lists the problems of the manifest. The example is [`catalog/plugins/session-cost`](../../catalog/plugins/session-cost).

## How it is kept apart

- **The frame.** `sandbox="allow-scripts"` without `allow-same-origin`: an opaque origin with no access to the app's DOM, cookies, storage or token, and no popups, top navigation, forms or downloads. `referrerpolicy="no-referrer"`.
- **The CSP** is built by the client and is the first element of the head, before anything of the page. The frontend fills only the marker after it (`<!--harness-host-->`, the one comment the client keeps) with the theme and the bridge.
- **The bridge** keeps `parent`, `window` and `navigator.userActivation` before the page's scripts run, carries a per-load nonce, and removes its own script element. The host takes a message only from that frame's window with that nonce.
- **Every call goes to the client.** `CallPluginUi` checks the plugin may run in the session's folder, that policy allows plugin UI, that the method is a capability the manifest declared, and that this device approved it for the plugin's current content. Otherwise it answers `needs_approval`, and the frontend asks the person outside the frame, where the page can't click. The page never sees the RPCs or the agent token.
- **A plugin approves nothing.** Grants are made only by the app's own controls (`SetPluginUiGrant`: the pane's ask, a status-bar item's ask, Extensions), name the content hash the person was shown, and are refused from another machine. The API has no method for permissions, tools, settings or approvals.
- **Navigation.** A link or `location` change would carry what the page read in its URL. The frame's second load stops the pane: it is removed and says so, with a reload button. This is detection after the fact; the web build's own CSP also limits `frame-src` to the app and loopback.

## Approvals

Per device and never synced: `uiGrants` in the plugin's record in `HARNESS_HOME/plugins/state.json`, beside its approval, each with the content hash it was made for. A grant holds while the plugin has that content, so an update asks again, and stale grants are dropped with the update. `network` is one grant for all declared origins.

Extensions shows a plugin's panes, status-bar items and what they may ask for in its contents, and under "Its panes on this device" each capability with Allowed or Not allowed and a Revoke button. Revoking applies at the next call.

## Policy

Two keys of the org POLICY document, set in Settings > Managed policy:

| Key | Field | Effect |
|---|---|---|
| `disablePluginUi` | `plugin_ui_disabled` | No panes or status-bar items on any device; calls and grants are refused. The rest of a plugin runs as approved. Extensions says why. |
| `disablePluginUiNetwork` | `plugin_ui_network_disabled` | Panes get no origins in their CSP, whatever was declared and granted; a network grant is refused. |

`true` in any document stands. Both show in the managed-policy notice.

## Decisions

- **Our own manifest key.** `ui` is ours; no other format has panes. A plugin keeps working where `ui` means nothing.
- **One `plugin` pane, not a pane per plugin.** The pane list is a fixed union that layouts and links persist. One `plugin` pane shows the plugin pane opened last in that session, titled by it, and gets full screen, drag and drop like the others.
- **The status bar is the composer footer.** The app has no other status bar. Items sit left of the model picker.
- **Inline, not served.** The client inlines the page's files and the frame gets a `srcdoc`. Serving them over HTTP would need an unauthenticated route on the client, and a `connect-src` to it the page could also use. Inlining keeps `default-src 'none'`.
- **Status-bar items are static.** Text and icon come from the manifest. A dynamic item would need a page running while no pane is open.
- **Every capability asks first, `theme.read` included.** The task asked for it, and asking costs one click once. The CSS variables of the theme come without asking, as a Visual's do, since they are needed to draw anything.
- **The user-activation check of `session.prompt` is a speed bump.** The frame reports it; a page could lie. What holds is the grant, the client's limit of one prompt per two seconds, and the chip in the transcript.
- **Plugin UI stays on the device.** All four RPCs refuse a request from another machine (the relay): the plugin, its grants and its panes are this device's.
- **Nothing synced.** No `UserPreferences` field and no `SessionEvent` were needed; the ranges 64–65 and 400–409 of those messages stay free. ManagedPolicy uses 400 and 401.

## Not done

- A page can't keep a status-bar item current, or run with its pane closed.
- `url()` inside an inlined stylesheet, and fonts or media files, aren't inlined.
- Navigation is detected after the first load, not prevented.
- Panes of sessions run by another machine or by an ACP agent aren't shown.
