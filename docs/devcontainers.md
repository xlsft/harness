# xlsft\`s harness in a dev container

The agent's tools run where `harness-client` runs. Put the client inside a [dev container](https://containers.dev) and the
agent works in the container, with its toolchain and its files, while the model calls go through your server as everywhere
else. This page covers installing it, signing in, reaching it from the app or the browser on your machine, and what the
sandbox does in a container.

## Install it with the feature

`deploy/devcontainer/harness-client` is a [dev container feature](https://containers.dev/implementors/features/). It isn't
published to a registry yet, so copy the folder next to your `devcontainer.json` and refer to it by path:

```text
.devcontainer/
  devcontainer.json
  harness-client/        # a copy of deploy/devcontainer/harness-client
```

```jsonc
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": {
    "./harness-client": {
      "serverUrl": "https://harness.example.com",
      "version": "latest"
    }
  },
  // Credentials and sessions survive a rebuild (see below).
  "mounts": ["source=harness-home,target=/home/vscode/.harness,type=volume"],
  // The client's agent endpoint, for the app or the web UI on your machine (see below).
  "forwardPorts": [7801]
}
```

| Option | Default | What |
|---|---|---|
| `version` | `latest` | A release, `0.2.0`, or the newest stable one |
| `serverUrl` | | Your server, written to the container's machine policy: `harness-client login` then needs no URL |
| `mirror` | | An internal update mirror ([docs/enterprise.md](enterprise.md#an-internal-mirror-and-offline-installs)) to install from instead of GitHub; an `http(s)` one is also written as `updateMirrorUrl` |
| `disableUpdates` | `true` | `harness-client update` refuses in the container: rebuild the image to update |

It installs `/usr/local/bin/harness-client` for the container's architecture (amd64 or arm64), checks the archive against the
release's `checksums.txt`, and, if `minisign` is in the image, `checksums.txt` against the release key, as
`harness-client update` does. It writes `/etc/harness/settings.d/00-devcontainer.json`, the system folder of the client's
machine policy on Linux ([docs/enterprise.md](enterprise.md#what-a-machine-reads)), and sets `HARNESS_KEYRING=0`: a
container has no keychain, so credentials go to `~/.harness/credentials.json`. It needs `curl` and `tar` in the image.

**Linux releases of `harness-client` don't exist yet** (releases are macOS only for now, [docs/release.md](release.md)): until
they do, the feature stops with a message saying so. Meanwhile, build the client into the image from source, with Go in it:

```jsonc
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": { "ghcr.io/devcontainers/features/go:1": {} },
  "postCreateCommand": "git clone --depth 1 https://github.com/xlsft/harness /tmp/harness && sudo env PATH=$PATH go -C /tmp/harness/packages/client build -o /usr/local/bin/harness-client ./cmd/harness-client && sudo mkdir -p /etc/harness/settings.d && echo '{\"serverUrl\": \"https://harness.example.com\"}' | sudo tee /etc/harness/settings.d/00-devcontainer.json",
  "containerEnv": { "HARNESS_KEYRING": "0" }
}
```

A client built this way has no release key, so `harness-client update` refuses: rebuild to update.

## Signing in

Pick one.

- **Sign in inside the container**: `harness-client login`. It prints a link and a code; open the link on your machine,
  confirm the code, done. The credentials stay in `~/.harness`, so mount a volume there (above) or you sign in again after
  every rebuild.
- **A device token from your machine's environment**, for containers you throw away often. Make one with
  `harness-client ci login https://harness.example.com` on your machine (it prints a device token, which is a secret), keep it in your shell's environment or a secret
  store, and pass it through:

  ```jsonc
  "remoteEnv": {
    "HARNESS_SERVER_URL": "https://harness.example.com",
    "HARNESS_DEVICE_TOKEN": "${localEnv:HARNESS_DEVICE_TOKEN}"
  }
  ```

  The client then keeps it in memory only. Don't put the token itself in `devcontainer.json`: it's committed.

Provider keys never reach the container either way: the server holds them and makes the model calls.

## Reaching the client from your machine

The agent runs in the container; the interface runs on your machine, in the desktop app or in the browser on your server's
web UI. Start the client's agent endpoint in the container:

```bash
harness-client serve --listen 127.0.0.1:7801 --cors-origin https://harness.example.com
```

It prints a bearer token (also in `~/.harness/agent-token`). Then:

- **VS Code, the dev containers CLI, Codespaces**: they forward a port from the container's loopback to yours. With
  `"forwardPorts": [7801]`, `http://127.0.0.1:7801` on your machine is the client; enter it and the token where your server's web
  page asks for a client. The listener stays on loopback, so nothing else on the container's network reaches it.
- **Plain `docker run -p`**: Docker publishes the container's network interface, not its loopback. Listen on all of them with
  `--listen 0.0.0.0:7801 --allow-non-loopback` and publish it only to your machine's loopback: `-p 127.0.0.1:7801:7801`. The
  token is then the only protection, as the client warns.

`--cors-origin` is the page that talks to the client: your server's address for its web UI. The client refuses other
origins, and requests whose `Host` isn't loopback or the bind address. A browser on an HTTPS page talks to plain HTTP only on
loopback, which a forwarded port is; Safari may refuse even that ([deploy/README.md](../deploy/README.md#web-ui)).

Dev servers the agent starts in the container (previews) are ports like any other: forward them the same way.

## The sandbox in a container

`harness-client` runs the model's commands under an operating system sandbox where it has one: Seatbelt on macOS today. On
Linux the driver is being added; it relies on kernel features (an unprivileged sandboxing LSM such as Landlock, or user
namespaces as bubblewrap uses them) that a container runtime may or may not let a container use. Docker's default seccomp
profile and AppArmor, a missing `CAP_SYS_ADMIN`, or a host kernel built without them each switch a mechanism off, and what's
left differs by host. The design notes in `docs/design/platforms.md` will say which mechanisms the Linux driver uses once that
lands.

What the client does about it is the same everywhere:

- **Sandbox on, and it works here**: commands run confined, as on a workstation.
- **Sandbox on, and it can't be applied here**: commands run unconfined, and the session says so (Settings → Sandbox shows it
  as unavailable, with the reason).
- **Your administrator requires it** (`sandbox.enabled` in a policy): commands are refused rather than run unconfined.

So in a container you have two layers to choose from. The container is a boundary of its own: the agent sees the files you
mounted and the network the container has, and nothing else of your machine. That is often enough for a dev container, and
then leaving the client's sandbox off in it (Settings → Sandbox) is a reasonable choice, as long as no policy requires it.
If you want the client's sandbox inside the container too, give the container what the driver needs (for example a seccomp
profile that allows the namespace calls) only after reading what that opens up, and check Settings → Sandbox afterwards.

Keep secrets out of the container you don't want the agent to use: everything the container user can read, a command can.
Mount only the project, and pass credentials for the agent's own tools (`gh`, cloud CLIs) deliberately.
