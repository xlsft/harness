# syntax=docker/dockerfile:1
#
# harness server: one container, four processes under s6-overlay (docs/PLAN.md §1.7):
#   core      Nitro server (packages/core), Node 24, public :3000; also serves the web UI (packages/ui)
#   postgres  Postgres 17, unix socket /run/postgresql only, data in /data/postgres
#   searxng   SearXNG under granian, 127.0.0.1:8888 only
#   objects   Garage, the S3 object store, 127.0.0.1:3900 only, data in /data/objects
#
# Build from the repo root:  docker build -f deploy/Dockerfile -t ghcr.io/xlsft/harness .
#
# Base: Debian 13 "trixie" slim. Postgres 17 and Python 3.13 come straight from the distro (with
# Debian security updates, no third-party apt repo), glibc matches the prebuilt wheels and native
# Node modules (libsql) that musl/Alpine would break, and the official node image has a trixie
# variant to copy Node from. Bookworm would need the PGDG repo for Postgres 17 and is past its
# regular security support (oldstable, LTS only since mid-2026).

ARG NODE_VERSION=24.22.2
ARG DEBIAN_TAG=trixie-slim

# ---------------------------------------------------------------------------------------------
# core: pnpm install + nitro build -> .output (self-contained: traced node_modules included)
FROM node:${NODE_VERSION}-trixie-slim AS core-build
ARG PNPM_VERSION=11.8.0
RUN npm install -g --no-fund --no-audit pnpm@${PNPM_VERSION}
WORKDIR /src
COPY packages/core/package.json packages/core/pnpm-lock.yaml packages/core/pnpm-workspace.yaml ./
# More retries and fewer parallel downloads: slow links otherwise time out mid-install.
RUN --mount=type=cache,id=pnpm-store,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile --reporter=append-only \
      --network-concurrency 8 --fetch-retries 6
COPY packages/core/ ./
RUN pnpm build

# ---------------------------------------------------------------------------------------------
# web UI: static files from `nuxt generate`, in the "web" transport (a client to connect to, or the console for what needs
# none). No scripts at install: postinstall's `nuxt prepare` needs the sources, copied after the install for its cache.
FROM node:${NODE_VERSION}-trixie-slim AS ui-build
# packages/ui pins a newer pnpm than core, with a different lockfile format and fewer install flags.
ARG PNPM_UI_VERSION=12.15.0
RUN npm install -g --no-fund --no-audit pnpm@${PNPM_UI_VERSION}
WORKDIR /src
COPY packages/ui/package.json packages/ui/pnpm-lock.yaml packages/ui/pnpm-workspace.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile --ignore-scripts
COPY packages/ui/ ./
RUN NUXT_PUBLIC_TRANSPORT=web pnpm generate

# ---------------------------------------------------------------------------------------------
# searxng: pinned upstream commit (SearXNG publishes no release tags) in a venv under /opt/searxng.
FROM debian:${DEBIAN_TAG} AS searxng-build
ARG SEARXNG_COMMIT=d47cb96bfb79240d487bdb2336886d0dc59f2f30
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get install -y --no-install-recommends ca-certificates git python3 python3-venv \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/searxng
RUN git init -q . \
    && git remote add origin https://github.com/searxng/searxng.git \
    && git fetch -q --depth 1 origin "${SEARXNG_COMMIT}" \
    && git checkout -q FETCH_HEAD
RUN --mount=type=cache,id=pip,target=/root/.cache/pip \
    python3 -m venv .venv \
    && .venv/bin/pip install -q --only-binary=:all: -r requirements.txt -r requirements-server.txt
# Freeze the version (searx/version.py otherwise shells out to git at runtime), then drop
# everything the server does not need.
RUN .venv/bin/python -m searx.version freeze \
    && rm -rf .git client docs tests utils searxng_extra container \
    && find .venv -name '__pycache__' -type d -prune -exec rm -rf {} + \
    && .venv/bin/python -m compileall -q -j 0 searx .venv/lib

# ---------------------------------------------------------------------------------------------
# s6-overlay v3, checksum-verified.
FROM debian:${DEBIAN_TAG} AS s6
ARG S6_OVERLAY_VERSION=3.2.3.2
ARG S6_NOARCH_SHA256=5379750ed30a84bbd2e2dd74847ba6b5bd29cd0b2e3ea2ec58049b57eb2eda12
ARG S6_X86_64_SHA256=e6befcc96a437a3831386ecfc51808c5d3e939dc5fe3c02ae9284599e8aa2408
ARG S6_AARCH64_SHA256=b17f17a82e7a515c682a91edaf2ffdabb73f891981b6c1fd712115693a2f8b4c
ARG TARGETARCH
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) arch=x86_64; sum="${S6_X86_64_SHA256}" ;; \
      arm64) arch=aarch64; sum="${S6_AARCH64_SHA256}" ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    base="https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}"; \
    curl -fsSLo /tmp/noarch.tar.xz "${base}/s6-overlay-noarch.tar.xz"; \
    curl -fsSLo /tmp/arch.tar.xz "${base}/s6-overlay-${arch}.tar.xz"; \
    echo "${S6_NOARCH_SHA256}  /tmp/noarch.tar.xz" | sha256sum -c -; \
    echo "${sum}  /tmp/arch.tar.xz" | sha256sum -c -; \
    mkdir /s6root; \
    tar -C /s6root -Jxpf /tmp/noarch.tar.xz; \
    tar -C /s6root -Jxpf /tmp/arch.tar.xz

# ---------------------------------------------------------------------------------------------
# Garage (object store): the project's static musl binary, pinned and checksum-verified. Upstream publishes no checksum
# files, so these were computed from the release downloads; why Garage: deploy/README.md.
FROM debian:${DEBIAN_TAG} AS garage
ARG GARAGE_VERSION=v2.5.0
ARG GARAGE_X86_64_SHA256=2ff667286217cd73ebcb1c03af993822365000441dfe31a80cb64c5189a9dea8
ARG GARAGE_AARCH64_SHA256=69b6040cef428b993500c17dd0d0c1240acf2f3f667b9b05ceba4b9660db4e8c
ARG TARGETARCH
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) arch=x86_64; sum="${GARAGE_X86_64_SHA256}" ;; \
      arm64) arch=aarch64; sum="${GARAGE_AARCH64_SHA256}" ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/garage "https://garagehq.deuxfleurs.fr/_releases/${GARAGE_VERSION}/${arch}-unknown-linux-musl/garage"; \
    echo "${sum}  /tmp/garage" | sha256sum -c -; \
    install -D -m 0755 /tmp/garage /garage/usr/local/bin/garage

# ---------------------------------------------------------------------------------------------
FROM node:${NODE_VERSION}-trixie-slim AS node

# ---------------------------------------------------------------------------------------------
FROM debian:${DEBIAN_TAG}

ARG PG_MAJOR=17
# postgresql-common must not create a default cluster: ours is initialized into /data/postgres.
RUN set -eux; \
    apt-get -o Acquire::Retries=5 update; \
    apt-get install -y --no-install-recommends postgresql-common; \
    sed -ri 's/#?\s*create_main_cluster\s*=.*/create_main_cluster = false/' /etc/postgresql-common/createcluster.conf; \
    apt-get install -y --no-install-recommends \
      ca-certificates locales python3 tzdata "postgresql-${PG_MAJOR}"; \
    rm -rf /var/lib/apt/lists/* /var/cache/debconf/*-old /usr/share/doc/* /usr/share/man/*; \
    # harness runs core and SearXNG; postgres (from the package) runs the database.
    groupadd --system --gid 1000 harness; \
    useradd --system --uid 1000 --gid harness --home-dir /nonexistent --no-create-home \
      --shell /usr/sbin/nologin harness

COPY --from=s6 /s6root/ /
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=searxng-build /opt/searxng /opt/searxng
COPY --from=garage /garage/ /
COPY --from=core-build /src/.output /app
COPY --from=core-build /src/prompts /app/prompts
COPY --from=ui-build /src/.output/public /app/web
COPY deploy/rootfs/ /

# The release workflow passes the tag's version; /api/health reports it. Set last so it costs no cached layer.
ARG HARNESS_VERSION=0.0.0-dev

ENV PATH="/usr/lib/postgresql/${PG_MAJOR}/bin:${PATH}" \
    HARNESS_VERSION="${HARNESS_VERSION}" \
    NODE_ENV=production \
    PORT=3000 \
    HARNESS_DATA_DIR=/data/core \
    HARNESS_PROMPTS_DIR=/app/prompts \
    HARNESS_WEB_DIR=/app/web \
    HARNESS_PUBLIC_URL=http://localhost:3000 \
    HARNESS_SEARXNG_URL=http://127.0.0.1:8888 \
    HARNESS_S3_ENDPOINT=http://127.0.0.1:3900 \
    HARNESS_S3_CREDENTIALS_FILE=/data/objects/s3-credentials \
    DATABASE_URL="postgres://harness@/harness?host=/run/postgresql" \
    PGHOST=/run/postgresql \
    # s6-overlay: wait as long as first-start initdb needs, stop the container if init fails,
    # and give services time to stop on `docker stop` (core drains, Postgres fast-shutdown).
    S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0 \
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2 \
    S6_KILL_GRACETIME=3000 \
    S6_SERVICES_GRACETIME=9000 \
    S6_VERBOSITY=1

VOLUME ["/data"]
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD ["node", "-e", "fetch('http://127.0.0.1:3000/api/health').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]

ENTRYPOINT ["/init"]
