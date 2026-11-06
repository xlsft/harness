# Task tracker and knowledge base

How xlsft`s harness connects to external trackers, what a board is, and how the knowledge base reads from more than one
source. This file is the source of truth for the work and is kept current with every decision, so someone else can
continue from the branch. The status of each part is at the end.

## 1. Provider connections

A connection is one configured account on one tracker instance, managed like a model provider in
**Settings > Trackers**: connect, test, disable, remove. YouTrack is the first provider. Jira, Linear and GitHub Issues
follow by implementing the provider interface.

| | Personal, on the device | Admin, on the server |
|---|---|---|
| Who manages it | the user, per device | admins |
| Where the config lives | `HARNESS_HOME/tracker-connections.json` (0600), never synced | table `tracker_connection` |
| Where the secret lives | system keychain, 0600 file only where there is none (`HARNESS_KEYRING=0`, headless) | AES-256-GCM (HKDF purpose `tracker-secrets`, AAD `<id>:secrets`) like other provider keys |
| Where requests go from | the client: instances are often behind a VPN the server can't see | the server. Secrets never reach a client |
| Id | `tc_...`, made by the client | `tc_...`, made by the server |

The server knows a device connection only as an opaque id stored in a stage of the owner's personal board (see 2.5). It
never learns its URL, login or secret.

### Auth methods, checked against the YouTrack documentation

The user picks one per connection.

- **Token.** A YouTrack permanent token (`perm:...`), sent as `Authorization: Bearer`. Any user with *Update Self* creates
  one under Profile > Account Security, and it carries that user's permissions. This is JetBrains' recommendation for
  non-interactive integrations and the default in the form.
- **OAuth.** Authorization Code with PKCE against Hub (`<hub>/api/rest/oauth2/auth` and `/token`), with
  `access_type=offline` for a refresh token. YouTrack has no dynamic client registration: an admin of the instance
  creates an OAuth client (*Low-level Admin Write*), so the form asks for the client id, and the secret for a confidential
  client. Device connections redirect to a loopback address (`http://127.0.0.1:<port>/callback`; YouTrack accepts a
  registered loopback IP URI). Admin connections redirect to the server's `/tracker-oauth/callback`. The form states
  that the client must exist on the instance first.
- **Login and password.** YouTrack has no documented REST method for it: there is no basic auth, and JetBrains marks the
  Resource Owner flow deprecated. The method kept here is the one from `xlsft/ymcp`: post the Hub login form as the web UI
  does, then take a token from the implicit flow, renewed from the Hub session. It does not work with two-factor
  authentication, and the form says so. It depends on undocumented Hub behaviour, which is why it is not the default.

### Provider interface

`packages/client/tracker/provider.go`:

```go
type Provider interface {
    Capabilities() Capabilities
    Test(ctx) (Identity, error)
    Search(ctx, query string, page Page) (Issues, error)
    Get(ctx, id string) (*Issue, error)
    Create(ctx, IssueDraft) (*Issue, error)
    Update(ctx, id string, IssueUpdate) error
    Comment(ctx, id, text string) error
    Fields(ctx, project string) ([]Field, error)
}
// Optional, announced in Capabilities.Knowledge.
type KnowledgeProvider interface {
    SearchArticles(ctx, query string, limit int) ([]Article, error)
    ListArticles(ctx, parent string, page Page) (Articles, error)
    GetArticle(ctx, id string) (*Article, error)
}
```

`Capabilities` says what the provider can do and what the form should say: name, auth methods, query language name and
a sample, whether it has custom fields, comments and knowledge articles. Errors classify as `ErrNotFound`, `ErrAuth`,
`ErrForbidden` or transient (`IsTransient`: network, 429, 5xx), and the sync decides on those.

**Decision: the provider exists twice.** The server is TypeScript and admin requests must come from it, so
`packages/core/src/tracker/provider.ts` mirrors the Go interface and `youtrack.ts` mirrors the Go client's behaviour
(fallbacks included). The two are held together by matching tests on fakes of the same shape. A shared Go sidecar on the
server was rejected: nothing else in core runs Go.

### Working without admin rights

From ymcp, in both implementations:

- **Field schema.** `admin/projects/<id>/customFields` answers 403 or an empty list to a regular user; then the schema is
  merged from `projectCustomField` of the 20 most recently updated issues of the project. Schemas are cached for 5
  minutes, and a value a cached schema rejects is checked again against a fresh one.
- **Groups.** `groups` needs admin rights; the fallback is Hub (`usergroups`, `projectteams`) with a Hub-scoped token, and
  when both are empty, a clear error. Only the Go client has this now, as no flow uses groups yet; see 7.
- **Comments.** Deleted comments stay in the REST list without text; reads filter them out.
- Non-JSON answers (a proxy, a VPN page) become a readable error, not a decode error.

`packages/client/tracker/youtrack` is adapted from `internal/youtrack` of `xlsft/ymcp` (MIT); the notice is kept in
`packages/client/tracker/youtrack/LICENSE.ymcp` and in the file headers. Its MCP surface, TUI and pipelines are not
copied.

## 2. Boards

We have **internal boards** only, and they are configured.

- **Scope**: `shared` or `personal`, and that is the only distinction. A shared board uses admin connections. A personal
  board uses the owner's device connections or admin connections, and only the owner sees it; no role reaches it.
- A board is an ordered list of **stages**. A stage has zero or more **inputs** and zero or more **outputs**, at most one
  of each per connection.
- Cards are internal, optionally linked to one external issue. A card without an issue just works; nothing below
  applies to it except its position.

### 2.1 Data model (`packages/core/src/db/schema.ts`)

- `tracker_connection`: admin connections.
- `board`: `scope`, `owner_id` (personal), `name`, `manager_group_ids`.
- `board_stage`: `board_id`, `name`, `position`.
- `board_stage_input`: `stage_id`, `connection_scope` (`admin`/`device`), `connection_id`, `query`, `interval_seconds`,
  `last_polled_at`, `last_ok_at`, `last_error`, `lease_until`.
- `board_stage_output`: `stage_id`, `connection_scope`, `connection_id`, `fields` (`[{name, value}]`), `comment`.
- `board_card`: `board_id`, `stage_id`, `position`, `title`, `title_edited`, `description`, `version`, the link
  (`connection_scope`, `connection_id`, `external_id`, `external_url`, `external_state`, `external_checked_at`), and
  `session_ids`, `pr_urls`, `branch`.
- `board_card_output`: one application of an output to a card: `status`, `attempts`, `last_error`, `next_attempt_at`,
  `lease_until`, with the output's fields and comment copied at the moment of the move.
- `board_input_seen`: `(board_id, connection_scope, connection_id, external_id)`, the "once" ledger.

The old `tracker_task` table is dropped by migration; there is no data to preserve.

### 2.2 Inputs

An input pulls issues matching a query, in the provider's own syntax, into its stage as cards, **once per board**. The
`board_input_seen` row is written with the card and outlives it. So:

- **Moving a card out does not bring it back.** Neither does deleting it, nor the issue changing and matching again.
- **An issue already on the board by another input or a manual link** is not pulled twice.
- **Deliberately no re-entry feature.** To pull an issue again, link it by hand: `link` on a card ignores the ledger.
- First poll: everything that matches comes in, up to 200 issues per poll; the rest follow on later polls because the
  ledger skips what is there. The query is always asked `sort by: updated desc`-free, exactly as written.
- Queries are tested from the form against the live connection before saving (`SearchConnectionIssues` on the server,
  `SearchIssues` on the client).

### 2.3 Outputs

Entering a stage with an output for the card's connection creates a `board_card_output` in the same transaction as the
move. An output sets fields (`Stage: Review`) and may add a comment.

- Only a real change of stage fires, and only an output of the connection the card is linked to.
- A card pulled in by an input does **not** fire the output of the stage it lands in: nothing moved it there.
- Leaving the stage supersedes outputs still pending or failed there, so a quick drag through a stage applies only what
  the card ends up on. An output already running finishes.
- **Failures are visible on the card, never silent.** Status is `pending`, `running`, `failed` or `done`. A transient
  error retries with backoff (1, 5, 15 min, three retries) and shows `pending` with the error; a permanent one, or the
  last retry, stays `failed` until the user retries (`RetryCardOutput`, button on the card, `retry` in the tool).
- A pending output on a device connection shows that it waits for the device.

### 2.4 Where polling runs

**Work runs where the credential lives.** That is the rule, and "the server for shared boards, a running client for
personal ones" follows from it, with one refinement.

| | Input and output on an admin connection | Input and output on a device connection |
|---|---|---|
| Shared board | server | not allowed |
| Personal board | **server** | a running client of that device |

**Decision to check:** a personal board's stage on an admin connection is polled by the server, not by a client. The
credential cannot leave the server anyway, so a client could only ask the server to do it, and this way it also works
with no client running.

- **Server**: a tick every 30 seconds (`services/board-sync.ts`). Due inputs are claimed with a lease
  (`lease_until`, one conditional UPDATE), so several replicas never double-poll. `HARNESS_BOARD_SYNC=off` turns it off.
- **Client**: a worker in the long-running client (`tracker/sync.go`) ticks every 30 seconds, and `SyncNow` runs it at once
  after a move in the UI. It calls `ClaimDeviceWork` with the ids of its enabled connections; the server answers with
  due inputs and pending outputs **of that user's personal boards only**, leased to this call. It runs them and reports
  with `ReportDeviceWork`. The server validates every report against the owner.
- **Interval**: per input, default 300 seconds, minimum 60, maximum one day. A failed poll backs off by the same interval,
  and `last_error` shows on the stage. A manual "Sync now" clears the wait.
- **Device absent**: nothing polls. The stage shows when it last polled and `This connection is on another device` when
  the viewing client lacks that connection.

### 2.5 External changes

- **Title**: a card's title follows the issue's summary until someone edits it on the card (`title_edited`).
- **Description and fields**: never copied; `get` reads them live. The card stores only id, URL and title.
- **State changes outside**: do **not** move cards. The board is ours; an input is a door, not a mirror. An issue that stops
  matching a query leaves its card where it is.
- **Reconcile**: with each poll cycle up to 50 linked cards of the connection, oldest check first, are read with
  `Get`. Not found sets `external_state = missing`: the card stays, shows it, and outputs fail visibly. If the issue
  comes back, the state returns to `linked`. Auth and transient errors never mark a card missing.
- **Connection removed**: its inputs and outputs go, pending outputs are superseded, and cards keep their text with
  `external_state = disconnected`. Device connections do this through `ForgetDeviceConnection`.
- **Connection disabled**: polling stops, and an output due on it fails visibly and can be retried after enabling.

### 2.6 Two people at once

`MoveCard` carries the `version` the mover saw. A stale one answers `FAILED_PRECONDITION` with nothing changed: the first
move wins, only its output fires, and the UI reloads and says so. The tool sends no version and moves last-write-wins,
as an agent always acts on what it just read. Positions are floats between neighbours, renumbered when they run out.

### 2.7 Permissions

- **Personal board**: its owner only. Everyone else, admins included, gets `NOT_FOUND`.
- **Shared board, seeing and working cards** (create, edit, move, comment, retry, link): every signed-in user.
- **Shared board, configuring** (stages, inputs, outputs, name, delete): admins, and members of the board's
  `manager_group_ids`. Only admins create shared boards and set the manager groups.
- **Admin connections, using them on a personal board** and testing queries through them: any signed-in user, as the task
  said a personal board can use them. This shows such a user what the company account sees. **Decision to check.**
- **Admin connections, managing**: admins. Others see id, name, provider, capabilities and health; never the login, auth
  details or `last_error` beyond "failing", and never a secret.

### 2.8 Privacy of device connections

Personal boards live on the server, so a user's cards follow them across devices. The cost is that an issue's id, URL and
title from a device connection are stored on the server, visible only to that user. **Decision to check.** The
connection's URL, login and secret stay on the device, and the issue's body and comments are read live and never stored.

## 3. Agent

`TaskTracker` is compact, with one `operation` field: `boards`, `list`, `get`, `create`, `update`, `move`, `comment`,
`link`, `retry`, `delete`.

- `get` on a linked card reads the issue live (the server for admin connections, the client for device ones), marked as
  untrusted external content.
- `comment` posts on the linked issue, from where the credential lives.
- With no `board`, `create` uses the only board the user can see; with none it makes a personal **My tasks** with To do, In
  progress and Done; with several it asks which. `list` with no board goes across all.
- Every operation on a card records the session. The session's PR is linked as soon as the PR tracker sees one
  (`LinkSessionPullRequest`); `link` does it by hand.

### Sessions from cards

`PrepareCardSession` returns a branch name (`card/<slug>-<short id>`) and a first prompt. The card's menu opens the new
session screen with both and a worktree on, and after creation calls `LinkCard` with session and branch. The card then
shows session, branch and PR. A later agent can build on this: everything needed is `PrepareCardSession`,
`CreateSession` and `LinkCard`.

## 4. Knowledge base

Separate from boards. Two kinds of source, each with a global and a per-user scope:

| | Global | Per-user |
|---|---|---|
| Internal | `knowledge_document` scope `org` | `knowledge_document` scope `user` |
| Provider | an admin connection with `knowledge_enabled` | a device connection with `knowledge_enabled` |

- Provider documents are read **live** and never copied. An optional `knowledge_query` narrows a source, such as
  `project: HELP`.
- **Search**: the server's `SearchKnowledge` covers internal documents and global provider sources. The client's
  `SearchAll` adds device sources and merges; the tools and the UI use only that. Each hit says its source. A source that
  fails is reported as a warning next to the hits of the others, never hiding them.
- **Reading**: document ids of provider articles are `conn:<connection id>:<article id>`, internal ones stay `kb_...`. Reads
  route by that prefix.
- **Ranking**: provider hits come from the provider's own search and have no score comparable to ours. The merge
  alternates sources, keeping each source's order.
- **`query` on `/api/articles`**: the public docs list only `fields`, `$skip` and `$top`, but ymcp sends `query`. To be
  safe, results are filtered on the client side by the words of the query, which holds even if an instance ignores the
  parameter. To be verified on a real instance.
- **Visibility**: per-user content is the owner's alone, not admins'. Device sources never reach the server at all.
- **Untrusted**: everything is marked as reference material, not instructions, and escaped so that its text can't close
  the wrapper tag.

## 5. Security

- Secrets are never logged, never sent to a model, never returned by an RPC. Forms send them write-only.
- Server requests go through the SSRF guard (`lib/ssrf.ts`) with the connection's `allow_private_network`, redirects included.
- Tool output contains issue text only inside an untrusted wrapper.
- Audit: creating, changing and deleting admin connections writes the audit log.

## 6. Contracts

- `proto/harness/library/v1/tracker_connections.proto`: `TrackerConnectionService` (server) and the shared messages.
- `proto/harness/library/v1/boards.proto`: `BoardService` (server).
- `proto/harness/library/v1/knowledge.proto`: additions, all backward compatible.
- `proto/harness/agent/v1/trackers.proto`: `LocalTrackerService` (client): device connections, `SyncNow`, the merged
  knowledge view.
- `proto/harness/library/v1/tracker.proto` is removed. **It breaks `buf breaking` on purpose:** the task said to replace the
  tables, and the only callers are in this repository.

No SessionEvent, relay Command or UserPreferences numbers are used so far.

## 7. Status and what is left

See the bottom of this file, kept current per commit.

### Done

Nothing yet: this is the first commit.

### Left

Everything above.
