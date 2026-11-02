# Shared helpers for the s6 service scripts. Sourced, not executed.

PG_MAJOR=17
PGDATA=/data/postgres
PG_SOCKET_DIR=/run/postgresql

log() { printf '[%s] %s\n' "${LOG_TAG:-harness}" "$*" >&2; }

# The internal Postgres runs unless DATABASE_URL points somewhere other than its unix socket.
internal_postgres() {
  case "${DATABASE_URL:-}" in
    '') return 0 ;;
    *host=/run/postgresql* | *%2Frun%2Fpostgresql* | *%2frun%2fpostgresql*) return 0 ;;
    *) return 1 ;;
  esac
}

# The internal SearXNG runs unless HARNESS_SEARXNG_URL points somewhere other than 127.0.0.1:8888.
internal_searxng() {
  case "${HARNESS_SEARXNG_URL:-}" in
    '' | http://127.0.0.1:8888 | http://127.0.0.1:8888/ | http://localhost:8888 | http://localhost:8888/) return 0 ;;
    *) return 1 ;;
  esac
}

# ensure_dir <path> <user:group> <mode>: create the directory and fix ownership when it is wrong
# (a fresh named volume is root-owned). The recursive chown runs only on a mismatch.
ensure_dir() {
  mkdir -p "$1"
  if [ "$(stat -c %U:%G "$1")" != "$2" ]; then
    log "fixing ownership of $1 -> $2"
    chown -R "$2" "$1"
  fi
  chmod "$3" "$1"
}
