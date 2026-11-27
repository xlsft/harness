# Issues and Knowledge

How xlsft`s harness tracks issues: projects, boards, connections to external trackers, and how Knowledge reads from more than
one source. This file is the source of truth for the work and is kept current with every decision, so someone else can continue
from the branch. What is built and what comes next is in section 9; how to add a tracker or knowledge provider is section 10.

The name: the product surface is **Issues** (sidebar, `/issues`, the `Issues` tool). In code and contracts an issue here is a
**card**, so it isn't taken for a provider's issue (`TrackerIssue`); "tracker" means an external one (`tracker` packages,
Settings > Issue trackers). Likewise **Knowledge** to users, `/knowledge` as the route.

## 1. Provider connections

There are two kinds of provider, each with its own connections and its own settings section:

- **Issue trackers** (Settings > Issue trackers): issues for boards. YouTrack is first; Jira, Linear, GitHub Issues, GitLab Issues,
  Yandex Tracker and Kaiten follow as providers (section 10).
- **Knowledge sources** (Settings > Knowledge sources): documents Knowledge reads live (section 6). The YouTrack knowledge base is first
  (`youtrack-kb`, "YouTrack Knowledge Base"); Confluence, Yandex Wiki, Linear documents, GitHub and GitLab wikis and Kaiten documents
  follow.

A connection is one configured account on one instance, managed like a model provider: connect, test, disable, remove. Both kinds
come in the same two scopes:

| | Personal, on the device | Company, on the server |
|---|---|---|
| Who manages it | the user, per device | admins; users use it without seeing how it signs in |
| Where the config lives | `HARNESS_HOME/tracker-connections.json`, `HARNESS_HOME/knowledge-connections.json` (0600), never synced | tables `tracker_connection`, `knowledge_connection` |
| Where the secret lives | system keychain (`tracker-connection:<id>`, `knowledge-connection:<id>`); a 0600 file only where there is none (`HARNESS_KEYRING=0`, headless): `tracker-connection-keys.json`, `knowledge-connection-keys.json` | AES-256-GCM, HKDF purpose `tracker-secrets` or `knowledge-secrets`, AAD `<id>:secrets` |
| Where requests go from | the client: instances are often behind a VPN the server can't see | the server. Secrets never reach a client |

The server knows a device connection only as an opaque id in a stage or a project. It never learns its URL, login or secret, and it
never learns of a device knowledge source at all.

### Borrowing a sign-in: combined products without signing in twice

A knowledge source connection either signs in on its own or **borrows the sign-in of a tracker connection**. Decisions:

- A knowledge provider declares which tracker providers it can borrow from (`KnowledgeCapabilities.borrow_from`): same vendor and same
  site or organization. Jira for Confluence (one Atlassian site), Yandex Tracker for Yandex Wiki (one Yandex organization), GitHub or
  GitLab Issues for their wikis (the same code host connection), Linear for its documents (one workspace), Kaiten for its documents (one
  company), YouTrack for its knowledge base (one instance).
- The connection stores only a reference (`borrow_connection_id`; the device's `borrow`), **of the same scope**: a device source borrows a
  device tracker connection, a company source a company one. A company source can never borrow a device connection and the reverse.
- Credentials are **resolved at request time, never copied**: every open decrypts the tracker connection's secret, takes its address
  when the source has none of its own, and merges its options under the source's own. Refreshed OAuth tokens are written back to the
  tracker connection. A new token there is used by the source at once.
- A borrowing connection keeps no auth, login, OAuth client or secret of its own (they are cleared on save). Its own address is
  allowed (the form offers it as an optional address, empty meaning the tracker's), and then the tracker's credentials go there: only the owner of the device or an
  admin can set either.
- The tracker connection disabled: the source fails with "<name>, whose sign-in this uses, is disabled". Removed: "the tracker connection
  whose sign-in this uses is gone". The source is not removed with it; the user sees why it fails and picks another sign-in.
- OAuth for a borrowing source is the tracker connection's: Authorize there. `StartKnowledgeOAuth` refuses a borrowing source.

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

### Auth methods every provider can declare

`TrackerAuthMethod`, shared by both kinds: `TOKEN = 1`, `PASSWORD = 2`, `OAUTH = 3`, `EMAIL_TOKEN = 4` (account email in `login` and an API
token, such as Atlassian Cloud basic auth), `VCS_CONNECTION = 5` (through the server's code host connection from Settings > VCS, named by
the option `vcs_connection_id` and, for a GitHub App, `vcs_owner`; on a device through `gh auth token --hostname <host>` or
`glab config get token --host <host>` for the host of the base URL; nothing is stored). A provider lists the ones it supports in its
capabilities; the generic checks refuse any other, require the login for `PASSWORD` and an email for `EMAIL_TOKEN`, and the OAuth client id
for `OAUTH`.

### Provider interface

`packages/client/tracker/provider.go`: `Capabilities`, `Test`, `Search`, `Get`, `Create`, `Update`, `Comment`, `Fields`, `Projects`.
`packages/client/tracker/knowledge.go`: `KnowledgeProvider` with `Test`, `SearchArticles`, `ListArticles`, `GetArticle`, opened from
`KnowledgeOpenOptions`. Capabilities say what a provider can do and what the form should say (methods, option fields, docs URL, logo,
address placeholder or default, per-method hints, and for knowledge `borrow_from`). Failures classify as `ErrNotFound`, `ErrAuth`,
`ErrForbidden` or `ErrTransient`, and the sync decides on those. Section 10 has the exact interfaces.

**Decision: the provider exists twice.** The server is TypeScript and admin requests must come from it, so
`packages/core/src/tracker/provider.ts`, `packages/core/src/knowledge/provider.ts` and each provider module mirror the Go behaviour,
fallbacks included. The two are held together by
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
| Provider | a company knowledge source connection (`knowledge_connection`) | a device knowledge source connection |

- Provider documents are read **live** and never copied. An optional `query` narrows a source, in the provider's syntax
  (`KnowledgeCapabilities.query_language`), such as `project: HELP`.
- **Sources are knowledge source connections**, not tracker connections (section 1). One may borrow a tracker connection's sign-in.
  Until this split a tracker connection served documents itself (`knowledge_enabled`, `knowledge_query`); those fields are deprecated in
  the proto (numbers 12 and 13 kept, never reused), gone from the form, and always false.
- **Migration.** Company: migration `0018_knowledge_connections` turns every `youtrack` connection with `knowledge_enabled` into a
  `youtrack-kb` knowledge connection borrowing it, enabled as it was, keeping its query and recording `migrated_from`, then clears the
  flag. Device: `tracker.New` runs `KnowledgeStore.MigrateKnowledge` at startup, which does the same for every device connection with the
  flag, picking the first knowledge provider that borrows from its provider; it does nothing once done, and a failure is retried at the
  next start.
- **Document ids**: `conn:<knowledge connection id>:<article id>`, internal ones stay `kb_...`. Reads route by the prefix: a device source's
  id is answered by the client, anything else goes to the server. **Ids from before the split** name the tracker connection
  (`conn:tc_...:<article>`); both sides resolve them through `migrated_from`, so links in old sessions still read, and the document comes
  back under the source's new id.
- **Search**: the server's `SearchKnowledge` covers internal documents and global provider sources; the client's `SearchAllKnowledge` adds
  device sources and merges. Tools and the UI use only that. Each hit says its source. A source that fails is a warning beside the others'
  hits; non-admins aren't told why a global source failed, as it can name an internal host.
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
- Audit: creating, changing and deleting admin connections (`tracker_connection.*`, `knowledge_connection.*`) and changing shares.
- A borrowing knowledge source never stores or returns the borrowed secret; regular users see neither its `borrow` nor how it signs in.
- Options are never secrets: they are stored and shown in the clear (to admins for company connections).

## 8. Contracts

- `proto/harness/library/v1/tracker_connections.proto`: `TrackerConnectionService`, shared messages (`TrackerAuthMethod`,
  `ProviderOptionField`, `ProviderAuthHint`).
- `proto/harness/library/v1/knowledge_connections.proto`: `KnowledgeConnectionService` (company knowledge sources), `KnowledgeCapabilities`,
  `KnowledgeConnection`, `KnowledgeConnectionInput`. Forwarded by the client's core proxy.
- `proto/harness/library/v1/projects.proto`: `ProjectService`.
- `proto/harness/library/v1/boards.proto`: `BoardService`.
- `proto/harness/library/v1/knowledge.proto`: additions, backward compatible.
- `proto/harness/agent/v1/trackers.proto`: `LocalTrackerService`: device connections (trackers and, in the `*LocalKnowledge*` methods,
  knowledge sources; a device knowledge OAuth flow is polled with `GetLocalTrackerOAuth`), folder links, the merged Knowledge view.

**Numbers reserved for providers, so provider work never touches an enum or a field number:**

| Message | Field | Number |
|---|---|---|
| `TrackerAuthMethod` | `EMAIL_TOKEN`, `VCS_CONNECTION` | 4, 5 |
| `TrackerCapabilities` | `knowledge` (deprecated), `option_fields`, `docs_url`, `logo`, `base_url_placeholder`, `default_base_url`, `auth_hints` | 8, 9, 10, 11, 12, 13, 14 |
| `TrackerConnection` | `knowledge_enabled`, `knowledge_query` (deprecated), `options` | 12, 13, 18 |
| `TrackerConnectionInput` | `knowledge_enabled`, `knowledge_query` (deprecated), `options` | 12, 13, 14 |
| `ProviderOptionField` | `key`, `label`, `placeholder`, `required`, `allowed_values`, `help`, `default_value` | 1-7 |
| `ProviderAuthHint` | `method`, `help`, `secret_label`, `login_label` | 1-4 |
| `KnowledgeCapabilities` | `provider` ... `description` | 1-14 |
| `KnowledgeConnection` / `KnowledgeConnectionInput` | `borrow` / `borrow_connection_id` | 14 |

The next free numbers: `TrackerCapabilities` 15, `TrackerConnection` 19, `TrackerConnectionInput` 15, `KnowledgeCapabilities` 15,
`KnowledgeConnection` 20, `KnowledgeConnectionInput` 15, `TrackerAuthMethod` 6. A provider that needs one asks for it in its own change.
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
- **Webhooks** instead of polling; attachments.
- **Providers**: trackers Jira, Linear, GitHub Issues, GitLab Issues, Yandex Tracker, Kaiten; knowledge Confluence, Yandex Wiki, Linear
  documents, GitHub and GitLab wikis, Kaiten documents. One agent per vendor, following section 10.
- **Remote control**: `LocalTrackerService` answers on the client the window talks to, like SSH hosts, not through the relay.

## 10. Writing a provider

A provider is a few files and one line per registry. Nothing else changes: no proto enum or number, no Vue file except one logo line,
no service or RPC. If something seems to need more, stop and ask: the foundation is meant to cover it.

### What to add

For a tracker `acme` (and/or a knowledge provider `acme-wiki`):

1. **Go**: a package `packages/client/tracker/acme` whose `init` calls `tracker.Register("acme", tracker.Factory{...})` and/or
   `tracker.RegisterKnowledge("acme-wiki", tracker.KnowledgeFactory{...})`. A vendor with both may keep both in one package (YouTrack
   does: `provider.go` and `knowledge.go`), or a knowledge provider gets its own package (`packages/client/tracker/acmewiki`).
2. **Go registry line**: one blank import in `packages/client/tracker/providers/providers.go`, sorted.
3. **TS**: `packages/core/src/tracker/acme.ts` exporting a `TrackerModule`, and/or `packages/core/src/knowledge/acme-wiki.ts` exporting a
   `KnowledgeModule`.
4. **TS registry line**: append to `TRACKERS` or `KNOWLEDGE` in `packages/core/src/tracker/registry.ts`.
5. **Logo**: one entry in `packages/ui/app/utils/providerLogos.ts` under the key you put in `Capabilities.Logo` / `logo` (an SVG from a
   CC0 or MIT set, with the source in the comment). Without one the UI shows two letters.
6. **Tests with a fake HTTP server** on both sides (below).

Ids: lower case, `[a-z][a-z0-9-]*`, unique across both kinds. A knowledge provider is named after its product (`confluence`,
`yandex-wiki`), or `<tracker>-<thing>` when it has no name of its own (`youtrack-kb`, `linear-docs`, `github-wiki`, `gitlab-wiki`,
`kaiten-docs`). Trackers: `jira`, `linear`, `github`, `gitlab`, `yandex-tracker`, `kaiten`.

### Go interfaces (`packages/client/tracker`)

```go
type Provider interface { // provider.go
	Capabilities() Capabilities
	Test(ctx context.Context) (Identity, error)
	Search(ctx context.Context, query string, page Page) (issues []Issue, hasMore bool, err error)
	Get(ctx context.Context, id string) (*Issue, error) // with description and recent comments; ErrNotFound
	Create(ctx context.Context, draft IssueDraft) (*Issue, error)
	Update(ctx context.Context, id string, update IssueUpdate) error
	Comment(ctx context.Context, id, text string) error
	Fields(ctx context.Context, project string) ([]Field, error)
	Projects(ctx context.Context) ([]Project, error)
}

type Factory struct {
	Capabilities Capabilities
	Open         func(OpenOptions) (Provider, error)
	OAuth        func(baseURL string) (OAuthExchange, error)              // nil without OAuth
	Validate     func(baseURL string, options map[string]string) error // optional
}

type KnowledgeProvider interface { // knowledge.go
	Test(ctx context.Context) (Identity, error)
	SearchArticles(ctx context.Context, query, base string, limit int) ([]Article, error)
	ListArticles(ctx context.Context, parentID, base string, page Page) (articles []Article, hasMore bool, err error)
	GetArticle(ctx context.Context, id string) (*Article, error)
}

type KnowledgeFactory struct {
	Capabilities KnowledgeCapabilities // BorrowFrom lists tracker provider ids
	Open         func(KnowledgeOpenOptions) (KnowledgeProvider, error)
	OAuth        func(baseURL string) (OAuthExchange, error)
	Validate     func(baseURL string, options map[string]string) error
}
```

`OpenOptions` carries `BaseURL`, `Auth`, `Login`, `OAuthClientID`, `Credentials` (`Secret`, `ClientSecret`, `AccessToken`,
`RefreshToken`, `ExpiresAt`), `Options`, `OnTokens` (call it with refreshed OAuth tokens) and `VCSToken` (for `AuthVCS`).
`KnowledgeOpenOptions` has the same plus `BorrowedFrom` (the tracker provider, "" for its own sign-in) and `TrackerBaseURL`. When
borrowing, the credentials, address and options are the tracker connection's: build your client from them exactly as for your own
sign-in. `base` in the article methods is the connection's `Query`.

### TS interfaces (`packages/core/src`)

```ts
interface TrackerModule { // tracker/provider.ts
  capabilities: Capabilities;
  open(o: OpenOptions): TrackerProvider;
  oauth?: OAuthModule; // authorizeUrl(...), exchangeCode(...)
  normalizeUrl?(raw: string): string;
  validate?(baseUrl: string, options: Record<string, string>): void; // throw ApiError("invalid_request")
}
interface KnowledgeModule { // knowledge/provider.ts
  capabilities: KnowledgeCapabilities;
  open(o: KnowledgeOpenOptions): KnowledgeProvider; // test, searchArticles, listArticles, getArticle
  oauth?: OAuthModule;
  normalizeUrl?(raw: string): string;
  validate?(baseUrl: string, options: Record<string, string>): void;
}
```

`OpenOptions` has `baseUrl`, `auth`, `login`, `oauthClientId`, `credentials` (`token` for `token` and `email_token`, `password`,
`clientSecret`, `accessToken`, `refreshToken`, `expiresAt` in ms), `options`, `allowPrivateNetwork`, `retryDelaysMs`, `onOAuthTokens`
and `vcs` (for `vcs`: `() => Promise<{ type, baseUrl, token() }>`). Throw `TrackerError(kind, message)` with kind `not_found`, `auth`,
`forbidden`, `transient` or `other`. Every request goes through `guardedFetch` from `lib/ssrf.ts` with `allowPrivateNetwork`
(redirects included); never `fetch` directly.

### Capabilities: what the UI renders

The settings sections are generic: they show a row per provider with the logo, the display name, what it gives (knowledge:
`description`), its sign-in methods, a Docs link (`docs_url`) and Connect; the form shows the address (placeholder
`base_url_placeholder`, optional when `default_base_url` is set), the methods (`auth_methods` in that order, the first is the default)
with your `auth_hints` (help, secret and login labels) or a generic text, then your `option_fields`, then the query field when
`query_language` is set (knowledge). Keep Go and TS capabilities identical.

### Option fields

For settings that are not credentials: a Yandex organization id and its kind (`org_id`, `org_kind` with `allowed_values: ["360",
"cloud"]`), a Jira deployment (`deployment`: `cloud` or `data-center`), a GitHub App owner (`vcs_owner`). Declare `key` (lower case
letters, digits, `_`), `label`, `placeholder`, `required`, `allowed_values`, `help`, `default_value`. The stores keep only declared keys,
fill defaults and check required and allowed values (`tracker.CheckOptions`, `checkOptions` in `tracker/signin.ts`) before your code
sees them. **Never put a secret in an option**: options are stored and shown in the clear. A provider using `VCS_CONNECTION` declares
`vcs_connection_id` (and `vcs_owner` for GitHub) among its option fields; `VCS_OPTION_FIELDS` in `tracker/vcs-signin.ts` has them.

### Borrowing

A knowledge provider that can use a tracker's sign-in lists the tracker ids in `BorrowFrom` / `borrowFrom`. The stores check the provider
and the scope, resolve the credentials at open and hand them to `Open` as if they were the source's own, with `BorrowedFrom` set; your
`Open` must not care where they came from except to derive another API host when the products live on different hosts (Yandex Wiki's
API is not Tracker's; Confluence Cloud is the Jira site under `/wiki`). Anything that must match between the two (the same Atlassian site,
the same Yandex organization) is checked in `Validate`, which receives the merged options and the effective base URL. A provider with
no sign-in of its own leaves `AuthMethods` empty and must list at least one `BorrowFrom`.

### Tests with a fake server

Follow `packages/client/tracker/youtrack/youtracktest` and `packages/core/test/helpers/fake-youtrack.ts`: an `httptest.Server` (Go) or a
Node HTTP server (TS) serving the vendor's REST paths with canned JSON, checking the auth header, and able to fail with chosen status
codes. Cover: each auth method signing in, `Test` naming the account, search and pagination (`hasMore`), `Get` of a missing item
returning not found, a 401/403/429/5xx classified right, a non-JSON answer (a proxy page) giving a readable error, and for knowledge a
borrowed sign-in opening through `tracker.New(...).Knowledge` (see `TestKnowledgeBaseBorrowsTheTrackerSignIn`). On the server side,
`packages/core/test/provider-registry.test.ts` must keep passing with your line added. Never a real account, key or token: fakes only.

### Checklist

- [ ] Every auth method verified against the vendor's current documentation, with the URL in a comment next to the code that uses it
      (header format, token type, OAuth endpoints, scopes, PKCE support, loopback redirect support for device OAuth).
- [ ] Rate limits: honour `429` and `Retry-After`, classify as transient; no retry of a write that may have happened.
- [ ] Pagination: `Page.Limit`/`Offset` mapped to the vendor's cursors or pages; `hasMore` true only when there is more.
- [ ] Company connections: every request through `guardedFetch` with the connection's `allowPrivateNetwork`, redirects included; the
      device side has no such guard because it runs on the user's network.
- [ ] No secret in a log, an error message, a URL or a query string: tokens go in headers only. Error messages name the path and status.
- [ ] Non-JSON answers (an HTML login or VPN page) become a readable error, not a parse failure.
- [ ] Capabilities identical in Go and TS; `docs_url` set; a logo line added or the monogram accepted.
- [ ] Option fields declared for every non-secret setting; `Validate` for what must go together.
- [ ] Knowledge: `BorrowFrom` set when the vendor has a tracker on the same site; `Tree` / `tree` when documents have children.
- [ ] Untrusted content: return text as the vendor gives it; Knowledge marks and escapes it, never you.
- [ ] Tests on fakes on both sides; `go test ./tracker/...` and the core suite pass.

