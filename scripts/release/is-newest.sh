#!/usr/bin/env bash
# Prints true when <tag> is at least as new as <current> (the release GitHub calls latest now; empty: none yet), by
# semver: 0.1.0-rc.2 < 0.1.0 < 0.1.1. `sort -V` gets the suffix the wrong way round, so it can't decide this.
set -euo pipefail
tag="${1:?usage: is-newest.sh <tag> [current]}"
current="${2:-}"
[[ -z "$current" ]] && { echo true; exit 0; }
python3 - "$tag" "$current" <<'PY'
import re, sys
def key(t):
    m = re.fullmatch(r"v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?", t)
    if not m:
        sys.exit(f"not a version: {t}")
    core = tuple(int(x) for x in m.groups()[:3])
    pre = m.group(4)
    if pre is None:
        return core + ((1,),)
    ids = tuple((0, int(p), "") if p.isdigit() else (1, 0, p) for p in pre.split("."))
    return core + ((0,) + ids,)
print("true" if key(sys.argv[1]) >= key(sys.argv[2]) else "false")
PY
