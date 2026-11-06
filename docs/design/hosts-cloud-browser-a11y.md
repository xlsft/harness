# SSH hosts, cloud providers, model tuning, real Chrome, i18n and accessibility

Decisions behind this work, and how to continue it.

## Merging this branch

The string extraction touches almost every component, so it is applied last, by script, never merged by hand:

1. Revert the extraction commit (`git revert`, no history rewrite), merge `main`, resolve what is left, commit.
2. `cd packages/ui && node scripts/i18n-extract.mjs --apply`, restore `test/i18n.test.ts` if the revert removed it, `pnpm exec eslint --fix app`, `node scripts/i18n-extract.mjs`, commit.

`--check` fails on a stale catalog or plain text in a template, and `test/i18n.test.ts` runs it.

## SSH hosts (`packages/client/sshhost`, `ssh_hosts.proto`)

- The user's own `ssh` command, not a Go SSH client: their config, agent, ProxyJump and hardware keys come along, and no key or password passes through us (`BatchMode=yes`).
- `StrictHostKeyChecking=yes` always. A new key is learned with `accept-new` into a throwaway known_hosts with authentication limited to `none`, so nothing is offered to an unverified host. Trusting scans again and compares with the fingerprint the user saw. A changed key is only cleared by forgetting it on purpose.
- The token reaches the host's client over stdin, never argv. `HARNESS_EXIT_WITH_STDIN=1` makes it exit with the connection.
- The frontend talks to the host's client directly through the tunnel, and the window reloads to switch (`lib/ssh`, `stores/sessions.ts`). Nothing is proxied, so every screen works unchanged.
- Hosts and remembered keys live in `HARNESS_HOME/ssh`, device-local, never synced.
- Not exercised end to end: the reverse tunnel for a server on the local loopback, and uploading a signed release for another platform.

## Cloud providers (`packages/core/src/gateway/cloud`, `services/presets-cloud.ts`)

- Credentials are a layer over the existing adapters: `authorize()` runs after `buildRequest`. Only InvokeModel and Vertex's Claude route needed adapters of their own.
- A cloud provider's secret is kept in `cloudSecret`, never `apiKey`, so no adapter can send it as a key.
- The server's own identity (`ambient`) is for global providers only, refused both at validation and in `authorize`.
- Options that land in a host name are validated. A personal provider's endpoint always follows from them. A service account's `token_uri` is ignored.
- Prices are left out: these platforms price by region and endpoint.

## Speed and cache (`services/model-tiers.ts`)

- Computed from provider and model rather than stored, so a change needs no migration.
- Multipliers come from the vendors' pricing pages, read 2026-11-06. OpenAI's differ per model (gpt-5.5 2.5x, gpt-5-mini 1.8x, o3 1.75x), and some models lack a tier, so the table names models exactly. A model not in it has no tier rather than a guessed price.
- `ModelTuning` is a synced preference (`UserPreferences` field 26). Field 27 is unused. New sessions pick it up.

## Advisor (`tools/advisor.go`, `bootstrap/advisor.go`)

A client tool, not Anthropic's server tool, so it works with any provider and on platforms without that one. Tool-less request with a rendered copy of the session. Offered only when a model is chosen and available. Not in the fake bundle: tests that assume every bundle tool is offered would break, so advisor tests add it.

## Real Chrome (`browser/attach.go`, `real_browser.proto`)

- CDP attachment rather than an extension: it reads `DevToolsActivePort`, loopback only, and never starts Chrome.
- Only tabs this client opened are attached (popups of them follow). Nothing auto-attaches, so the user's tabs stay out of reach.
- Grants are enforced by pausing main-frame document requests (`Fetch.requestPaused`), not by checking tool input, so clicks, redirects and scripts are covered. Subframes go on: blocking them only breaks pages.
- Only the user can switch a session to their Chrome: no agent tool does it.
- The site question can only be answered on the session's machine; no relay command exists yet.

## i18n and accessibility

- English text is the key (`tt()`, `app/utils/i18n.ts`). A language applies on reload, so nothing needs to be reactive. No pack ships; `app/i18n/README.md` says how to add one.
- The function is `tt`, not `t`: `t` is a common local variable in components.
- Screen reader mode speaks moments of a session (`lib/a11y/announce.ts`), because streaming text into a live region is unusable.
- Color-blind themes are checked under simulated color vision in `test/a11y.test.ts`.

## Left to do

- Answering a real-Chrome site question from another device.
- Editing a cloud provider's options after connecting.
- A per-session speed toggle.
- Screen reader mode in the terminal frontend.
- A Russian pack.
