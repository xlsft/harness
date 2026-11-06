# Contributing to harness

## Repository layout

A monorepo **without monorepo tooling**: every package builds on its own. Packages share no runtime code; they talk through the protobuf contracts in [`proto/`](proto).

| Path | What | Toolchain |
|---|---|---|
| `packages/core` | Server (Nitro v3) | Node 24, pnpm |
| `packages/client` | Agent client, Go library + `harness-client` binary | Go 1.26 |
| `packages/cli` | Terminal frontend | Go 1.26, charm.land |
| `packages/ui` | Desktop / mobile frontend | Node 24, pnpm, Nuxt 4 |
| `proto/` | RPC contracts (buf module) | buf |
| `evals/` | Model eval suite | Go |
| `catalog/` | Skills catalog, a plugin marketplace; see [its README](catalog/README.md) | JSON, checked by `scripts/catalog` (Python, standard library) |
| `scripts/` | e2e test and local dev stack | bash |
| `docs/` | Plan, protocol conventions, design | — |

## Rules

- **English only** in code, comments, docs and prompt files.
- **Clean-room.** Match Claude Code's formats, protocols and behavior, but never copy its code or prompt texts. Its identity (spinners, verbs, accent colors, logo) is off-limits; ours is in [docs/design/brand.md](docs/design/brand.md).
- **Small, focused commits** on `main`, each pushed right away. Commit messages end with a `Co-Authored-By` trailer when an AI agent wrote the change.
- **Generated code is committed.** After editing `proto/`, run `buf generate` from the root and commit the result. CI fails on stale generated code.
- **Contracts stay backward compatible within `v1`.** `buf breaking` runs on pull requests.

## Setup

```bash
# protobuf toolchain (once)
go install github.com/bufbuild/buf/cmd/buf@v1.59.0
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.1
export PATH="$(go env GOPATH)/bin:$PATH"

# server dependencies (also provides protoc-gen-es)
(cd packages/core && pnpm install)
```

## Everyday commands

```bash
buf lint proto && buf generate                     # contracts and codegen
(cd packages/core && pnpm typecheck && pnpm test)  # server
(cd packages/client && go vet ./... && go test -race ./...)
(cd packages/cli && go test ./... && go run ./cmd/harness demo)
scripts/e2e/run.sh                                 # core + client end to end, scripted model
```

## Local dev stack and real models

```bash
scripts/dev/stack.sh                               # build, start core on :3737 with an admin, sign the client in
HARNESS_HOME=.dev/home HARNESS_KEYRING=0 .dev/harness-client -p "..." --model <id>
scripts/dev/evals.sh                               # eval suite against the stack
scripts/dev/stack.sh stop
```

Add providers, keys and models in the app under Settings -> Providers (global ones as the admin, personal ones as any user); the auto-mode classifier is set there too. Keys are stored encrypted on the server and never go through env vars. State lives in git-ignored `.dev/` and `packages/core/.data-dev/`.

## Evals

The suite in [`evals/`](evals) measures how well models drive harness's tools. Run it before and after changing prompts (`packages/core/prompts`), tool semantics or the agent loop, and compare with the latest baseline in [docs/evals](docs/evals). New tasks must pass `go run ./run -selftest`: check.sh fails on the untouched repo and passes with the reference solution.
