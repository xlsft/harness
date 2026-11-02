# harness

An agentic coding harness that replaces Claude Code. It keeps the same tools, the same UX and the same architecture, and runs with **any model** behind an OpenAI- or Anthropic-compatible endpoint.

> **Status:** early development (milestone M0). Nothing is usable yet. See the [plan](docs/PLAN.md).

## How it works

harness follows Claude Code's architecture, with a self-hosted backend in place of Anthropic's.

- **The client runs locally.** It drives the agent loop and executes tool calls (shell, files, git, stdio MCP servers) on the machine where your code lives, either local or over SSH. It enforces permissions and runs hooks.
- **The server is self-hosted**, a single Docker container. It handles sign-in, stores provider API keys behind an LLM gateway, serves the prompts, and keeps every piece of user data: settings, memory, skills, agents, MCP connections and sessions. It also relays sessions to web and mobile. It never executes code.
- **Frontends are just views.** The terminal UI and the desktop/mobile UI render the same sessions through the same client.

```
 ┌───────────────────── harness server (Docker) ─────────────────────┐
 │ auth · LLM gateway + provider keys · prompt registry · user data   │
 │ MCP registry & proxy · session relay · tunnels                     │
 └──────────▲───────────────────────▲─────────────────────▲──────────┘
            │ gateway / sync API    │ relay               │
 ┌──────────┴───────────────────────┴───────┐       web / mobile UI
 │ harness client — agent loop, tool         │
 │ execution, permissions, hooks, sessions   │
 │   ▲ Go API               ▲ stdio NDJSON   │
 │   cli (terminal UI)      desktop UI       │
 └──────────────────────────┬────────────────┘
                            │ ssh (your keys)
                            ▼
                remote machine: harness client
```

## Packages

The repository is a monorepo **without monorepo tooling**. Each package is self-contained, with its own `package.json` or `go.mod`, build and CI job. Packages share no code: they talk only through the contracts in [`docs/protocol`](docs/protocol).

| Package | Role | Stack | Status |
|---|---|---|---|
| [`packages/core`](packages/core) | Server: auth, LLM gateway, prompts, user data, MCP, relay | [Nitro v3](https://nitro.build), TypeScript | in progress |
| [`packages/client`](packages/client) | Universal client: agent loop and tool execution; a Go library and a headless binary | Go | in progress |
| [`packages/cli`](packages/cli) | Terminal frontend, designed to match Claude Code one-to-one | Go, [charm.land](https://charm.land) | planned |
| [`packages/ui`](packages/ui) | Desktop and mobile frontend | [Nuxt 4](https://nuxt.com), [Nuxt UI](https://ui.nuxt.com) | template only |

Build order: `core` + `client` first, then the `cli` and `ui` frontends, deployment last.

## Documentation

- [Implementation plan](docs/PLAN.md): architecture, decisions, roadmap, risks.
- [Protocols](docs/protocol):
  - [auth](docs/protocol/auth.md) — device flow and tokens;
  - [LLM gateway](docs/protocol/gateway.md);
  - [prompt bundle](docs/protocol/prompts.md);
  - [client SDK stream](docs/protocol/sdk-stream.md).

## Compatibility with Claude Code

- Tool names and schemas match Claude Code, so permission rules, hooks and agent definitions carry over.
- Project files are read as-is: `CLAUDE.md`, `AGENTS.md`, `.claude/` (settings, rules, skills, agents, commands) and `.mcp.json`.
- Sessions are stored in Claude Code's JSONL shape. Existing Claude Code sessions and user configuration can be imported.

This is a clean-room implementation: it is compatible with Claude Code's formats and protocols but contains none of its code or prompts.

## License

[MIT](LICENSE)
