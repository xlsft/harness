#!/usr/bin/env bash
# Passes when RELEASE.md carries the artifact's real SHA-256 and size and nothing else changed.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ART=dist/harness-demo-1.4.2.tar.gz
git diff --quiet -- "$ART" || { echo "FAIL: the artifact was modified" >&2; exit 1; }
read -r sum size < <(python3 -c 'import hashlib,os,sys; p=sys.argv[1]; print(hashlib.sha256(open(p,"rb").read()).hexdigest(), os.path.getsize(p))' "$ART")
expected="$(sed -e "s/^- sha256: TODO$/- sha256: $sum/" -e "s/^- size: TODO bytes$/- size: $size bytes/" "$TASK_DIR/repo/RELEASE.md")"
if [ "$(cat RELEASE.md)" != "$expected" ]; then
  echo "FAIL: RELEASE.md does not match the expected content:" >&2
  diff <(echo "$expected") RELEASE.md >&2 || true
  exit 1
fi
echo "ok: sha256 $sum, $size bytes"
