# Linux and Windows

What xlsft\`s harness does on platforms other than macOS, why, and what shipping them needs. Releases stay macOS for now; the CI bot and server hosts already run the client on Linux, usually in a container.

## The Linux sandbox

The driver is in `packages/client/sandbox` and takes the same `Policy` as Seatbelt. It picks one of two backends when the client first needs it, by trying them on this machine rather than by reading version numbers:

| Backend | Used when | Typical place |
|---|---|---|
| `bubblewrap` | `bwrap` is installed and a trial run gets its namespaces | desktops, VMs, privileged or user-namespace containers |
| `landlock` | no working `bwrap`, and a trial run shows Landlock and seccomp enforcing | a default Docker or Kubernetes container, whose seccomp profile refuses new namespaces |
| unsupported | neither | old kernels, gVisor, WSL 1 |

Unsupported is not off: Settings shows the reason, commands run with the user's full rights only because the user is told so, and with a policy document that requires the sandbox they are refused (`State.Refusing`). A failure at run time (the helper cannot apply the policy) never falls through to an unconfined command: the helper exits 126 and says why.

### What holds where

| Policy | Seatbelt | `bubblewrap` | `landlock` |
|---|---|---|---|
| Writes only in the roots | kernel | kernel: read-only root with the roots bound over it | kernel |
| Credential stores closed for reading | kernel | kernel: masked by a tmpfs or an unreadable file | kernel: every other path is allowed one by one, as of the moment the command starts |
| Protected paths inside a root that exist (`.git/config`, hooks, `.mcp.json`, agent folders, `~/.zshrc` if home is a root) | kernel | kernel: read-only bind mounts, also found below the root to a small depth | **guard only** |
| Protected names that do not exist yet, at any depth | kernel, by pattern | **guard only**, and only at a working folder's top level | **guard only**, same |
| Network only through the proxy | kernel, by address | kernel: an empty network namespace, with the proxy brought in over a unix socket | **by port only**, kernel 6.7 and later; see below |
| Direct DNS, UDP, raw sockets | kernel | nothing to reach in the namespace | kernel (seccomp) |
| Sockets of other programs (`docker.sock`, D-Bus, ssh-agent) | kernel | masked: `/run` is empty, sockets found in `/tmp` are covered when the command starts | kernel: seccomp refuses `socket(AF_UNIX)`, so a command's own unix sockets fail too; `socketpair` works |
| Other processes | kernel | kernel: own PID namespace | kernel: Landlock stops ptrace outward; signals only from kernel 6.12 |
| Setuid programs | do not start | start, without privileges | start, without privileges |
| Local ports (`allowLocalhost`) | loopback, shared with the machine | a loopback **of the command's own** | listen anywhere; connect to ports 1024 and up |
| Preview servers | loopback and the proxy | share the machine's network; direct connections limited by port from kernel 6.7, not at all before | same as for commands |

### Decisions

**Backends are chosen by a trial, not by version.** `bwrap --version` and the kernel release say nothing about a container's seccomp profile, AppArmor's `apparmor_restrict_unprivileged_userns` on Ubuntu 24.04, or gVisor implementing part of a call. The trial runs the real helper under the real filter and checks that a forbidden write and a forbidden socket are refused. It runs once per process.

**A helper instead of a C shim or a new dependency.** Seccomp and Landlock apply between fork and exec, so a process has to do it. The client's own binary does: `Command` puts `<self> __harness-sandbox-stage <spec> <argv...>` in front of the command, and an `init` in package sandbox recognizes that and never returns to `main`. An `init` and not a line in `main` because the package is a library and every binary that embeds it, a test binary included, has to work. The helper uses raw system calls through the standard library; `go.mod` does not change. Its arguments can only add restrictions, so nothing is gained by calling it directly.

**The rendering is pure and untagged.** The `bwrap` arguments, the Landlock rules and the BPF program are functions of the policy with the file system passed in, like `SeatbeltProfile`. Their tests run on every platform, with a small BPF interpreter for the filter. Only entering the kernel is tagged `linux`.

**Landlock cannot express the protected paths, so a guard stands behind it.** A Landlock rule on a folder grants the same rights to everything below it, with no way to take any back, so a working folder a command can create files in is a working folder whose `.git/hooks` and `.harness/settings.local.json` it can create files in. Those files are what turns a sandboxed command into an unconfined one on the next `git` or the next session, so they cannot simply be left open. The guard (`guard.go`) records them when a command starts and looks again when it ends. What changed is moved to `<scratch>/sandbox-rejected`, the original is put back, and `Diagnose` tells the model, exactly as for a kernel refusal. Nothing is deleted, because the guard cannot tell a command's edit from one the user made elsewhere while that command ran.

The guard is detection, not a boundary. A process left running can write after its command ended; a tab of `TerminalRun` and a preview server have no end to look at. It covers `bubblewrap` too, for names that do not exist yet: a bind mount needs something to bind over, and creating placeholders would litter the user's project.

**`landlock` limits the network by port, since it can do nothing else.** Landlock's TCP rules name a port and no address, and nothing else available without privileges looks at addresses. So a command may connect to the proxy's port on any host, and with `allowLocalhost` on (the default), since a test's own server has a port nobody can name in advance, to any port from 1024 up on any host. That stops the common traffic (80, 443, 22, 25, DNS, UDP, ICMP), and it does not stop a determined command from sending data to a server of its own on a high port. Where that matters, run the container so that `bwrap` works, or give it no route out. Settings shows this as a limit of the machine. Below kernel 6.7 there are no TCP rules: the filter refuses internet sockets altogether, so commands have no network and no localhost, and the note says so. This errs toward a command that fails with an explanation.

**A private loopback under `bubblewrap`.** An empty network namespace is what makes the proxy the only way out, by address. The price is that `allowLocalhost` means the command's own loopback: a test's server works, a database on the machine or a server started by an earlier command does not. `Diagnose` recognizes a refused connection to localhost and says so. With `allowLocalhost` off and kernel 6.7 or later, Landlock also refuses binding; below that the private loopback reaches nothing anyway. Sharing one namespace across a session's commands is possible follow-up work; it needs a process per session that holds the namespace.

**The proxy is reached over a unix socket under `bubblewrap`.** `Session` serves the same proxy on a unix socket in a private folder, bound at `/run/harness-sandbox/proxy.sock`. Inside the namespace a small forwarder listens on `127.0.0.1:<port>` and relays to it, so `HTTP_PROXY` is the same value everywhere. The forwarder dies with the PID namespace; it cannot carry the filter, which belongs to one thread of a Go program, so it is not dumpable, and the command's filter refuses ptrace.

**`/tmp` is the real one.** A private `/tmp` would hide files from the file tools, which run outside, and the model commonly writes a file there with one and reads it with the other. So the sockets found in it when the command starts are masked, and a socket made later by something else stays reachable under `bubblewrap`.

**Seccomp allows by default, with a list of refusals.** A default-deny list of system calls breaks with every new kernel and libc. Refused: other architectures and x32 (the process is killed: it could otherwise reach numbers the list does not name), namespaces and mounts, ptrace and process memory, the kernel keyring, BPF, `io_uring` (its operations bypass this filter), module loading and the like, file handles, `TIOCSTI` (it types into the terminal), and `clone3` (`ENOSYS`, because the filter cannot read its flags; libc falls back to `clone`).

**Supported architectures are `amd64` and `arm64`**, the ones CI builds. On others the driver reports unsupported, since the filter needs a table of system call numbers.

### Not closed on Linux

- Protected names at depth when they do not exist yet, e.g. a new `sub/.harness/settings.local.json`. The client only reads settings from the session's folder and above, so this matters when a session later opens in `sub`.
- A hard link to a protected file inside a root, under `landlock`.
- Anything written after the end of the command that started it (see the guard).
- Abstract unix sockets below kernel 6.12 for preview servers under `bubblewrap`.
- `run_outside_sandbox` still needs the user; that is unchanged. WSL 2 is plain Linux here and takes the same path; it has not been tried.

## Windows

There is no sandbox driver: `GetSandboxStatus` reports unsupported with the reason, commands run with the user's rights, and a policy document that requires the sandbox refuses them. Restricted tokens and job objects can limit writes but need a helper and privileges no installer sets up yet, and that design is still open.

The `PowerShell` tool (`tools/powershell.go`) is offered to the model on Windows, when `pwsh` or `powershell.exe` is found. Its decisions:

- **It has its own permission check.** It is not `CheckAs: "Bash"`. The check for `Bash` reads POSIX shell and PowerShell is not that: `echo (Remove-Item x)` looks like an `echo`. So a `Bash(...)` rule, a read-only allowlist or an auto-mode fact never approves a PowerShell line; the tool asks, unless a rule names `PowerShell` itself. A session that disables `Bash` loses this tool too.
- **It shares the shell's state.** The working directory, background tasks, timeouts and output limits are those of `Bash`, through the same `Shell`.
- **No profile, no execution policy override.** `-NoProfile` for the same reason the sandbox does not trust what a project can write; the execution policy is the machine's, and `-Command` does not run into it.

`Bash` on Windows stays what it was: it finds `bash` on `PATH` or fails with a message. Killing a command on Windows still kills only the direct child (`proc_windows.go`).

## What Linux and Windows releases need from `release.yml`

Nothing here changes the workflow yet. What it takes (#5):

1. **`.goreleaser.yaml`:** add `linux` and `windows` to `goos`. Keep `CGO_ENABLED=0`: the sandbox helper uses no cgo, and the static binary is also what runs inside `bwrap`, whatever libc the image has. Windows archives are `.zip` (`format_overrides`).
2. **`client-archives` in `release.yml`:** its artifact paths name `dist/*.tar.gz`; add `dist/*.zip`. `.sbom.json` already follows the archives.
3. **`scripts/release/assemble.sh` and `harness-client update`:** `selfupdate.ArchiveName` already has the platform in it, and `.tar.gz` fixed: Windows needs its `.zip` there and in the unpacking, and a way to replace a running `.exe` (rename it aside first). `checksums.txt` has to cover the new files.
4. **`docs/release.md`:** the table of platforms, and that the Linux sandbox wants `bubblewrap` (a package dependency in `.deb`/`.rpm`, a note for tar archives).
5. **The terminal frontend (`packages/cli`)** has no goreleaser entry on any platform; it is a separate decision.
6. **Desktop (`desktop.yml`):** Tauri bundles for Linux and Windows have config but no job. They need runners of those platforms and Windows code signing, which has no secret yet. The sidecar client it bundles has to be built per target.
7. **A release gate:** `scripts/release/ci-green.sh` already requires `client`, and the `sandbox-linux` and `powershell-windows` jobs in `client.yml` are part of it, so the sandbox has to be green on the commit a release comes from.
8. **Image:** `deploy/Dockerfile` ships the server, not the client. If a client image ever follows, it should contain `bubblewrap` and say which capabilities or seccomp profile give it the stronger backend.

Windows also wants a package channel (WinGet) later; nothing depends on it.
