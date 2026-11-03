# harness server image

One container, three processes under [s6-overlay](https://github.com/just-containers/s6-overlay) v3
(docs/PLAN.md §1.7). No sidecars, no Docker-in-Docker, no privileged mode.

| Process | What | Listens on | Runs as |
|---|---|---|---|
| `core` | Nitro server from `packages/core` (Node 24) | `:3000` (public) | `harness` |
| `postgres` | Postgres 17, data in `/data/postgres` | unix socket `/run/postgresql` only | `postgres` |
| `searxng` | SearXNG (pinned upstream commit) under granian | `127.0.0.1:8888` only | `harness` |

Start order: a one-shot `init-data` lays out `/data` and initializes the cluster, then `postgres`
starts, a one-shot `postgres-ready` waits for `pg_isready` and creates the `harness` role and
database if missing, then `core` starts. `searxng` only waits for `init-data`. s6 restarts any
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
  harness/server
```

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
docker build -f deploy/Dockerfile -t harness/server .
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
| `PGHOST` | `/run/postgresql` | so `psql` / `pg_dump` in `docker exec` find the socket |

`DATABASE_URL` form: an empty host plus a `host=` query parameter that is a directory selects a
unix socket. node-postgres (`pg`, via `pg-connection-string`), postgres.js and libpq all accept this
form. The equivalent percent-encoded form `postgres://harness@%2Frun%2Fpostgresql/harness` also
works and is also recognized as internal. No password: Postgres authenticates socket connections
by peer (OS user `harness` → role `harness`).

Passed through to core (see `packages/core`): `HARNESS_SECRET`, `HARNESS_ADMIN_EMAIL`,
`HARNESS_ADMIN_PASSWORD`, `HARNESS_ALLOW_SIGNUP`, `HARNESS_TRUST_PROXY`, `HARNESS_GRPC_PORT`
(publish that port too if you set it), `HARNESS_SESSION_BUDGET_USD`, and
the other `HARNESS_*` tuning variables.

> Today's core still stores its data in SQLite (`HARNESS_DB_URL`, default
> `file:/data/core/harness.db`) and ignores `DATABASE_URL`. The internal Postgres runs anyway, so
> the same image and volume keep working when core moves to Postgres.

## Users and LDAP

Admins manage accounts in the app: **Settings → Users** (the section is shown to admins only; the
server refuses everyone else, `harness.auth.v1.AdminService`).

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

## Volumes

Everything stateful is under `/data` (declared as a volume):

| Path | Owner | Content |
|---|---|---|
| `/data/postgres` | `postgres`, mode 0700 | the Postgres cluster (`initdb` on first start) |
| `/data/core` | `harness` | core's secret, files, SQLite database (until the Postgres move) |
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

## Operations

```bash
docker exec -it harness psql -U harness harness       # SQL shell (as the app role)
docker exec -it harness psql -U postgres              # superuser
docker exec harness /command/s6-rc -a list           # running services
docker exec harness /command/s6-svc -r /run/service/core   # restart core only
docker logs -f harness                                # all three processes
```

## Backups

Logical backup of the database plus core's files:

```bash
docker exec harness pg_dump -U harness -Fc harness > harness-$(date +%F).dump
docker exec harness tar -C /data -cf - core searxng > harness-files-$(date +%F).tar
```

Restore into a fresh volume: start the container once (it creates an empty `harness` database),
stop core, restore, start core:

```bash
docker exec harness /command/s6-svc -d /run/service/core
docker exec -i harness pg_restore -U harness -d harness --clean --if-exists < harness-2026-11-01.dump
docker exec -i harness tar -C /data -xf - < harness-files-2026-11-01.tar
docker exec harness sh -c 'chown -R harness:harness /data/core /data/searxng'
docker exec harness /command/s6-svc -u /run/service/core
```

Alternatively, back up the whole volume while the container is stopped
(`docker run --rm -v harness-data:/data -v "$PWD":/out debian tar -C / -czf /out/data.tgz data`).
Copying `/data/postgres` from a running container is not a consistent backup.

While core still uses SQLite (`/data/core/harness.db`), back that file up with the container stopped,
or with `sqlite3 .backup` from a host tool.

## Upgrading

```bash
docker pull harness/server:<new>
docker stop harness && docker rm harness
docker run -d --name harness ... harness/server:<new>     # same volume, same env
```

core applies its database migrations on startup. Take a `pg_dump` first.

Postgres major versions: the image pins Postgres 17. If a future image moves to a new major, the
container refuses to start against an older cluster (the log says so) instead of touching it.
Upgrade by dump and restore: `pg_dump` with the old image, start the new image on a fresh volume
(or after moving `/data/postgres` aside), then `pg_restore` as above.

## External Postgres or SearXNG

- `DATABASE_URL=postgres://user:pass@db.example.com:5432/harness` (anything that does not point at
  `/run/postgresql`): the internal Postgres is not started; its service brings itself down and
  `/data/postgres` is not created. Create the database and role yourself; core runs migrations.
- `HARNESS_SEARXNG_URL=https://searx.example.com`: the internal SearXNG is not started. The external
  instance must enable the JSON format (`search.formats: [html, json]`) and allow core's requests
  through its limiter.

## Image notes

- Base: Debian 13 (trixie) slim. Postgres 17 and Python 3.13 are distro packages with Debian
  security updates, glibc suits the prebuilt Python wheels and Node's native modules (libsql), and
  Node is copied from the official `node:24-trixie-slim` image.
- Multi-stage: core is built with pnpm in a Node stage (only `.output`, which carries its traced
  `node_modules`, and `prompts/` are copied); SearXNG's venv is built in a separate stage from
  wheels only; s6-overlay tarballs are checksum-verified.
- Pinned versions are build args: `NODE_VERSION`, `PNPM_VERSION`, `SEARXNG_COMMIT` (SearXNG has no
  release tags), `S6_OVERLAY_VERSION` with its checksums, `PG_MAJOR`.
- CI (`.github/workflows/image.yml`) builds `linux/amd64` and `linux/arm64`; it pushes to
  `ghcr.io/<owner>/harness-server` only for `v*` tags.

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
