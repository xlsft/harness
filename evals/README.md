# evals

An eval suite for harness. It measures how well different models from the server's model catalog drive harness's Claude-Code-style tools (`Read`, `Edit`, `Bash`, `Grep`…), and it catches regressions when server-side prompts change.

Each attempt runs the real headless client against a throwaway copy of a small repository. A deterministic `check.sh` grades the result offline. No model output is judged by another model.

Like every package in this repository, `evals/` is standalone: it has its own `go.mod` and shares no code with the rest of the repo.

## Requirements

- Go (see `go.mod`), `git` and `bash`.
- Task toolchains: Go, Node.js 24 or newer (TypeScript tasks run natively through type stripping, with no `npm install`), and Python 3.10 or newer, whose standard library also grades the `doc-*` tasks. Their binary files come from `TestWriteEvalFixtures` in `packages/client/documents`. Checks never use the network.
- A `harness-client` binary that is signed in to a harness server (`harness-client login <url>`). Build it from `packages/client` with `go build ./cmd/harness-client`.

## Running

From `evals/`:

```sh
# validate the tasks themselves (no model calls): every check must fail on the
# untouched repo and pass with the reference solution
go run ./run -selftest

# run two models over all tasks, 4 attempts in parallel, 3 repeats each
go run ./run -client ../packages/client/harness-client \
  -models claude-sonnet-4-6,deepseek-chat -parallel 4 -repeat 3

# only some tasks
go run ./run -models qwen3-coder -tasks go-fix-failing-test,ts-rename-across-files
```

| Flag | Default | Meaning |
|---|---|---|
| `-models a,b` | `$HARNESS_EVAL_MODELS` | Model ids from the server's model catalog (required unless `-selftest`) |
| `-tasks x,y` | all | Task ids to run |
| `-parallel N` | 1 | Attempts executed concurrently |
| `-repeat N` | 1 | Attempts per model × task (models are not deterministic) |
| `-client path` | `$HARNESS_CLIENT`, else `harness-client` on `PATH` | The client binary |
| `-server url` | `$HARNESS_SERVER`, else stored credentials | Passed to the client as `--server` |
| `-client-arg x` | none | Extra client argument, repeatable (for example `-client-arg=--effort -client-arg=high`) |
| `-out dir` | `results/<timestamp>` | Output directory (`results/` is git-ignored) |
| `-keep` | off | Keep each attempt's working copy for debugging |
| `-check-timeout` | `2m` | Timeout for each `check.sh` |
| `-selftest` | off | Validate tasks instead of running models |

### What one attempt does

1. Copies `tasks/<id>/repo/` into a fresh temp directory, runs `git init` and commits the snapshot, so the agent starts from a clean tree.
2. Runs the client in that directory, killed when the task's `timeout` expires:

   ```sh
   harness-client -p "<prompt>" --model <id> --output-format json \
     --max-turns <max_turns> --permission-mode bypassPermissions --no-session-persistence
   ```

   The command line is built in one place, `clientArgs` in [`run/client.go`](run/client.go). Change it there if the CLI changes.
3. Parses the JSON result (`num_turns`, `usage`, `total_cost_usd` or `cost_usd`, `result`, `is_error`). Missing fields count as zero.
4. Runs `tasks/<id>/check.sh` in the working copy. Exit code 0 means pass. The check runs even when the client reported an error such as `error_max_turns`, because the work may already be done.

### Output

- `results.md`: per-model summary (pass rate, timeouts, errors, average turns, tokens, cost, time), a task × model matrix, and a list of failed attempts.
- `results.json`: the same summary plus every attempt.
- `runs/<model>/<task>/<repeat>/`: `command.txt`, `client.stdout`, `client.stderr`, `result.txt` (the agent's final answer) and `check.log`.

Statuses: `pass`; `fail` (check failed); `timeout` (the client was killed); `error` (the run itself broke: the client could not start, its output could not be parsed, or the check could not run).

## Tasks

| Task | Language | Tags | What it exercises |
|---|---|---|---|
| `go-fix-failing-test` | Go | bugfix | Run the tests, find the byte-vs-rune bug, fix it without touching the tests |
| `go-off-by-one-bug-report` | Go | bugfix | Reproduce a bug from `BUG_REPORT.md`, fix a pagination off-by-one, add a regression test |
| `ts-rename-across-files` | TypeScript | refactor | Rename a function across definition, imports, namespace calls and comments |
| `ts-implement-lru-cache` | TypeScript | feature | Implement a class from its doc comment (recency, eviction callback, edge cases) |
| `py-implement-from-docstring` | Python | feature | Implement `slugify` from a detailed docstring (Unicode, truncation rules) |
| `py-answer-question` | Python | read-only | Trace config defaults across modules and answer with `ANSWER: <n>`; files must stay untouched |
| `shell-release-checksum` | shell | shell, docs | Discover an artifact's SHA-256 and size with the shell and fill in release notes |

## Adding a task

Create `tasks/<id>/` (the directory name is the task id):

```
tasks/<id>/
  task.yaml       # required
  repo/           # required: the snapshot the agent works in
  check.sh        # required: grades the attempt, exit 0 = pass
  hidden/         # optional: files check.sh overlays before grading (e.g. hidden tests)
  solution/       # optional: reference fix overlaid on repo/ by -selftest
  solution.txt    # optional: reference final answer used by -selftest
```

`task.yaml`:

```yaml
id: go-fix-failing-test        # must match the directory name
title: Fix a failing Go test
language: go                   # informational
tags: [bugfix]                 # bugfix | refactor | feature | read-only | shell | ...
timeout: 5m                    # Go duration; default 5m
max_turns: 25                  # passed as --max-turns; default 30
prompt: |
  The test suite fails (`go test ./...`). Fix the bug without modifying the tests.
```

`check.sh` runs under `bash` with the working copy as its current directory. It gets these environment variables:

| Variable | Value |
|---|---|
| `EVAL_TASK_DIR` | Absolute path of `tasks/<id>` (the script can also use its own location) |
| `EVAL_WORKDIR` | The working copy (same as the current directory) |
| `EVAL_RESULT_FILE` | File containing the agent's final answer text (empty if none) |
| `EVAL_MODEL` | The model id (`selftest` during `-selftest`) |

Guidelines:

- **Deterministic and offline.** No network, no clocks, no randomness. Set `GOPROXY=off GOTOOLCHAIN=local` for Go and use only the standard library or `node:test`.
- **Quick.** Snapshots stay tiny, and an attempt should be doable in a handful of turns.
- **Do not trust files the agent can edit.** Keep the grading tests in `hidden/` and copy them in from `check.sh`, so an agent that edits or deletes visible tests cannot pass. Use `git status` or `git diff` against the initial commit to require a change (a new test file) or forbid one (read-only tasks).
- **Grade answers by a fixed format.** For question tasks, ask for a final line such as `ANSWER: <value>` and parse `$EVAL_RESULT_FILE`.
- **Ship a reference solution** and make sure `go run ./run -selftest` reports `fails` for the untouched repo and `passes` for the solution.

Go files under `tasks/` belong to the stub module `tasks/go.mod`, which keeps them out of the runner's `go vet ./...` and `go build ./...`. Each Go task's `repo/` has its own `go.mod`.
