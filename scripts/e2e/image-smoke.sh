#!/usr/bin/env bash
# Starts a built server image once: every process comes up, /api/health answers with the expected version, and the
# checksum-pinned binaries (s6-overlay, Garage) run on this architecture.
#
# Usage: scripts/e2e/image-smoke.sh <image> [expected version]     (needs docker, curl, jq)
set -euo pipefail
image="${1:?usage: image-smoke.sh <image> [expected version]}"
want="${2:-0.0.0-dev}"
name="harness-smoke-$$"
trap 'docker logs "$name" 2>&1 | tail -n 40; docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

docker run -d --name "$name" -p 127.0.0.1::3000 -e HARNESS_SECRET="$(openssl rand -base64 48)" "$image" >/dev/null
url="http://$(docker port "$name" 3000/tcp | head -n1)"

for _ in $(seq 1 120); do
  health="$(curl -fsS "$url/api/health" 2>/dev/null || true)"
  [[ -n "$health" ]] && break
  sleep 2
done
[[ -n "${health:-}" ]] || { echo "no health answer from the image" >&2; exit 1; }
got="$(jq -r .version <<<"$health")"
[[ "$got" == "$want" ]] || { echo "the image reports version $got, expected $want" >&2; exit 1; }

docker exec "$name" garage --version
docker exec "$name" /command/s6-rc -a list
docker exec "$name" node --version
echo "image ok: $image reports $got"
