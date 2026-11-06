# harness server image

One container, four processes under [s6-overlay](https://github.com/just-containers/s6-overlay) v3
(docs/PLAN.md §1.7). No sidecars, no Docker-in-Docker, no privileged mode.

| Process | What | Listens on | Runs as |
|---|---|---|---|
| `core` | Nitro server from `packages/core` (Node 24), and the web UI from `packages/ui` as static files | `:3000` (public) | `harness` |
| `postgres` | Postgres 17, data in `/data/postgres` | unix socket `/run/postgresql` only | `postgres` |
| `searxng` | SearXNG (pinned upstream commit) under granian | `127.0.0.1:8888` only | `harness` |
| `objects` | [Garage](https://garagehq.deuxfleurs.fr/) (S3 object store, pinned release), data in `/data/objects` | `127.0.0.1:3900` (S3), `127.0.0.1:3901` (its RPC) only | `harness` |

Start order: a one-shot `init-data` lays out `/data` and initializes the cluster, then `postgres`
starts, a one-shot `postgres-ready` waits for `pg_isready` and creates the `harness` role and
database if missing, `objects` starts and a one-shot `objects-ready` waits for its port, then `core`
starts. `searxng` only waits for `init-data`. s6 restarts any
process that exits. On `docker stop`, core is stopped first (it closes the listener and drains
in-flight requests), then Postgres gets SIGINT (fast shutdown: clean checkpoint, no recovery on the
next start).

## Run

```bash
docker run -d --name harness -p 3000:3000 -v harness-data:/data \
  -e HARNESS_PUBLIC_URL=https://harness.example.com \
  -e HARNESS_ADMIN_EMAIL=you@example.com -e HARNESS_ADMIN_PASSWORD='a long password' \
  -e HARNESS_SECRET="$(openssl rand -base64 48)" \
  --stop-timeout 20 \
  ghcr.io/xlsft/harness:latest
```

Images are published to `ghcr.io/xlsft/harness` for `linux/amd64` and `linux/arm64`, tagged `X.Y.Z`, `X.Y` and `latest` (a
pre-release gets only its own `X.Y.Z-rc.N`), and signed; [how to verify them](../docs/release.md#verifying-a-release).

- `HARNESS_ADMIN_EMAIL` / `HARNESS_ADMIN_PASSWORD` create the first admin on startup (username: `HARNESS_ADMIN_USERNAME`, or the email's local part). They are optional: on a server with no accounts, the first person to sign in (from the app or the CLI) creates the admin account on the sign-in page (name, username, email, password). After that, sign-up is closed and an admin adds people, unless `HARNESS_ALLOW_SIGNUP=true`. People sign in with their email or their username (3-30 letters, digits, `_` or `.`; case-insensitive).
- Set `HARNESS_SECRET` (32+ characters) explicitly and keep it: it derives the auth secret and
  encrypts stored provider keys. Without it core generates one into `/data/core/secret`, which works
  but then that file is part of what you must back up.
- `--stop-timeout 20` gives core time to drain long-lived streams before Postgres stops (core is
  SIGKILLed after 8 s at the latest; an idle shutdown takes about 4 s). If Docker's timeout runs out
  first, Docker SIGKILLs the whole container and Postgres runs crash recovery on the next start
  (safe, but not clean). Some Docker setups use a timeout shorter than the usual 10 s, so set it.
- Put TLS in front with a reverse proxy and set `HARNESS_TRUST_PROXY` as core documents.

Build it yourself from the repo root:

```bash
docker build -f deploy/Dockerfile -t ghcr.io/xlsft/harness .
```

Health: `GET /api/health` (also the image's `HEALTHCHECK`).

## Environment

Defaults baked into the image:

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `3000` | core's HTTP port |
| `HARNESS_PUBLIC_URL` | `http://localhost:3000` | the URL clients and the browser use; set it |
| `HARNESS_DATA_DIR` | `/data/core` | core's files (secret, attachments; today also the SQLite database) |
| `DATABASE_URL` | `postgres://harness@/harness?host=/run/postgresql` | internal Postgres over its unix socket |
| `HARNESS_SEARXNG_URL` | `http://127.0.0.1:8888` | internal SearXNG |
| `HARNESS_PROMPTS_DIR` | `/app/prompts` | prompt files shipped in the image |
| `HARNESS_WEB_DIR` | `/app/web` | the built web UI; empty: the server serves no pages but `/device` |
| `HARNESS_VERSION` | the release's version | set by the image build; `/api/health` reports it |
| `PGHOST` | `/run/postgresql` | so `psql` / `pg_dump` in `docker exec` find the socket |
| `HARNESS_S3_ENDPOINT` | `http://127.0.0.1:3900` | internal object store; see "Object store" |
| `HARNESS_S3_CREDENTIALS_FILE` | `/data/objects/s3-credentials` | its keys, generated at first start |

`DATABASE_URL` form: an empty host plus a `host=` query parameter that is a directory selects a
unix socket. node-postgres (`pg`, via `pg-connection-string`), postgres.js and libpq all accept this
form. The equivalent percent-encoded form `postgres://harness@%2Frun%2Fpostgresql/harness` also
works and is also recognized as internal. No password: Postgres authenticates socket connections
by peer (OS user `harness` → role `harness`).

Passed through to core (see `packages/core`): `HARNESS_SECRET`, `HARNESS_ADMIN_EMAIL`,
`HARNESS_ADMIN_PASSWORD`, `HARNESS_ALLOW_SIGNUP`, `HARNESS_TRUST_PROXY`, `HARNESS_GRPC_PORT`
(publish that port too if you set it), `HARNESS_SESSION_BUDGET_USD`, and
the other `HARNESS_*` tuning variables.

Model prices follow a public catalog: core fetches `https://models.dev/api.json` (no key, about 6 MB) once a day, and
keeps the prices it needs in its database, so a restart or a lost connection changes nothing and a failed fetch keeps the
last good one. Without any, the prices shipped with core apply. On an air-gapped install set `HARNESS_MODEL_PRICES=off`
(no fetch, also not by the Refresh button; catalog prices already on models stay), or set `HARNESS_MODEL_PRICES_URL` to a
mirror of the same document, which may be on your private network. See [the gateway protocol](../docs/protocol/gateway.md).

> Today's core still stores its data in SQLite (`HARNESS_DB_URL`, default
> `file:/data/core/harness.db`) and ignores `DATABASE_URL`. The internal Postgres runs anyway, so
> the same image and volume keep working when core moves to Postgres.

## Web UI

The image serves the web interface at `/`, so people reach xlsft`s harness from a browser with nothing installed. Set
`HARNESS_PUBLIC_URL` to the address they use: the session cookie and its same-origin check depend on it.

What it can do depends on whether a client is connected, because the server never runs code: sessions, tools and files live
on a client.

| | Without a client | With a client |
|---|---|---|
| Sign in (password, LDAP, SSO, second factor) | Yes | Yes |
| Account, second factor, own usage | Yes | Yes |
| By role: users, groups and quotas, LDAP, SSO, provisioning, audit log, managed policy, providers, usage of everyone | Yes | Yes |
| Agent sessions, tools, terminal, files, history on that machine, sandbox, local models and everything else on the device | **No** | Yes |

To connect one, run it on your machine, or anywhere the browser can reach it, with the page's address allowed:

```bash
harness-client login https://harness.example.com
harness-client serve --listen 127.0.0.1:7801 --cors-origin https://harness.example.com
```

and enter its address and the token it prints on the page. The browser talks to that client directly and the server never sees
the token; the choice lasts as long as the tab. For another machine add `--allow-non-loopback` to the client and read the
warning it prints: the connection is plain HTTP, and browsers refuse a plain HTTP address from an HTTPS page unless it is
loopback, so put TLS in front of it. Safari is stricter and may refuse plain HTTP to loopback from an HTTPS page too; use another browser, or the
desktop app.

## Users and LDAP

Admins manage accounts in the app: **Settings → Users** (the section is shown by role; the server decides what each role may
do, `packages/core/src/auth/roles.ts`, and refuses the rest).

- **Add a user**: name, optional username, email, role, and either a password or "generate a
  password and show it once" (copy it then; it is never shown again). "Signs in through LDAP"
  pre-provisions a directory user instead (no local password; the username is their directory
  username).
- **Per user**: change the role, reset the password (set one, or generate one shown once), disable /
  enable (disabling signs them out everywhere: web sessions and devices), delete (asks to confirm),
  see and revoke their devices and web sessions, and see / remove their personal providers (only
  whether a key is set; admins can neither read nor use personal keys).
- **Rules**: the last active admin cannot be demoted, disabled or deleted, and nobody can disable or
  delete themselves. To hand over, make someone else an admin first.

### LDAP / Active Directory

Configured in the same section (stored in the server database; the search account's password is
encrypted with a key derived from `HARNESS_SECRET` and never shown again; there are no environment
variables for it). Fill in, then use **Test connection** and **Test user lookup** (they test the form
as it is, before saving), then enable and save.

| Field | Notes |
|---|---|
| Servers | `ldap://` or `ldaps://` URLs, tried in order (`ldaps://dc1.corp.example.com:636`) |
| StartTLS | upgrades `ldap://` connections; use either this or `ldaps://` |
| CA certificate | PEM of a private CA; empty uses the system roots. "Skip certificate check" exists for testing only: it lets anyone on the path read passwords |
| Search account | bind DN + password used to find users (`cn=harness,ou=services,dc=example,dc=com`, AD also takes `harness@corp.example.com`); empty searches anonymously |
| User search | base DN (`ou=people,dc=example,dc=com`) and filter. `{{username}}` is replaced by the login, escaped (RFC 4515). OpenLDAP: `(\|(uid={{username}})(mail={{username}}))`; AD: `(&(objectClass=user)(\|(sAMAccountName={{username}})(userPrincipalName={{username}})(mail={{username}})))` |
| Attributes | username / email / display name: `uid` / `mail` / `cn` (AD: `sAMAccountName` / `mail` / `displayName`) |
| Allowed group | only members may sign in (checked via `memberOf`, else the group's `member`, `uniqueMember` or `memberUid`) |
| Admin group | members become admins and everyone else users, re-checked at every sign-in (the last admin is never demoted). Empty: roles are managed in Users |
| Group to role rules | group DN to role, in order, first match wins and no match is a plain user; the admin group is checked first. Re-checked at every sign-in. Directories without `memberOf` work too: mapped groups are looked at one by one |
| Create accounts | on first sign-in (default on). Off: add directory users under Users first ("Signs in through LDAP") |
| Local accounts | **Everyone, when the directory refuses**: LDAP first, then local passwords. **Only the break-glass admins**: local passwords only for the listed usernames / emails (they still work while the directory is down); everyone else signs in through LDAP only |

Sign-in: while LDAP is enabled, the sign-in page (`/device`, used by the apps and the CLI) sends the
email-or-username and password to `POST /api/auth/sign-in/ldap`. The server finds the user with the
search account (exactly one match), binds as them with their password, creates or updates the local
account (marked LDAP, no local password; name, email and admin-group role follow the directory) and
issues the normal session, so devices, tokens and everything after sign-in work as for local accounts.
A directory user whose email or username already belongs to a local account is refused rather than
merged: delete or rename the local account first. Disabled users stay disabled whatever the directory
says. Failed sign-ins are limited per login (`HARNESS_LOGIN_FAILURES_PER_15MIN`, default 10 per 15
minutes) and per client address (five times that); set `HARNESS_TRUST_PROXY=true` behind a reverse
proxy so the address is the client's. Passwords are never logged.

Before switching to break-glass mode, add your own admin login to the list, or keep a local admin
created with `HARNESS_ADMIN_EMAIL` / `HARNESS_ADMIN_PASSWORD` listed there.

### Roles

| Role | May |
|---|---|
| Admin | Everything. |
| Auditor | Read users, devices, the audit log, usage, quotas, policy and sign-in settings. Change nothing. Secrets stay write-only for them as for everyone. |
| Billing | Read usage and the user and group lists, set quotas. No sign-in settings, policy or audit log. |
| User | No administration. |

Every role uses the product the same way. The JSON API under `/api/admin` and every RPC go through the same table.

### Single sign-on (OIDC, SAML)

**Settings → SSO**, next to LDAP. Add a provider, and **Test** checks the form before anything is saved: OIDC discovery, the
code flow and signing keys, or the SAML sign-in URL and certificate. Save, register what it shows at your identity provider
(redirect URI, or ACS URL, entity ID and metadata URL), and Test again for a link that runs one real sign-in and shows what the
provider sent and the role that gives, creating nothing.

Enabled providers become buttons on the sign-in page. That page is the one the desktop app and the CLI open for their device
flow, so no client needs anything else; `/device?return=/` brings the web UI back to itself.

- **OIDC**: authorization code with PKCE. ID tokens are checked against the provider's keys (issuer, audience, nonce, expiry;
  asymmetric signatures only). With a client secret it is sent by `client_secret_basic` if the provider offers it. Issuers must
  be HTTPS (HTTP only for localhost).
- **SAML**: HTTP-Redirect out, HTTP-POST back. The assertion must be signed by the certificate you gave and answer a request
  from here; provider-initiated sign-in is refused and a response works once. Requests are not signed and assertions cannot be
  encrypted. Replace the certificate here when your provider rolls its key.
- **Accounts**: created at first sign-in unless turned off, then linked by the provider's subject. An account with the same email
  is linked only if it has no local password (one an admin or SCIM made for this); one with a password is refused, never taken
  over. Disabled users stay disabled.
- **Group to role rules** are re-checked at every sign-in; the last active admin is never demoted. Groups the provider reports
  become groups here, so quotas can be set on them. Claim and attribute names can be changed; the groups claim may be a dotted path
  (`realm_access.roles`).
- **Require SSO** refuses local passwords and LDAP except for the break-glass logins you list. It needs an enabled provider and
  at least one login.

### Second factor

Everyone can turn on an authenticator app (TOTP) in **Settings → Account**, and gets ten one-time recovery codes. The secret is
encrypted at rest and the codes are stored hashed. It is asked for on the sign-in page after any way of signing in, so approving a
device needs it too.

**Settings → SSO → Two-factor sign-in** requires it, for everyone or by role. People without one enroll at their next sign-in, and
their connected devices stop refreshing tokens (within an hour) until they did. Turn it on for your own account first: the server
refuses a requirement that would lock out the admin setting it. "Trust the identity provider" does not ask people who come in
through SSO. **Settings → Users** resets someone's factor when they lost it. Wrong codes count like wrong passwords
(`HARNESS_LOGIN_FAILURES_PER_15MIN`, per user).

### SCIM

**Settings → Provisioning** gives the base URL (`$HARNESS_PUBLIC_URL/scim/v2`) and bearer tokens, shown once and stored hashed.
Users and groups: create, replace, patch, delete, with `eq` filters on `userName`, `externalId`, `emails.value` and `displayName`.
PATCH takes both dialects Okta and Azure AD use. Setting `active` to false disables the account and signs it out everywhere;
delete removes it with everything it owns. The last admin is refused. Provisioned accounts have no password and sign in through
SSO, which links them by email; an account that already has the address is adopted. Groups from SCIM can carry quotas, and rules
map their names to roles as membership changes. Bulk and sorting are not offered, and ServiceProviderConfig says so.

### Quotas

**Settings → Users**: spend limits per user (open a user), per group and per role, and a budget for one session. For a user, their
own limit applies, else the strictest of their groups', else their role's. Admins and billing set them, auditors read them;
`/api/admin/quotas` remains for scripts. A user's personal providers are a different thing: their spend counts towards no quota,
and neither their limits nor what they used them for is shown to anyone else.

## Audit log and telemetry

**Settings → Audit log** (admins and auditors) lists sign-ins and failed ones, device approvals, changes to users, roles, groups,
quotas, policy, sign-in settings and SCIM, provider key changes and provisioning, with filters and CSV export. It says that a key or
password changed, never what to: only named fields go in, and whatever looks like a credential is removed on the way. Events are kept
until `HARNESS_AUDIT_RETENTION_DAYS` (unset: forever).

For scripts, with an access token and a role that may read them:

```bash
curl -H "Authorization: Bearer $TOKEN" "https://harness.example.com/api/admin/audit.csv?action=policy&from=2026-11-01T00:00:00Z"
curl -H "Authorization: Bearer $TOKEN" "https://harness.example.com/api/admin/usage.csv?from=2026-11-01T00:00:00Z"
```

An export stops at 50,000 rows or 12 MB and says so (`x-harness-truncated`, and on the screens). Usage CSV is also in **Settings →
Usage** and per user. Someone's records on their personal provider appear for others with tokens only, without model, session or cost.

| Variable | Notes |
|---|---|
| `HARNESS_AUDIT_WEBHOOK_URL` | POST each batch of events as a JSON array; retried until delivered |
| `HARNESS_AUDIT_WEBHOOK_SECRET` | Signs the body: `x-harness-signature: sha256=<HMAC-SHA256 hex>` |
| `HARNESS_AUDIT_SYSLOG_URL` | `udp://host:514`, `tcp://host:601` or `tls://host:6514`: RFC 5424, one message per event, facility local0, octet counting over TCP and TLS |
| `HARNESS_AUDIT_RETENTION_DAYS` | Delete older events daily |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Turns on OpenTelemetry over OTLP/HTTP. Also `_TRACES_ENDPOINT`, `_METRICS_ENDPOINT`, `_HEADERS`, `_PROTOCOL` (`http/json` or `http/protobuf`) |
| `OTEL_SERVICE_NAME`, `OTEL_METRIC_EXPORT_INTERVAL` | Standard meanings; default service `harness-core`, 60 s |
| `OTEL_TRACES_EXPORTER=none`, `OTEL_METRICS_EXPORTER=none`, `OTEL_SDK_DISABLED=true` | Turn parts off |

Forwarding reads the audit table with a stored position per sink, so what is written while a collector is down, or across a restart,
goes out afterwards; a consumer dedupes by `id`. It starts at the current end of the log when first turned on.

Traces: a span per RPC, continuing a caller's `traceparent`. Metrics: `harness.rpc.duration`, `harness.llm.requests`, `.tokens`, `.cost`,
`.duration`, `harness.auth.sign_ins`, `harness.audit.events`. Attributes are names and outcomes: never prompts, keys or email
addresses, and a personal provider shows as `personal`.

## Managed policy

**Settings → Managed policy**: what an admin sets for everyone, above every user, project and local setting. It is stored in the
org POLICY document `default`, so clients get it through their sync feed and keep the last one offline. People see what applies to
them, marked as set by their administrator, in the same section and under Sessions, Models and Sandbox.

| Setting | Enforced by |
|---|---|
| Permission rules: deny, ask, allow; "only these rules" | The client: commands run there |
| Sandbox required; never outside it | The client. On a platform without a sandbox, required means commands are refused |
| Allowed providers (id or preset), no personal providers | The server: the catalog and the gateway. On the client, `local` allows local models and `acp` or `acp:<id>` external agents |
| Allowed MCP servers | The server: the catalog, proxy and stdio launches; the client will not register others from a repository's `.mcp.json` |

A modified client can ignore what only a client can enforce. Rules and the sandbox take effect at a session's next start; modes
switch live.

## Volumes

Everything stateful is under `/data` (declared as a volume):

| Path | Owner | Content |
|---|---|---|
| `/data/postgres` | `postgres`, mode 0700 | the Postgres cluster (`initdb` on first start) |
| `/data/core` | `harness` | core's secret, files, SQLite database (until the Postgres move) |
| `/data/objects` | `harness` | the object store: Garage's metadata and data, its keys and RPC secret |
| `/data/searxng` | `harness` | `secret_key` (generated once); optional `settings.yml` override |

Ownership is fixed at every start (only when wrong), so a fresh named volume or a host directory
created by root works. Do not mount the same volume into two running containers.

SearXNG settings: the image ships `/etc/harness/searxng/settings.yml` (JSON format on, limiter off).
To customize, copy it to `/data/searxng/settings.yml`, keep `search.formats` containing `json`, and
restart the container.

The bundled SearXNG is the built-in web search provider and the default. Admins can connect another one
(Brave Search, Tavily, Exa, Kagi, Perplexity, or a SearXNG instance) under **Settings → Search** and
make it the default for everyone; each user may pick another one for themselves there, including one
connected with their own key.

## Object store

All user files live here: what users upload (attachments, images in messages, avatars) and what the model made for itself
(scratch files, saved tool results, screenshots, `SendUserFile`). Core owns it: every read and write goes through core's
auth checks (`harness.objects.v1.ObjectsService`, `GET /api/objects/<sha256>`), nothing else talks to the S3 port, and it
listens on loopback inside the container. The keys are generated by `init-data` on first start into
`/data/objects/s3-credentials` and are not printed, passed by a client or exposed by any API.

Why Garage (checked November 2026, for a single node on linux/amd64 and linux/arm64):

| | License | Maintenance | Single-node footprint |
|---|---|---|---|
| **Garage** v2.5.0 | AGPL-3.0 | Active, released 2026-10-24 | One process, a static 66-69 MB binary. 28 MiB resident idle and 36 MiB after our test suite (measured on arm64). `--single-node --default-bucket` sets itself up |
| SeaweedFS 4.52 | Apache-2.0 | Very active, about one release a week | One binary running master, volume server, filer and S3 gateway, with volume files and vacuuming to look after: more moving parts than one bucket needs |
| MinIO | AGPL-3.0 | Repository archived (read-only) since April 2026, and binaries stopped in October 2025 | Out: no fixes, no releases |

Garage does the one thing asked, in the least memory, with the fewest things to operate. SeaweedFS's better license and
its macOS binaries are real advantages, but it is built for scale this deployment does not have (its memory use was not
measured here; the comparison is of what it runs). AGPL obligations: the image ships Garage unmodified and as a separate
process that core talks to over S3, so harness itself stays MIT; the source of the shipped version is at
<https://git.deuxfleurs.fr/Deuxfleurs/garage/src/tag/v2.5.0>. Garage publishes no checksum files, so the Dockerfile's
checksums were computed from the release downloads (sizes agree with Garage's own release list).

Configuration (`/etc/harness/garage/garage.toml`): SQLite for metadata and fsync on, because this node has no replica to
rebuild from, and metadata snapshots every 24 hours. Garage compresses and deduplicates blocks by itself.

| Variable | Default | Notes |
|---|---|---|
| `HARNESS_OBJECTS_MAX_FILE_BYTES` | 1 GiB | largest file core takes |
| `HARNESS_OBJECTS_QUOTA_BYTES` | 10 GiB | per user; negative: unlimited. Per user or role: `objects_bytes` in `/api/admin/quotas` |
| `HARNESS_OBJECTS_SWEEP_MS` | 6 hours | how often orphans are removed; 0: never |
| `HARNESS_S3_BUCKET` | `harness-objects` | |
| `HARNESS_S3_REGION` | `garage` | set it for an external S3 |

Uploads travel in chunks of 8 MiB, one request each. A reverse proxy in front of core has to allow that request size (nginx:
`client_max_body_size 16m`), or uploads fail and clients keep retrying while their files stay local.

Served files: `Content-Disposition: attachment` except for raster images and PDFs, `X-Content-Type-Options: nosniff`, and a
sandboxing CSP on everything. HTML, SVG, scripts and any other unlisted type go out as `application/octet-stream`.

### External S3

`HARNESS_S3_ENDPOINT=https://s3.example.com` with `HARNESS_S3_ACCESS_KEY`, `HARNESS_S3_SECRET_KEY`, `HARNESS_S3_REGION` and
`HARNESS_S3_BUCKET`: the internal store is not started and `/data/objects` is not created. Create the bucket yourself; it must
allow path-style requests and multipart uploads, and it should not be public. Moving an existing store over: copy the
bucket's objects with their keys (`u/<owner>/<sha256>`), then switch; the database needs no change.

## Operations

```bash
docker exec -it harness psql -U harness harness       # SQL shell (as the app role)
docker exec -it harness psql -U postgres              # superuser
docker exec harness /command/s6-rc -a list           # running services
docker exec harness /command/s6-svc -r /run/service/core   # restart core only
docker exec -e GARAGE_RPC_SECRET_FILE=/data/objects/rpc_secret harness garage -c /etc/harness/garage/garage.toml status   # object store
docker logs -f harness                                # all four processes
```

## Backups

A backup now has **three** parts, and they belong together: the database, core's files, and the object store. The
database says which objects exist and who owns them; the store has the bytes. Restoring one without the other gives
records whose bytes are gone (those files answer 404) or bytes nothing refers to (the sweep deletes them after a day).

```bash
docker exec harness pg_dump -U harness -Fc harness > harness-$(date +%F).dump
docker exec harness tar -C /data -cf - core searxng > harness-files-$(date +%F).tar
# The object store: all of /data/objects, keys included. Stop it for the copy, or it may not be consistent.
docker exec harness /command/s6-svc -d /run/service/core
docker exec harness /command/s6-svc -d /run/service/objects
docker exec harness tar -C /data -cf - objects > harness-objects-$(date +%F).tar
docker exec harness /command/s6-svc -u /run/service/objects
docker exec harness /command/s6-svc -u /run/service/core
```

Take the database first and the store second, close together: a store newer than its database only has extra bytes, while
the other way around loses files. Clients keep what they have not uploaded yet and upload it again, but what they already
uploaded exists only here. With an external S3, back up its bucket instead of `/data/objects`.

Restore into a fresh volume: start the container once (it creates an empty `harness` database),
stop core, restore, start core:

```bash
docker exec harness /command/s6-svc -d /run/service/core
docker exec -i harness pg_restore -U harness -d harness --clean --if-exists < harness-2026-11-01.dump
docker exec -i harness tar -C /data -xf - < harness-files-2026-11-01.tar
docker exec harness /command/s6-svc -d /run/service/objects
docker exec harness rm -rf /data/objects
docker exec -i harness tar -C /data -xf - < harness-objects-2026-11-01.tar
docker exec harness sh -c 'chown -R harness:harness /data/core /data/searxng /data/objects'
docker exec harness /command/s6-svc -u /run/service/objects
docker exec harness /command/s6-svc -u /run/service/core
```

Alternatively, back up the whole volume while the container is stopped
(`docker run --rm -v harness-data:/data -v "$PWD":/out debian tar -C / -czf /out/data.tgz data`).
Copying `/data/postgres` from a running container is not a consistent backup.

While core still uses SQLite (`/data/core/harness.db`), back that file up with the container stopped,
or with `sqlite3 .backup` from a host tool.

## Upgrading

```bash
docker pull ghcr.io/xlsft/harness:<new>
docker stop harness && docker rm harness
docker run -d --name harness ... ghcr.io/xlsft/harness:<new>     # same volume, same env
```

core applies its database migrations on startup. Take a `pg_dump` first. The server doesn't update itself: **Settings →
About** in the app shows the running version, whether a newer one exists, and (for admins) these commands. Clients one
version behind keep working: core refuses a client only below `HARNESS_MIN_CLIENT_VERSION`, which is unset unless you set it.

Postgres major versions: the image pins Postgres 17. If a future image moves to a new major, the
container refuses to start against an older cluster (the log says so) instead of touching it.
Upgrade by dump and restore: `pg_dump` with the old image, start the new image on a fresh volume
(or after moving `/data/postgres` aside), then `pg_restore` as above.

## External Postgres, SearXNG or S3

- `DATABASE_URL=postgres://user:pass@db.example.com:5432/harness` (anything that does not point at
  `/run/postgresql`): the internal Postgres is not started; its service brings itself down and
  `/data/postgres` is not created. Create the database and role yourself; core runs migrations.
- `HARNESS_SEARXNG_URL=https://searx.example.com`: the internal SearXNG is not started. The external
  instance must enable the JSON format (`search.formats: [html, json]`) and allow core's requests
  through its limiter.
- `HARNESS_S3_ENDPOINT`: see "External S3" under "Object store".

## Image notes

- Base: Debian 13 (trixie) slim. Postgres 17 and Python 3.13 are distro packages with Debian
  security updates, glibc suits the prebuilt Python wheels and Node's native modules (libsql), and
  Node is copied from the official `node:24-trixie-slim` image.
- Multi-stage: core is built with pnpm in a Node stage (only `.output`, which carries its traced
  `node_modules`, and `prompts/` are copied); the web UI is generated in another (only its static files are copied, about 14 MB); SearXNG's venv is built in a separate stage from
  wheels only; s6-overlay tarballs are checksum-verified.
- Pinned versions are build args: `NODE_VERSION`, `PNPM_VERSION`, `SEARXNG_COMMIT` (SearXNG has no
  release tags), `S6_OVERLAY_VERSION` and `GARAGE_VERSION` with their checksums, `PG_MAJOR`.
- CI (`.github/workflows/image.yml`) builds `linux/amd64` and `linux/arm64`, and starts the amd64 image once
  (`scripts/e2e/image-smoke.sh`). Only a release ([docs/release.md](../docs/release.md)) pushes it.

## Size and follow-ups

The image is about 1.07 GB uncompressed (about 260 MB compressed) on linux/arm64. The largest
parts are the apt layer (about 376 MB: Postgres 17 pulls in libLLVM 19 at 118 MB and libz3 at
26 MB for JIT, plus perl for postgresql-common, ICU and locales), the SearXNG venv (about 180 MB,
mostly curl_cffi, babel, granian and lxml), and the Node binary (123 MB). Ideas, not yet built and
tested:

- Copy the Postgres binaries and libraries from the official `postgres:17-trixie` image in a
  multi-stage build instead of installing them with apt. That skips apt for Postgres entirely, and
  JIT (`llvmjit*.so`, `bitcode/`) can be left out, saving about 170 MB.
- Or keep apt and run `dpkg --purge --force-depends libllvm19 libz3-4`, delete `llvmjit*` and
  `bitcode/`, and start Postgres with `-c jit=off`. This also saves about 170 MB but leaves dpkg
  with an unmet dependency, which breaks `apt-get install` in images built `FROM` this one.
- Remove `pip` from the SearXNG venv after installing (about 12 MB).
- Use apt cache mounts (`--mount=type=cache,target=/var/cache/apt`) so a slow or flaky mirror
  doesn't re-download everything on each retried build.
- Move the `user` bundle from `s6-rc.d/user` to `user-bundles.d/user`. s6-overlay 3.2.3 logs a
  deprecation warning for the current layout, but it still works.
- Remove the Tor-only SearXNG engines (`ahmia`, `torch`) through `use_default_settings.engines.remove`.
  Without a Tor proxy they log "can't register engine" at startup. The error does no harm.
- Put `/command` on `PATH` so `docker exec harness s6-svc ...` works without the full path.
