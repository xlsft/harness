#!/usr/bin/env bash
# Passes when the name changed in every story, formatting stayed and every other part keeps its bytes.
set -euo pipefail
TASK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 -I - "$TASK_DIR/hidden/contract.docx" contract.docx <<'PY'
import re, sys, zipfile
base, cur = zipfile.ZipFile(sys.argv[1]), zipfile.ZipFile(sys.argv[2])
def fail(m):
    print("FAIL:", m, file=sys.stderr); sys.exit(1)
if cur.testzip() is not None: fail("bad zip")
def text(z, part):
    x = z.read(part).decode()
    return re.findall(r"<w:p[ >].*?</w:p>", x, re.S), "".join(re.findall(r"<w:t(?: [^>]*)?>([^<]*)</w:t>", x))
for part in ("word/document.xml", "word/header1.xml"):
    _, t = text(cur, part)
    if "ACME" in t: fail(part + " still says ACME: " + t)
    if "Globex Ltd" not in t: fail(part + " lacks the new name: " + t)
_, body = text(cur, "word/document.xml")
if body.count("Globex Ltd") != 3: fail("expected the new name three times in the body: " + body)
for keep in ("Services Agreement", "This agreement is between", "and the Client.", "The Client pays within 30 days.", "Signed for", "by its director."):
    if keep not in body: fail("lost: " + keep)
if len(text(cur, "word/document.xml")[0]) != len(text(base, "word/document.xml")[0]): fail("the paragraph count changed")
x = cur.read("word/document.xml").decode()
if "<w:b/>" not in x: fail("the bold formatting of the name is gone")
for name in ("word/styles.xml", "word/media/logo.bin", "word/_rels/document.xml.rels"):
    if base.read(name) != cur.read(name): fail(name + " changed")
print("ok")
PY
