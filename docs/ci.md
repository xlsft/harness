# CI: review bot, @mentions and headless runs

xlsft\`s harness runs on a CI runner through `harness-client`, headless. Three things come out of that:

- **Review bot.** `harness-client ci review` reviews a pull request and posts findings as review comments, on the lines they are about.
- **@mention trigger.** `harness-client ci mention` answers a comment that calls the bot, from someone with write access.
- **Any prompt.** `harness-client -p` with the flags in [protocol/headless.md](protocol/headless.md).

The model calls go through your harness server, as everywhere else: providers and keys stay there.

## Credentials

```bash
harness-client ci login https://harness.example.com
```

This registers a device for CI through the usual device flow and prints its token, once. Store it as the CI secret `HARNESS_DEVICE_TOKEN`, and revoke it under Settings > Account when it is no longer needed. Nothing is stored on the machine you run it on.

The agent runs as whoever confirmed that device, with that account's models and limits.

## GitHub Actions

Copy [`ci/github/examples/harness-review.yml`](../ci/github/examples/harness-review.yml) and [`harness-mention.yml`](../ci/github/examples/harness-mention.yml) to `.github/workflows/`, then set the repository variable `HARNESS_SERVER_URL` and the secret `HARNESS_DEVICE_TOKEN`. The default `GITHUB_TOKEN` posts the comments; the examples give it the permissions it needs.

[`ci/github/action.yml`](../ci/github/action.yml) takes `mode` (`review`, `mention` or `run`), `server-url`, `device-token`, and optionally `model`, `max-budget-usd`, `max-turns`, `allowed-tools`, `prompt`, `trigger`, `fail-on`, `json-schema`, `github-token` and `harness-client`. It builds `harness-client` from its own source with Go, because releases do not carry a Linux binary yet; point `harness-client` at a binary on the runner to skip that.

```yaml
- uses: xlsft/harness/ci/github@<commit>
  with:
    mode: run
    server-url: ${{ vars.HARNESS_SERVER_URL }}
    device-token: ${{ secrets.HARNESS_DEVICE_TOKEN }}
    prompt: Write the release notes for this tag.
    allowed-tools: |
      Bash(git log *)
    max-budget-usd: "1"
```

## GitLab CI

```yaml
include:
  - remote: https://raw.githubusercontent.com/xlsft/harness/<commit>/ci/gitlab/harness.gitlab-ci.yml
```

Set the CI/CD variables `HARNESS_SERVER_URL`, `HARNESS_DEVICE_TOKEN` (masked) and `GITLAB_TOKEN` (masked: a project access token with the `api` scope, which posts the comments). The image gets `git` and `glab` from Alpine's packages; set `HARNESS_CLIENT` to a binary in your own image to skip the build. `HARNESS_REVIEW_FLAGS` passes extra flags to the review, such as `--fail-on high`.

The review runs in merge request pipelines. GitLab starts no pipeline for a comment by itself, so a project webhook does it:

1. Settings > CI/CD > Pipeline trigger tokens: add a token.
2. Settings > Webhooks: add `https://<gitlab-host>/api/v4/projects/<project-id>/ref/<default-branch>/trigger/pipeline?token=<token>`, for **Comments**.

GitLab then keeps the comment in the file `TRIGGER_PAYLOAD` names, and the `harness-mention` job reads it.

## What the bot can do

By default the agent reads the working tree and nothing else: no shell, no edits, nothing outside the folder. `--allowed-tools` (the action's `allowed-tools`) adds to that, for example `Bash(go test *)`. Whatever you add runs on code a pull request may have changed, so add little.

**Secrets.** The tokens stay with `harness-client` and the `gh` or `glab` it starts. They are taken out of the environment before the agent runs, so tools it starts do not inherit them. On Linux a process of the same user can still read the runner's original environment under `/proc`, which is one more reason to keep `allowed-tools` narrow.

**The repository's configuration is not used.** The agent runs as in an untrusted folder, without the repository's settings, hooks and MCP servers, because a pull request can change those files.

**Who can call the bot.** Only someone with write access: on GitHub `author_association` of `OWNER`, `MEMBER` or `COLLABORATOR`, on GitLab Developer or above, asked through the API. Everyone else's mention ends the job quietly. So do bots, the bot's own comments (they end with a hidden marker), and calls inside code or quotes.

**Forks.** Keep the review on `pull_request`, which gives forks no secrets: the bot then simply does not run for them. Never use `pull_request_target` with a checkout of the pull request's code.

**What people write is data.** Titles, descriptions, diffs and other people's comments reach the model in a marked block, with no authority. This lowers the risk of a prompt injection and does not remove it, so set `max-budget-usd` and read the bot's comments as you would read a stranger's.

## Reviews

- A finding goes on its line when the diff shows that line; otherwise it goes into the summary, so none is lost. So do those past `--max-comments`, 25 by default.
- A later push does not repeat findings: each comment carries a hidden fingerprint of its path and title.
- With nothing new to say, nothing is posted.
- Drafts are skipped unless `--include-drafts` is set.
- `--fail-on high|medium|low` fails the job on a finding that bad or worse. The review is otherwise a `COMMENT`: it never approves and never blocks by itself.
- `--prompt` tells the reviewer what your team cares about.

## Mentions on a session's pull request

Separately from CI, the server watches comments through its webhooks when a [code host connection](#connecting-a-code-host) exists. If a comment on a pull request linked to a session calls `@harness` (`HARNESS_VCS_MENTION` replaces the word; a GitHub App also answers to `@<app-slug>`) and its author has write access, the session gets it as an auto-fix job, whatever its auto-fix switches say. On GitLab this needs an access token on the connection to check the author.

## Connecting a code host

Settings > Code hosts shows what can reach GitHub and GitLab:

- **Your accounts**: the server's connections, and whether you authorized yours. Opening pull requests through the server needs this on GitHub.
- **On this machine**: `gh` and `glab`, whether they are installed and signed in. They cover whatever has no server connection. This never leaves the device.
- **Server connections** (admins): register a GitHub App in one click from a manifest, or add a GitLab instance with an OAuth application or an access token. Keys go to the server and are never shown again.

A server connection goes first when both cover a repository.
