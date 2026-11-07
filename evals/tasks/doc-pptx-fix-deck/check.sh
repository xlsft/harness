#!/usr/bin/env bash
# Passes when the slide order and titles are right, nothing dangles and the notes stayed.
set -euo pipefail
python3 -I - deck.pptx <<'PY'
import posixpath, re, sys, zipfile
z = zipfile.ZipFile(sys.argv[1])
names = set(z.namelist())
def fail(m):
    print("FAIL:", m, file=sys.stderr); sys.exit(1)
pres = z.read("ppt/presentation.xml").decode()
rels = dict()
for tag in re.findall(r"<Relationship [^>]*>", z.read("ppt/_rels/presentation.xml.rels").decode()):
    rels[re.search(r'Id="([^"]+)"', tag).group(1)] = re.search(r'Target="([^"]+)"', tag).group(1)
order = re.findall(r'<p:sldId [^>]*r:id="([^"]+)"', pres)
titles, parts = [], []
for rid in order:
    part = posixpath.normpath(posixpath.join("ppt", rels[rid])).lstrip("/")
    if part not in names: fail("a slide part is missing: " + part)
    parts.append(part)
    x = z.read(part).decode()
    m = re.search(r'<p:sp>(?:(?!</p:sp>).)*?type="(?:title|ctrTitle)"(?:(?!</p:sp>).)*?</p:sp>', x, re.S)
    titles.append("".join(re.findall(r"<a:t>([^<]*)</a:t>", m.group(0))) if m else "")
if titles != ["Opening", "Results", "Roadmap"]: fail("slides are %r" % titles)
everything = z.read("[Content_Types].xml").decode()
for override in re.findall(r'PartName="/([^"]+)"', everything):
    if override not in names: fail("a content type names a missing part: " + override)
for n in names:
    if not n.endswith(".rels"): continue
    base = posixpath.dirname(posixpath.dirname(n))
    for tag in re.findall(r"<Relationship [^>]*>", z.read(n).decode()):
        if "External" in tag: continue
        t = re.search(r'Target="([^"]+)"', tag).group(1)
        full = t.lstrip("/") if t.startswith("/") else posixpath.normpath(posixpath.join(base, t))
        if full not in names: fail(n + " points to a missing part: " + full)
blob = "".join(z.read(n).decode() for n in names if "notesSlide" in n and n.endswith(".xml"))
if "Mention the budget" not in blob: fail("the notes of the Roadmap slide went away")
if "Remove me" in blob: fail("the notes of the deleted slide stayed")
print("ok")
PY
