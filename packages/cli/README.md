# harness cli

The terminal frontend for harness, built with the [charm.land](https://charm.land) toolchain. It will match Claude Code's terminal UI in layout and behavior ([reference](../../docs/design/cli-reference.md)) with harness's own identity ([brand](../../docs/design/brand.md)).

> **Status:** brand primitives only. The full TUI is milestone M3.

## What is here

- `brand/` — palette with explicit truecolor / 256 / 16 / no-color fallbacks and dark/light variants, the logo mark (18×6, 12×4, ASCII), and the loaders (Rail, Pulse, ASCII). Detection honors `NO_COLOR`, `HARNESS_COLOR`, `HARNESS_THEME`, `HARNESS_ASCII` and `HARNESS_REDUCED_MOTION`.
- `cmd/harness` — `harness demo` renders the banner, transcript styles and an animated loader, to check the identity in real terminals.

```bash
go run ./cmd/harness demo
HARNESS_COLOR=16 go run ./cmd/harness demo
NO_COLOR=1 go run ./cmd/harness demo
```
