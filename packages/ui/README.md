# harness ui

The desktop and mobile frontend for harness, built with [Nuxt 4](https://nuxt.com) and [Nuxt UI v4](https://ui.nuxt.com). It implements the "harness desktop" design in [`docs/design/ui`](../../docs/design/ui).

> **Status:** first iteration. Every desktop screen of the design renders on scripted data (the mock transport); the Connect transport talks to a real `harness-client serve --listen` (see [Running against a real client](#running-against-a-real-client)).

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
| `NUXT_PUBLIC_AGENT_TOKEN` | — | Bearer token of the client; an `?agentToken=` query parameter overrides it for the tab |

Generated code (`app/gen`) comes from the root `buf.gen.yaml` (`buf generate` at the repository root, after `pnpm install` here for `protoc-gen-es`).

## Running against a real client

`harness-client serve --listen` serves AgentService to browsers: the Connect protocol over HTTP/1.1 and h2c, with server streaming for `Subscribe`.

```bash
# 1. Start the client (signed in with `harness-client login`, or HARNESS_SERVER_URL + HARNESS_ACCESS_TOKEN).
#    Pick any free port; the listener binds loopback only.
harness-client serve --listen 127.0.0.1:7801
#   harness-client: serving AgentService on http://127.0.0.1:7801 (Connect over HTTP/1.1 and h2c)
#   harness-client: bearer token (also in ~/.harness/agent-token, mode 0600): 3f9c…

# 2. Start the UI with the connect transport and the token.
NUXT_PUBLIC_TRANSPORT=connect \
NUXT_PUBLIC_AGENT_URL=http://127.0.0.1:7801 \
NUXT_PUBLIC_AGENT_TOKEN="$(cat "${HARNESS_HOME:-$HOME/.harness}/agent-token")" \
pnpm dev --port 3210
```

Instead of `NUXT_PUBLIC_AGENT_TOKEN` you can open `http://localhost:3210/?agentToken=<token>` once: the UI keeps the token in `sessionStorage` for that tab and removes it from the address bar.

The listener is not an open door. Every request needs `Authorization: Bearer <token>` (compared in constant time; 401 otherwise). Requests with an `Origin` outside the allowlist get 403, as do requests whose `Host` is not loopback or the bind address (DNS rebinding). The generated token is random per start and its file is removed on exit, so after restarting the client pass the new token. Client flags:

| Flag | Env | Default | |
|---|---|---|---|
| `--listen host:port` | `HARNESS_AGENT_LISTEN` | — | TCP address; combinable with `--socket` |
| `--token` | `HARNESS_AGENT_TOKEN` | random per start | At least 16 characters; prefer the env var (flags show up in `ps`) |
| `--cors-origin` (repeatable) | `HARNESS_AGENT_CORS_ORIGINS` (comma-separated) | `http://localhost:3210`, `http://127.0.0.1:3210` | Exact browser origins allowed to call the client; add yours when the UI runs on another port |
| `--allow-non-loopback` | — | off | Allows a non-loopback `--listen` address, with a loud warning: plain HTTP, the token is the only protection |

`scripts/e2e/ui-connect.sh` checks all of this end to end with the fake gateway: it starts a client, probes the access rules with curl, then runs `test/connect.live.test.ts` (this package's `ConnectTransport` streaming a turn).

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
