# harness ui

The desktop and mobile frontend for harness, built with [Nuxt 4](https://nuxt.com) and [Nuxt UI v4](https://ui.nuxt.com). It implements the "harness desktop" design in [`docs/design/ui`](../../docs/design/ui).

> **Status:** first iteration. Every desktop screen of the design renders on scripted data (the mock transport); the Connect transport is built against the contract but `harness-client serve` has no TCP listener for browsers yet.

## Development

```bash
pnpm install
pnpm dev --port 3210   # http://localhost:3210 (pick a free port)
pnpm lint
pnpm typecheck
pnpm test              # Vitest: the session reducer
pnpm build
```

Transport, from runtime config:

| Variable | Default | |
|---|---|---|
| `NUXT_PUBLIC_TRANSPORT` | `mock` | `mock` replays scripted `harness.agent.v1` events; `connect` talks to a client |
| `NUXT_PUBLIC_AGENT_URL` | — | AgentService base URL for `connect` |

Generated code (`app/gen`) comes from the root `buf.gen.yaml` (`buf generate` at the repository root, after `pnpm install` here for `protoc-gen-es`).

## Screens

The URL holds the layout, so every screen of the design is linkable:

| Design artboard | URL |
|---|---|
| Main | `/` |
| MainTerminal | `/?pane=terminal&menu=settings` |
| MainBrowser | `/?pane=browser&menu=settings` |
| MainFiles | `/?pane=files` |
| MainArtifact | `/?pane=artifact&menu=title` |
| MainSideChat | `/?pane=sidechat` |
| MainTasks | `/?pane=tasks&menu=tools` |
| MainAgents | `/?pane=agents` |
| MainArtifactsGrid / List | `/artifacts`, `/artifacts?view=list` |

Other sessions: `?session=<id>`; `?pane=diff` opens the diff pane, `?pane=none` closes the right pane. Pane menus take `menu=` values (`agent`, `exited`, `page`, `viewport`, `approval`, `markdown`, `folder`, `search`, `comments`, `updating`, `empty`, `quote`, `streaming`) to reach the other artboard states.

With the mock, a message sent to an idle session starts a live turn: text containing "plan" proposes a plan, "ask" or "question" asks a question, anything else runs a Bash command that asks for permission. Messages typed during a turn queue up. Reference screenshots (1440×900, plus a few at phone width) are in `testdata/screens`.

## Layout of the code

- `app/assets/css/main.css`, `app/app.config.ts`: design tokens mapped to Nuxt UI (radius 0, primary blue, graphite neutral scale).
- `app/utils/icons.ts` + `HIcon`: the artboards' SVG paths (`scripts/design-icons.py` regenerates them).
- `app/lib/transport`: `SessionTransport`, the mock (`mock.ts`, `scenario.ts`) and Connect (`connect.ts`) implementations.
- `app/lib/session`: the view model and the reducer from `SessionEvent`s; `app/stores/sessions.ts` keeps one state per session.
- `app/components`: shell (`AppSidebar`, `SessionHeader`, `RightPane`), `transcript/`, `composer/`, the panes, `prose/` (markdown elements for MDC).
- `app/lib/fixtures`: demo data for panes the contract does not cover yet (terminal, browser, files, artifacts, side chat).
