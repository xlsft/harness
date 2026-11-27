# harness docs / landing

Public landing + docs for **xlsft's harness** — the agentic coding harness that replaces Claude Code with any model behind an OpenAI- or Anthropic-compatible endpoint, served by your own backend.

Nuxt 4 + Nuxt UI v4. Design tokens mirror `packages/ui` (`xlsft's dark` / `xlsft's light`): pure black/white ground, hairline borders, square corners, blue accent `#2B7FFF`, monospace everywhere.

```sh
pnpm install
pnpm dev        # http://localhost:3000
pnpm generate   # static site in .output/public
```

Targets: **Web** (this site + the web UI in `packages/ui`) and **macOS** (Tauri desktop in `packages/desktop`).
