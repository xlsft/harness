<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-full-dark.svg">
    <img alt="xlsft's harness" src="docs/assets/logo-full-light.svg" width="520">
  </picture>
</p>

<p align="center">
  <b>Your workflow, unbounded.</b><br>
  The coding agent you already know, with any model and a server you own.
</p>

# harness

An agentic coding harness that replaces Claude Code. It keeps the same tools, the same UX and the same architecture, and runs with **any model** behind an OpenAI- or Anthropic-compatible endpoint.

> **Status:** early development. The core harness works headless (`harness-client -p`) against real models; frontends and the deployable image are in progress. See the [plan](docs/PLAN.md#5-roadmap).

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
            │ gRPC (Connect)        │ relay               │
 ┌──────────┴───────────────────────┴───────┐       web / mobile UI
 │ harness client — agent loop, tool         │
 │ execution, permissions, hooks, sessions   │
 │   ▲ Go API               ▲ gRPC (local)   │
 │   cli (terminal UI)      desktop UI       │
 └──────────────────────────┬────────────────┘
                            │ ssh (your keys)
                            ▼
                remote machine: harness client
```

## Packages

The repository is a monorepo **without monorepo tooling**. Each package is self-contained, with its own `package.json` or `go.mod`, build and CI job. Packages share no runtime code: all interaction is gRPC-compatible RPC ([ConnectRPC](https://connectrpc.com)) defined in protobuf under [`proto/`](proto), with generated code committed into each package.

| Package | Role | Stack | Status |
|---|---|---|---|
| [`packages/core`](packages/core) | Server: auth, LLM gateway, prompts, user data, MCP, relay | [Nitro v3](https://nitro.build), TypeScript | in progress |
| [`packages/client`](packages/client) | Universal client: agent loop and tool execution; a Go library and a headless binary | Go | in progress |
| [`packages/cli`](packages/cli) | Terminal frontend, designed to match Claude Code one-to-one | Go, [charm.land](https://charm.land) | brand primitives |
| [`packages/ui`](packages/ui) | Desktop and mobile frontend | [Nuxt 4](https://nuxt.com), [Nuxt UI](https://ui.nuxt.com) | template only |

Build order: `core` + `client` first, then the `cli` and `ui` frontends, deployment last.

## Documentation

- [Contributing](CONTRIBUTING.md): setup, commands, dev stack, evals.
- [Design](docs/design): [visual identity](docs/design/brand.md) and the [terminal UI reference](docs/design/cli-reference.md).
- [Eval baselines](docs/evals).

- [Implementation plan](docs/PLAN.md): architecture, decisions, roadmap, risks.
- [Protocol contracts](proto): protobuf services for auth, the LLM gateway, prompts and the agent API.
- [Protocol conventions](docs/protocol): transport, errors, versioning, codegen, and [gateway behavior](docs/protocol/gateway.md).

## Compatibility with Claude Code

- Tool names and schemas match Claude Code, so permission rules, hooks and agent definitions carry over.
- Project files are read as-is: `CLAUDE.md`, `AGENTS.md`, `.claude/` (settings, rules, skills, agents, commands) and `.mcp.json`.
- Sessions are stored in Claude Code's JSONL shape. Existing Claude Code sessions and user configuration can be imported.

This is a clean-room implementation: it is compatible with Claude Code's formats and protocols but contains none of its code or prompts.

## License

[MIT](LICENSE)
