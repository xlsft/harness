# harness ui

The desktop and mobile frontend for harness, built with [Nuxt 4](https://nuxt.com) and [Nuxt UI v4](https://ui.nuxt.com).

> **Status:** the Nuxt UI starter template only. Work starts after `core` and `client` (see [roadmap](../../docs/PLAN.md#5-roadmap)); the design will be provided separately.

## What it will be

- A single app for desktop, mobile and web that renders harness sessions. It includes the session sidebar, the transcript with collapsible tool calls, cards for permissions, questions and plans, the prompt box, and panes for diff, tasks, plan, terminal and preview.
- Sessions arrive through a pluggable transport:
  - `ipc` on desktop, where the native shell runs the Go client as a sidecar over stdio NDJSON ([protocol](../../docs/protocol/sdk-stream.md));
  - `relay` on web and mobile, which connects through the harness server to a client running on the user's machine.

## Development

```bash
pnpm install
pnpm dev        # http://localhost:3000
pnpm lint
pnpm typecheck
pnpm build
```
