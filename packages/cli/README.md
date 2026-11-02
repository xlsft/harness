# harness cli

The terminal frontend for harness, built with the [charm.land](https://charm.land) toolchain (Bubble Tea v2, Lip Gloss v2, Glamour v2). It matches Claude Code's terminal UI in layout and behavior ([reference](../../docs/design/cli-reference.md)) with harness's own identity ([brand](../../docs/design/brand.md)).

> **Status:** first working version of the interactive TUI (M3).

```bash
go run ./cmd/harness login https://harness.example.com
go run ./cmd/harness                      # interactive session in the current directory
go run ./cmd/harness --model qwen-flash --permission-mode acceptEdits
go run ./cmd/harness --output-style explanatory   # see "Output styles" below
go run ./cmd/harness -p "fix the failing test" --output-format json   # headless, same as harness-client -p
go run ./cmd/harness whoami | logout | demo
```

`HARNESS_HOME`, `HARNESS_KEYRING`, `NO_COLOR`, `HARNESS_COLOR`, `HARNESS_THEME`, `HARNESS_ASCII` and `HARNESS_REDUCED_MOTION` work as in the client and brand docs. `--verbose` (or `HARNESS_DEBUG=1`) writes client logs to `$HARNESS_HOME/cli.log`.

## Layout

| Path | Purpose |
|---|---|
| `brand/` | Palette with truecolor / 256 / 16 / no-color fallbacks, logo mark, Rail and Pulse loaders |
| `internal/client` | The only file that touches `packages/client`: runs the client's `AgentService` (`server.Server`) in-process on a private unix socket and drives it with the generated Connect client |
| `internal/render` | Pure rendering functions (transcript items, tool groups, diffs, prompts, menus, working line, footer, banner), golden-tested in `testdata/` |
| `internal/tui` | The Bubble Tea model: `model.go` (state, transcript items), `events.go` (`harness.agent.v1` events), `keys.go` (input), `prompts.go` (permission / question / plan / choice), `commands.go` (slash commands, menus, rewind), `view.go` (the live dock), `outputstyle.go` (`/output-style` picker) |
| `cmd/harness` | Entry point and subcommands |
| `testdata/screens` | Screen captures from pseudo-terminal runs (`*.attrs.txt` mark styled runs as `«attrs\|text»`) |

The program runs inline (no alternate screen). Finished transcript items are printed into the terminal's scrollback; the view is only the live dock: in-progress tool groups and the streaming paragraph, the working line, queued messages, the todo list, then the input (or a modal prompt) and the footer. On resize and on `ctrl+o` the whole transcript is reprinted.

```bash
go test ./...                     # golden tests
go test ./internal/render -update # rewrite the golden files after an intended change
go test ./internal/tui -update    # same for the picker goldens in internal/tui/testdata
```

## Output styles

`/output-style` opens a picker of the built-in, user and project styles (`ListOutputStyles`): the active one is marked `✔` and preselected, custom styles show their source, and the last entry, "New style…", prints where to create a style file (`.claude/output-styles/<name>.md`) with a frontmatter template. `/output-style <name>` switches directly; `default` clears the style. A non-default style shows in the footer (`explanatory style · model · context`) and in `/status`. `--output-style` applies to every session of the run (a resumed session otherwise keeps its recorded style), and `/clear` keeps the style picked in the session. Semantics (sources, delivery through a reminder, the deferred change to the system prompt) are described in the [client README](../client/README.md#output-styles).
