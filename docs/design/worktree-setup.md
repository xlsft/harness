# Worktree setup

A session worktree (`.harness/worktrees/<name>`) is a fresh checkout: no `node_modules`, no virtualenv, nothing generated. Measured on a real session, an agent spent many turns making the repository's tests runnable there: it symlinked the main checkout's `node_modules`, then had to replace the link with per-entry links because Nuxt and vite wrote into it, which also wrote into the main checkout. xlsft\`s harness sets up a new worktree itself, before the agent works in it, from a file the project commits.

## The file

`.harness/worktree.json`, at the root of the checkout. A file of its own rather than a `setup` key in `.harness/settings.json`: settings are per-user and per-device layers that merge (with `settings.local.json`, the server's documents and policies), while a setup is a fact about the repository, like `.worktreeinclude`. One file, no merging, no trust prompt of its own (see [What runs it](#what-runs-it)).

```json
{
  "shareDependencies": true,
  "clone": ["packages/ui/.nuxt"],
  "setup": [
    { "run": "pnpm --dir packages/ui exec nuxt prepare", "timeoutSeconds": 300 },
    { "run": "make generate", "dir": "tools" }
  ]
}
```

| Key | What |
|---|---|
| `shareDependencies` | Give every folder with a tracked lockfile its dependencies, before `setup`: cloned copy-on-write from the source checkout where it has them installed, else installed from the package manager's cache. See below |
| `cloneDependencies` | `false` turns the cloning off: `shareDependencies` then always installs. Default `true` |
| `clone` | More folders, relative to the root, to clone copy-on-write from the source checkout when it has them (build caches, generated code: `packages/ui/.nuxt`). Before everything else |
| `setup[].run` | A command line, run by the session's shell (the Bash tool's shell, environment and snapshot) |
| `setup[].dir` | Where it runs, relative to the worktree's root (default the root). An absolute path, a path out of the worktree, or a folder that leaves it through a symbolic link is refused |
| `setup[].timeoutSeconds` | Its time limit: default 600, at most 3600. It is killed with what it started when the limit passes |

Unknown keys are an error, so a typo doesn't pass for a setup that does nothing. At most 32 commands and 32 folders to clone.

The file is read from the checkout the worktree is made from (the main checkout, or the worktree a session was in), not from the new worktree: a setup that is not committed yet applies too, and accepting the suggestion writes it there.

## Sharing dependencies safely

`shareDependencies` never links one checkout's dependency folder into another, and never writes into the source checkout. Every worktree gets a `node_modules` of its own. Tools that write into their dependency folder (Nuxt, vite, Prisma) write into the worktree's.

### Cloning, the default

**Decision:** when the source checkout has a lockfile's `node_modules` installed, the worktree's starts as a copy-on-write clone of it: `cp -c -R -p` (clonefile(2)) on macOS, `cp -R -p --reflink=always` on Linux. On APFS, btrfs and XFS a clone takes about as long as listing the files and no disk space, and the two copies part block by block on the first write: what a build writes in the worktree never reaches the original. That is what the symlink in the measured session lacked. It is also faster than any install, which still has to link or copy every file and run lifecycle scripts.

How it runs (`worktree/clone.go`):

- The clone goes to a temporary name next to the target and is renamed into place when complete; it never replaces a folder that exists. `--reflink=always`, not `auto`: a file system that can't clone fails the step at once instead of copying gigabytes.
- Symbolic links are copied as links. pnpm's and npm workspaces' links are relative and resolve inside the worktree. A link with an absolute target inside the source checkout is pointed at the same path in the worktree, so nothing in the clone leads back.
- The source's lockfile (as it is in its working tree, which is what its `node_modules` was installed from) is compared with the worktree's. Equal: done, no install. Different (the worktree is on another commit, or the main checkout moved on): the package manager reconciles the clone from its cache only, then with the network if the cache lacks something: `pnpm install --frozen-lockfile --offline || pnpm install --frozen-lockfile --prefer-offline`, `npm ci --offline … || npm ci --prefer-offline …`, `yarn install --frozen-lockfile --offline || …` (Yarn 1; Yarn 2+ and bun read their cache first by themselves).
- A file system that can't clone (HFS+, ext4, a network volume) makes the clone step run the install it stands for instead, with a line saying so in its output.
- The clone runs in the client itself, not in the sandbox: it is the client's own fixed `cp` from one checkout into the new worktree, and reading the source is the point. Installs and the commands of `setup` run in the sandbox (below).
- Not cloned: a virtualenv (`.venv` holds the absolute path of the checkout it was made in, in `pyvenv.cfg` and every script's first line), and anything when the source has no `node_modules` (Yarn Plug'n'Play has none).

Known limit: pnpm's `.bin` shims are text files that name the source's `node_modules/.pnpm` in `NODE_PATH`. That path is only a fallback for module resolution, and reading there writes nothing. If a tool must not see the source checkout at all, set `cloneDependencies` to `false`.

Cloning is the default of `shareDependencies`, and so of the suggestion. `"cloneDependencies": false` installs instead, for a project whose installs must run their lifecycle scripts in every checkout.

### Installing from the shared cache

Where nothing can be cloned (no `node_modules` in the source checkout, a lockfile of uv, `cloneDependencies: false`), the client installs from the package manager's cache:

The client finds the tracked lockfiles (`git ls-files`, not under `node_modules`) and handles each folder that has one:

| Lockfile | Command | Why it is fast and safe |
|---|---|---|
| `pnpm-lock.yaml` | `pnpm install --frozen-lockfile --prefer-offline` | pnpm's content-addressed store: files are hard links (or copy-on-write clones) of the store's, so a warm store installs in seconds and uses no extra space. `--ignore-workspace` is added when no `pnpm-workspace.yaml` sits in that folder or above it inside the checkout, because pnpm looks for one in parent folders, and a worktree's parent folders are the main checkout's |
| `bun.lock`, `bun.lockb` | `bun install --frozen-lockfile` | bun's global cache, hard links or clones |
| `yarn.lock` | `yarn install --immutable` with a `.yarnrc.yml` next to it (Yarn 2+); else `yarn install --frozen-lockfile --prefer-offline` | Yarn's global cache (Yarn 2+), or its offline cache (Yarn 1). With Plug'n'Play nothing is copied at all |
| `package-lock.json` | `npm ci --prefer-offline --no-audit --no-fund` | npm's cache in `~/.npm`. Safe, but npm copies every file: slower and bigger than pnpm |
| `uv.lock` | `uv sync --frozen` | uv's global cache, hard links into the worktree's `.venv` |
| `go.sum` | nothing | Go's module and build caches serve every checkout already |
| `Cargo.lock` | nothing | Cargo's registry is shared; `target/` is per worktree, which costs a first build. Pointing `CARGO_TARGET_DIR` at a shared folder from `setup` is possible, but two worktrees building at once then wait on its lock |

One lockfile per folder runs, in the order of the table (a folder with `pnpm-lock.yaml` and `package-lock.json` gets pnpm). Other managers (Poetry, Bundler, Composer, Gradle) have no built-in step: add their install to `setup`; their caches are shared the same way.

The sandbox lets the installs write to the caches they need (`sandbox/toolchain.go`: pnpm's store and metadata cache, `~/.npm`, Yarn's cache, `~/.cache` for uv), and to the worktree, and nowhere else.

## What runs it

The client, in three places, with the same code (`worktree.RunSetup`, `server/worktreesetup.go`):

- **A session created in a worktree** (`CreateSession` with `worktree`): the setup starts as the session opens and runs in the background; the agent's first turn waits for it (`agent.AddStartGate`, as SessionStart hooks do).
- **`EnterWorktree`**: the tool runs it after moving the session and before it answers; its result says what ran.
- **An isolated subagent** (`isolation: "worktree"`): it runs in the background; the agent's first turn waits for it.

Each step runs through `tools.Shell.SetupCommand`: the Bash tool's shell, environment and snapshot, under the session's OS sandbox with the policy for commands, with **the worktree as its only working directory**. The main checkout is not writable from a setup, even though the worktree sits inside it. With the sandbox off (the user turned it off) commands run unconfined, as the model's do; the built-in installs still never write outside the worktree and the caches, but a `setup` command can.

Who can write the file decides what runs, as with a `package.json` script the user would run anyway: a setup is the project's own commands, run in a checkout of the project, inside the sandbox. The model cannot write `.harness/` (the sandbox and the permission engine protect it), so it cannot plant a setup for its next worktree.

Steps run in order. A failed or timed-out step stops nothing: the next steps run, the session works in the worktree either way, and the report says what failed. A step is stopped when the session closes (status canceled), and with `EnterWorktree` when its turn is interrupted.

## What the user sees

`WorktreeSetupUpdated` (`proto/harness/agent/v1/worktree_setup.proto`, SessionEvent 430): one `WorktreeSetup` per worktree, published again with the same id as it changes. A change of a step's status is durable (kept for `Subscribe` replays); output in between is ephemeral and throttled to one update a second. A step keeps the last 16 KB of its output. The UI draws one card in the transcript (`components/transcript/WorktreeSetupCard.vue`): the steps with their status and time, the output of a running or failed step open.

## The suggestion

A project with lockfiles that `shareDependencies` would install, and no `.harness/worktree.json`, gets a card on its first worktree: the installs it would run, the path of the file, **Set up** and **Not for this project**.

- **Set up** writes `{"shareDependencies": true}` (cloning what the main checkout has installed, installing the rest) to `.harness/worktree.json` in the checkout the worktree came from, never over an existing file (if one appeared meanwhile, that one runs), and runs it in that worktree at once. It is project configuration: commit it and every clone's worktrees get it. Worktrees made after it run it by themselves.
- **Not for this project** stops the offer for that repository on this device (`~/.harness/worktree-setup-dismissed.json`, keyed by the main checkout's path). Nothing is written into the project: the choice not to set up is nobody else's.
- No answer: the next session's first worktree offers it again. While one offer waits, further worktrees of the same repository in that session don't add cards; they are not set up if the offer is accepted later.

`AnswerWorktreeSetup` carries the answer; `NOT_FOUND` when the offer is no longer waiting (answered, or the session reopened).

## What the agent is told

The `worktree_setup` reminder (`packages/core/prompts/reminders/worktree_setup.md`, with a fallback in the client), on the first request in the worktree, or in the `EnterWorktree` result instead:

- after a run: every step with how it ended and its time, the last 2000 bytes of a failed step's output, and that what succeeded is in place and should not be run again;
- after an offer: that nothing was installed, the installs offered, that a note follows if the user accepts, and that if the task needs dependencies before then it should install them in the worktree the same way and never link another checkout's;
- when the user accepts later: the same as after a run, on the agent's next request (it does not start a turn);
- a file that cannot be used: why, and that nothing was set up.

The reminder is written to the transcript like every reminder, so a resumed session still knows the setup ran.

## Tests

On temporary repositories and fixtures; no network and no real package registry.

- `worktree/setup_test.go`: `TestCloneFolder` clones a folder in a temporary directory (skipped where the file system can't clone; on the Mac's APFS it runs) and checks contents, relative and re-pointed absolute links, that writing the clone leaves the original as it was, and that it never clones over a folder; `TestDependencyStepsClone` checks clone, reconcile-only-when-the-lockfiles-differ, no clone without an installed source or for a virtualenv, `clone` folders, and the fallback to the install. Also the file format and its errors, never replacing a file, lockfile detection and the command per manager, planning, the runner (order, a failure that stops nothing, a timeout, a folder that leaves the worktree, cancelation, throttled progress), and `TestShareDependenciesPnpm`: a real pnpm install, offline, of a `file:` dependency into a worktree with a store of the test's own, once installed from the store and once cloned from the main checkout, checking that the worktree's `node_modules` is its own and that writes into it (a vite cache, a changed file) leave the main checkout's unchanged. It skips without pnpm.
- `server/worktreesetup_test.go`: a setup that runs before the first turn and reaches the first request; a suggestion accepted (with a fake `pnpm` on `PATH`), the file written, the reminder after it, and the next worktree set up without asking; a suggestion dismissed and not offered again; `EnterWorktree`; an isolated subagent; a timeout.
- `server/worktreesetup_darwin_test.go`: under Seatbelt, a setup step cannot write into the main checkout; the default setup clones the main checkout's `node_modules` into a session's worktree.
- `packages/ui/test/reducer.test.ts`: one card per worktree from the offer to the end of the run.

What only fixtures have seen: pnpm against a real registry with a warm store (expected to take seconds; the offline fixture took about ten on a cold store), bun, Yarn, npm and uv installs (only their commands are tested), Linux's sandbox around a setup, and reflinks on btrfs or XFS (only the fallback path is exercised off APFS).
