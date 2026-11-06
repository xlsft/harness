# harness desktop UI design

Source of truth for `packages/ui` (Nuxt 4 + Nuxt UI v4). The design lives in the "harness desktop" canvas; this folder is a snapshot of its artboards (`*.dc.html`, Design Component pages) plus the canvas index (`canvas.json`). Open a file in a text editor: the markup is plain HTML with inline styles; `{{…}}` holes and `<sc-for>`/`<sc-if>` blocks are filled by the small `Component` class at the bottom of each file (it holds the sample data).

The UI follows the same principle as the CLI: **Claude Code's structure and behavior, harness's own identity** ([brand.md](../brand.md)).

## Artboards

| Group | Files |
|---|---|
| Desktop screens, 1440×900, fluid | `Main` (session), `MainTerminal`, `MainBrowser`, `MainFiles`, `MainArtifact`, `MainSideChat`, `MainTasks` (background tasks + tools menu), `MainAgents`, `MainArtifactsGrid`, `MainArtifactsList` |
| Right-side panes, all states | `Terminal`, `Browser`, `Files`, `ArtifactPane`, `SideChat`, `TasksPane`, `AgentsPane`, `HeaderMenu` (tools menu), `ArtifactsPage` |
| Transcript components | `Md` (markdown), `Tool` (tool calls), `Media` (attachments), `Event` (chat events: permission, plan, question, notices), `Diff` (unified, split, turn summary), `Agent` (subagents), `Task` (background tasks), `Link` (links + OG cards), `Ref` (internal references), `User` (user messages) |
| Input | `Composer` (every input state: empty, typing, queue, `/` and `@` menus, attachments, mode/model/effort pickers, running, permission waiting) |

## Visual language

Terminal-native: a pure black ground, hairline borders, **no rounded corners**, **monospace everywhere**, color only for meaning.

### Color tokens

| Token | Value | Use |
|---|---|---|
| `bg` | `#000000` | app ground, transcript |
| `surface` | `#0a0a0a` | sidebar, panes, code blocks, tool call bodies |
| `surface-hover` / `selected` | `#131313` | row hover, current item |
| `surface-raised` | `#1c1c1c` | segmented-control active segment, inner rules |
| `border` | `#2b2b2b` | every hairline: panes, cards, inputs, rows |
| `border-strong` | `#454545` | focus-less emphasis, scrollbars |
| `text` | `#ffffff` | primary text, titles, active items |
| `text-body` | `#d4d4d4` | body text, row labels |
| `text-muted` | `#8f8f8f` | labels, metadata, icons, hints |
| `text-faint` | `#737373` | line numbers, disabled |
| `accent` | `#2b7fff` (blue-500) | brand mark, links, focus, current item rule, running state; tints `#2b7fff14/1f/26` |
| `accent-hover` | `#8ec5ff` (blue-300) | link hover |
| `success` | `#00bc7d` (emerald-500) | additions `+N`, done ✓, passing; diff add bg `#00bc7d1f` |
| `danger` | `#ff2056` (rose-500) | deletions `−N`, failures, high severity; diff del bg `#ff20561f` |
| `warning` | `#fe9a00` (amber-500) | medium severity, modified files |
| `orange` | `#fe6e00` (orange-500) | secondary status / alt accent option |
| `info` | `#00b8db` (cyan-500) | diff hunk headers |
| `pink` / `violet` | `#f6339a` / `#8e51ff` | tags, rare categorical marks |

Selection: white text on accent. The accent is themable (the artboards offer blue, emerald, orange, rose as alternatives); default blue.

### Typography

- Font: `'Cascadia Mono', 'Cascadia Code', 'JetBrains Mono', monospace` for everything (Google Fonts: Cascadia Code, JetBrains Mono).
- Scale (px): 10 · 10.5 · 11 · 11.5 · 12 · 12.5 · 13 (body) · 13.5 · 16 (wordmark / titles). Dense UI: 11–12.5 for chrome, 13 for transcript text.
- Weights: 400 nearly everywhere; 700 only for the avatar initial and strong emphasis.

### Shape and spacing

- Corners: square (0). Status dots and color chips are squares (6–7 px).
- Borders: 1 px `#2b2b2b`; the current item gets a 2 px accent rule on the left.
- Rows: 28 px min height, 9 px horizontal padding, 9 px icon gap; icons 15 px, `currentColor`.
- Header bars: 44 px (window/title row), 46 px (sidebar footer); pane headers ~36 px.
- Sidebar: 264 px; main area flexes; right panes are resizable.
- Card pattern (tool call, agent, diff): bordered box, header row (icon · name · target · flex · meta), optional body on `surface` with a 2 px left rule, footer row with counts and actions.

### Layout

- **Sidebar:** window controls row; logo + "xlsft`s harness"; primary nav (New session ⌘N, Artifacts, Scheduled, Extensions, More); sessions grouped by project with status marks (accent dot = needs attention, `+N` diff, `PR N`, `fail` in rose); footer with avatar, user, "self-hosted", settings, search.
- **Main:** session header (title, repo/branch, tools menu), transcript, composer at the bottom.
- **Right pane:** one of Terminal, Browser, Files, Artifact, Side chat, Background tasks, Agents, toggled from the header's tools menu.
- **Artifacts page:** grid and list views.

## Implementation notes for `packages/ui`

- Nuxt UI theme: map the tokens above to CSS variables (`--ui-bg`, `--ui-border`, `--ui-text-*`, `--ui-primary` = accent) and set `radius` to 0. Status colors: `primary: blue`, `success: emerald`, `error: rose`, `warning: amber`, `info: cyan`, `neutral` built from the greys above (not stock zinc).
- Icons in the artboards are inline SVG paths; reuse them as an icon component set (one SVG per name) rather than an icon font.
- The design is dark-only for now.
