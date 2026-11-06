# Issues and Knowledge

How xlsft`s harness tracks issues: projects, boards, connections to external trackers, and how Knowledge reads from more than
one source. This file is the source of truth for the work and is kept current with every decision, so someone else can continue
from the branch. What is built and what comes next is in section 9.

The name: the product surface is **Issues** (sidebar, `/issues`, the `Issues` tool). In code and contracts an issue here is a
**card**, so it isn't taken for a provider's issue (`TrackerIssue`); "tracker" means an external one (`tracker` packages,
Settings > Trackers). Likewise **Knowledge** to users, `/knowledge` as the route.

## 1. Provider connections

A connection is one configured account on one tracker instance, managed like a model provider in **Settings > Trackers**:
connect, test, disable, remove. YouTrack is first. Jira, Linear and GitHub Issues follow by implementing the provider interface.

| | Personal, on the device | Admin, on the server |
|---|---|---|
| Who manages it | the user, per device | admins |
| Where the config lives | `HARNESS_HOME/tracker-connections.json` (0600), never synced | table `tracker_connection` |
| Where the secret lives | system keychain; a 0600 file only where there is none (`HARNESS_KEYRING=0`, headless) | AES-256-GCM (HKDF purpose `tracker-secrets`, AAD `<id>:secrets`) like other provider keys |
| Where requests go from | the client: instances are often behind a VPN the server can't see | the server. Secrets never reach a client |

The server knows a device connection only as an opaque id in a stage or a project. It never learns its URL, login or secret.

### Auth methods, checked against the YouTrack documentation

The user picks one per connection.

- **Token.** A YouTrack permanent token (`perm:...`), sent as `Authorization: Bearer`. Any user with *Update Self* creates one
  under Profile > Account Security; it carries that user's permissions. JetBrains recommends it for non-interactive integrations.
  The default in the form.
- **OAuth.** Authorization Code with PKCE against Hub (`<hub>/api/rest/oauth2/auth` and `/token`), `access_type=offline` for a
  refresh token. YouTrack has no dynamic client registration, so an admin of the instance creates an OAuth client (*Low-level Admin
  Write*); the form asks for the client id, and the secret for a confidential client. Device connections redirect to
  `http://127.0.0.1:<port>/callback` (YouTrack accepts a registered loopback IP URI). Admin connections redirect to the server's
  `/api/tracker/oauth/callback`; the state is sealed with the server's key, so nothing is stored until it comes back.
- **Login and password.** There is no documented REST method: no basic auth, and JetBrains marks the Resource Owner flow
  deprecated. The method kept is ymcp's: post the Hub login form as the web UI does, then take a token from the implicit flow,
  renewed from the Hub session. It fails with two-factor authentication and depends on undocumented Hub behaviour, which the form
  says and why it is not the default.

### Provider interface

`packages/client/tracker/provider.go`: `Capabilities`, `Test`, `Search`, `Get`, `Create`, `Update`, `Comment`, `Fields`, `Projects`;
`KnowledgeProvider` adds `SearchArticles`, `ListArticles`, `GetArticle`. `Capabilities` says what the provider can do and what the
form should say. Failures classify as `ErrNotFound`, `ErrAuth`, `ErrForbidden` or `ErrTransient`, and the sync decides on those.

**Decision: the provider exists twice.** The server is TypeScript and admin requests must come from it, so
`packages/core/src/tracker/provider.ts` and `youtrack.ts` mirror the Go behaviour, fallbacks included. The two are held together by
tests on fakes of the same shape. A Go sidecar on the server was rejected: nothing else in core runs Go.

### Working without admin rights

From ymcp, in both implementations unless noted:

- **Field schema.** `admin/projects/<id>/customFields` answers 403 or an empty list to a regular user; then the schema is merged
  from `projectCustomField` of the 20 most recently updated issues. Cached for 5 minutes; a value a cached schema rejects is checked
  against a fresh one.
- **Groups** (Go only; no flow needs them yet). `groups` needs admin rights; the fallback is Hub, and when that shows none, a clear
  error.
- **Comments.** Deleted comments stay in the REST list without text; reads filter them out.
- Non-JSON answers (a proxy, a VPN page) become a readable error.

`packages/client/tracker/youtrack` is adapted from `internal/youtrack` of `xlsft/ymcp` (MIT); the notice is in `LICENSE.ymcp` there
and in the file headers. Its MCP surface, TUI and pipelines are not copied.

## 2. Projects

Everything belongs to a project. A project has a **ticker**, name, logo, description and color, is **internal or a provider
project**, and is **personal or global**.

- **Provider project**: maps onto a provider's project (connection and key). The provider's values are defaults, local metadata
  overrides them; clearing a local value goes back to the provider's. The provider's values are a snapshot from when it was mapped
  (see 9).
- **A provider project on a device connection can only be personal.**
- **Tickers** are 2 to 10 upper case letters and digits, starting with a letter; unique among global projects, and among one user's
  personal projects. A personal and a global project may share one: the caller's own wins.
- **`EnsureDefaultProject`** makes "My issues" (`MY`) with a personal board when someone has no project of their own.

### Sharing, enforced on the server

Shared with **users** (the form takes an email), **groups** (a name), **roles** (`admin`, `user`) or **everyone on the server**, at
**view**, **edit** or **manage**. The highest level that applies wins. `services/projects.ts` `levels()` is the only source, and
everything else asks it.

| | View | Edit | Manage |
|---|---|---|---|
| See the project, its shared boards and their issues | yes | yes | yes |
| Create, edit, move, comment, relate, retry | | yes | yes |
| Change the project, its shares, its shared boards | | | yes |

- The owner of a personal project holds manage. **An admin holds nothing on someone's personal project unless it is shared with
  them**, as any user. Admins hold manage on global projects.
- A new global project is shared with everyone at edit unless the creator says otherwise. Only admins create global projects.
- What the caller holds no level on answers `NOT_FOUND`. Who else a project is shared with shows only to those with manage.
- Changing shares is audited.

### Folder links

A working directory links to a project in `HARNESS_HOME/tracker-folder-projects.json`, **device-local, never synced**: paths belong
to a machine. A folder under a linked one belongs to the same project (a worktree, a subfolder); the nearest link wins.

- The `Issues` tool defaults to the session folder's project, and `projects` marks it.
- `PrepareIssueSession` starts an issue's session in the folder linked to its project; with none, the user picks one.

### How boards and cards relate to projects

**Decision.** A card belongs to exactly one project and one board; a board belongs to exactly one project. So a card's number comes
from its project, and what you can see of it follows from its board.

- **Key**: `ABC-12` from the ticker; a linked provider issue keeps its own id as its key. Numbers never come back after a delete.
  Renaming a ticker renames the keys; old ones don't redirect.
- A linked card still has a project number, used only when it is unlinked.

## 3. Boards

We have **internal boards** only, and they are configured.

- **Scope**: `shared` or `personal`, and that is the only distinction. A shared board is seen by those with a level on the project,
  uses admin connections only, and is configured with manage. A personal board is its owner's alone, whatever anyone's role or level,
  and can use device or admin connections. It needs a level on the project, at least view.
- **A personal project's first board is personal**, on purpose: sharing a project does not expose what was on your board. To work
  together, add a shared board; the form says so.
- A board is an ordered list of **stages**. A stage has zero or more **inputs** and **outputs**, at most one of each per connection.
- Cards are internal, optionally linked to one external issue. A card without an issue just works.

### 3.1 Data model (`packages/core/src/db/schema.ts`)

`tracker_connection`; `project`, `project_share`; `board`, `board_stage`, `board_stage_input`, `board_stage_output`; `board_card`,
`board_card_comment`, `board_card_activity`, `board_card_relation`, `board_card_output` (one application of an output, with what to
apply copied at the moment of the move); `board_input_seen` (the "once" ledger); `saved_view`. The old `tracker_task` is dropped by
migration: there was no data to preserve.

### 3.2 Inputs

An input pulls issues matching a query, in the provider's own syntax, into its stage as cards, **once per board**. The ledger row
goes in with the card and outlives it.

- **Moving a card out does not bring it back.** Neither does deleting it, nor the issue changing and matching again.
- An issue already on the board by another input or a manual link is not pulled twice.
- **Deliberately no re-entry feature.** To get one back, link it by hand: `LinkCard` ignores the ledger.
- A poll takes up to 200 issues; the ledger lets the next one go on from there, for queries the provider orders stably.
- Queries are tested from the form against the live connection.

### 3.3 Outputs

Entering a stage with an output for the card's connection creates a `board_card_output` in the same transaction as the move. An
output sets fields (`Stage: Review`) and may post a comment.

- Only a real change of stage fires, and only the output of the connection the card is linked to.
- A card pulled in by an input does **not** fire the output of the stage it lands in: nothing moved it there.
- Leaving a stage supersedes outputs still pending or failed there, so a quick drag through applies only where the card ends up. One
  already running finishes.
- **Failures are visible on the card, never silent.** A transient error retries after 1, 5 and 15 minutes and shows `pending` with the
  error; a permanent one, or the last retry, stays `failed` until the user retries (button, `retry` in the tool). A retry applies what
  the move copied, not what the stage says now.
- A pending output on a device connection says it waits for the device.

### 3.4 Where it runs

**Work runs where the credential lives.**

| | Admin connection | Device connection |
|---|---|---|
| Shared board | server | not allowed |
| Personal board | **server** | a running client of that device |

**Decision to check:** a personal board's stage on an admin connection is polled by the server, not by a client. The credential
can't leave the server, so a client could only ask the server anyway, and this way it works with no client running.

- **Server**: a tick every 30 seconds (`services/board-sync.ts`). Due work is claimed with a lease in one conditional UPDATE, so
  several replicas never double up and a crashed worker's lease runs out. A move into a stage with an output wakes it at once.
  `HARNESS_BOARD_SYNC=off` turns it off.
- **Client**: a worker in the long-running client ticks every 30 seconds, and `SyncLocalTrackers` runs it at once after a move in the
  UI. `ClaimDeviceWork` with its enabled connections answers with due work **of that user's personal boards only**, and
  `ReportDeviceWork` is validated against the owner.
- **Interval**: per input, default 300 seconds, minimum 60, maximum a day. A failed poll waits the same interval, and `last_error`
  shows on the stage. Sync now on a board polls at once.
- **Device absent**: nothing polls. The stage shows its last poll and the form says the connection is on another device.

### 3.5 External changes

- **Title**: follows the issue's summary until someone edits it on the card.
- **Description, fields, comments**: never copied; `get` reads them live.
- **State changes outside do not move cards.** The board is ours; an input is a door, not a mirror. An issue that stops matching stays
  where it is.
- **Looking again**: up to 50 linked cards a cycle, each at most every 5 minutes, oldest first. Not found sets `missing`: the card stays
  and says so, and outputs fail visibly. If it comes back, `linked` again. Auth and network errors never mark a card missing.
- **Connection removed**: its inputs and outputs go, pending outputs stop, cards keep their text as `disconnected`. Device connections
  do this through `ForgetDeviceConnection`, told before the local delete so a failure can be repeated.
- **Connection disabled**: polling stops and says so; an output due on it fails visibly and can be retried after enabling.

### 3.6 Two people at once

`MoveCard` carries the `version` the mover saw. A stale one answers `FAILED_PRECONDITION` with nothing changed: the first move wins,
only its output fires, and the UI reloads and says so. The tool sends no version: an agent moves what it just read. Positions are floats
between neighbours, renumbered when they run out.

### 3.7 Connections and who may use them

- **Any signed-in user may use an admin connection** on a personal board and test queries through it, as the owner asked. It shows them
  what the company account sees. **Decision to check.**
- Commenting on an external issue through an admin connection takes edit on our side, so the company account doesn't speak for a viewer.
- Only admins manage admin connections. Others see id, name, provider, capabilities and health; never the URL, login, auth details or
  error, and never a secret.

### 3.8 Privacy of device connections

Cards are on the server, so they follow you across devices. The cost is that an issue's id, URL and title from a device connection are
stored on the server, visible only to that user. **Decision to check.** The connection's URL, login and secret stay on the device, and
an issue's body and comments are read live and never sent to the server (a test holds the worker to this).

## 4. The tracker part of an issue

Assignees, priority (low, normal, high, urgent), labels, due date, comments (ours; on the linked issue with `on_external`), activity
history written with each change, relations (blocks, blocked by, relates, duplicates), search and filters (project, board, stage, words
or key, assignees including `me`, unassigned, labels, priorities, due before, blocked, session), sort (updated, created, priority, due)
and saved views. Relations only show cards the caller can see. On a personal board only its owner can be assigned.

## 5. Agent

`Issues` is compact, with one `operation` field: `projects`, `boards`, `list`, `get`, `create`, `update`, `move`, `comment`, `link`,
`relate`, `retry`, `delete`.

- `get` on a linked card reads the issue live (the server for admin connections, the client for device ones), marked as untrusted and
  escaped so its text can't close the mark.
- With no board, `create` uses the only board there is in the project; with none it makes one, in the default project if none is named or
  linked; with several it asks.
- Each operation records the session. The session's PR is linked when the PR tracker detects one (`LinkSessionPullRequest`); `link` does
  it by hand.

### Sessions from issues

`LocalTrackerService.PrepareIssueSession` gives the folder linked on this device, a branch (`issue/<key>-<slug>`) and a first message.
**Start a session** on an issue opens the new session screen with those and a worktree on, and links session and branch once it exists. The
issue then shows session, branch and PR. A later agent can build on this with those three calls.

## 6. Knowledge

Separate from boards. Two kinds of source, each global and per-user:

| | Global | Per-user |
|---|---|---|
| Internal | `knowledge_document` scope `org` | `knowledge_document` scope `user` |
| Provider | an admin connection with `knowledge_enabled` | a device connection with `knowledge_enabled` |

- Provider documents are read **live** and never copied. An optional `knowledge_query` narrows a source, such as `project: HELP`.
- **Search**: the server's `SearchKnowledge` covers internal documents and global provider sources; the client's `SearchAllKnowledge` adds
  device sources and merges. Tools and the UI use only that. Each hit says its source. A source that fails is a warning beside the others'
  hits; non-admins aren't told why a global source failed, as it can name an internal host.
- **Reading**: provider articles are `conn:<connection id>:<article id>`, internal ones stay `kb_...`. Reads route by that.
- **Ranking**: provider hits have no score comparable to ours, so the merge takes each source in turn, keeping its order.
- **`query` on `/api/articles`**: the public docs list only `fields`, `$skip` and `$top`, but ymcp sends `query`. Results are also filtered
  here by the query's words, which holds if an instance ignores it. To be verified on a real instance.
- **Visibility**: per-user content is the owner's alone, not admins'. Device sources never reach the server.
- **Untrusted**: everything is marked as reference material, not instructions, and escaped against closing the wrapper.
- Scoping sources to a project: next, see 9.

## 7. Security

- Secrets are never logged, never sent to a model, never returned by an RPC. Forms send them write-only.
- Server requests go through the SSRF guard (`lib/ssrf.ts`) with the connection's `allow_private_network`, redirects included. The Hub session
  cookie goes to Hub only.
- Audit: creating, changing and deleting admin connections and changing shares.

## 8. Contracts

- `proto/harness/library/v1/tracker_connections.proto`: `TrackerConnectionService`, shared messages.
- `proto/harness/library/v1/projects.proto`: `ProjectService`.
- `proto/harness/library/v1/boards.proto`: `BoardService`.
- `proto/harness/library/v1/knowledge.proto`: additions, backward compatible.
- `proto/harness/agent/v1/trackers.proto`: `LocalTrackerService`: device connections, folder links, the merged Knowledge view.
- `proto/harness/library/v1/tracker.proto` is removed. **It breaks `buf breaking` on purpose**: nothing is released, no shims.

No SessionEvent, relay Command or UserPreferences numbers are used.

## 9. First version and what's next

### In the first version

Everything above, on server, client, tool and screens, tested on fakes: connections with all three auth methods, projects with sharing,
boards with inputs and outputs, device work through a client, the fields of an issue, saved views, sessions from issues, Knowledge from all
sources.

### Next

- **Tell the model its project up front.** A session in a linked folder gets it through the tool's default and `projects`; there is no line in
  the prompt or in `SessionInfo` yet.
- **Provider metadata refresh.** A provider project's values are from when it was mapped. `UpdateProject.provider` takes new ones, nothing
  calls it on a schedule.
- **Knowledge per project**: a project field on internal documents, and a provider project's key as the source's narrowing.
- **People and group pickers** in the share form, which takes an email or a group name now. Assignees come from `ListProjectMembers`.
- **Saved views shared with a project**; they are per user.
- **A card on several boards**, and moving a card between boards or projects.
- **Creating an external issue from a card**: `Provider.Create` is there, nothing calls it. **Comment on a device-connection issue from
  the screen**: the tool can, the screen posts on the card.
- **Webhooks** instead of polling; **Jira, Linear, GitHub Issues** providers; attachments.
- **Remote control**: `LocalTrackerService` answers on the client the window talks to, like SSH hosts, not through the relay.
