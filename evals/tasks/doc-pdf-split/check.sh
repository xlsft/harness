#!/usr/bin/env bash
# Passes when excerpt.pdf has the two pages, report.pdf is untouched and the code is right.
set -euo pipefail
if [ -n "$(git status --porcelain -- report.pdf)" ]; then
  echo "FAIL: report.pdf changed" >&2
  exit 1
fi

RESULT="${EVAL_RESULT_FILE:-}"
if [ -z "$RESULT" ] || [ ! -s "$RESULT" ]; then
  echo "FAIL: no final answer text (EVAL_RESULT_FILE is empty or missing)" >&2
  exit 1
fi

if ! grep -Eq 'ANSWER:[[:space:]]*ORCHID-7' "$RESULT"; then
  echo "FAIL: expected ANSWER: ORCHID-7" >&2
  exit 1
fi
python3 -I - excerpt.pdf <<'PY'
import re, sys, zlib
data = open(sys.argv[1], "rb").read()
def fail(m):
    print("FAIL:", m, file=sys.stderr); sys.exit(1)
if not data.startswith(b"%PDF-"): fail("not a PDF")
blobs = [data]
for m in re.finditer(rb"stream\r?\n(.*?)\r?\nendstream", data, re.S):
    try:
        blobs.append(zlib.decompress(m.group(1)))
    except zlib.error:
        blobs.append(m.group(1))
whole = b"\n".join(blobs)
pages = len(re.findall(rb"/Type\s*/Page(?![a-zA-Z])", whole))
if pages != 2: fail("expected 2 pages, found %d" % pages)
if b"Part 2" not in whole or b"Part 3" not in whole: fail("the wrong pages")
if b"Part 1" in whole or b"Part 4" in whole: fail("extra pages")
print("ok")
PY
