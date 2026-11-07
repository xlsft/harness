#!/usr/bin/env bash
# Passes when the input changed, the formulas stayed, the hidden sheet stayed and the answer is right.
set -euo pipefail

RESULT="${EVAL_RESULT_FILE:-}"
if [ -z "$RESULT" ] || [ ! -s "$RESULT" ]; then
  echo "FAIL: no final answer text (EVAL_RESULT_FILE is empty or missing)" >&2
  exit 1
fi

answer="$(grep -Eo 'ANSWER:[[:space:]]*[0-9]+' "$RESULT" | tail -n 1 | grep -Eo '[0-9]+' || true)"
if [ "$answer" != "1250" ]; then
  echo "FAIL: expected ANSWER: 1250, got '${answer:-<none>}'" >&2
  exit 1
fi
python3 -I - budget.xlsx <<'PY'
import re, sys, zipfile
z = zipfile.ZipFile(sys.argv[1])
def fail(m):
    print("FAIL:", m, file=sys.stderr); sys.exit(1)
inputs = z.read("xl/worksheets/sheet1.xml").decode()
summary = z.read("xl/worksheets/sheet2.xml").decode()
m = re.search(r'<c r="B2"[^>]*>(.*?)</c>', inputs)
if not m or "<f" in m.group(1) or "<v>125</v>" not in m.group(1): fail("Inputs!B2 is not 125: " + (m.group(1) if m else "missing"))
if "Inputs!B2*Inputs!B3" not in summary: fail("the total is no longer a formula")
if "B1*1.2" not in summary: fail("the tax line is no longer a formula")
stale = re.search(r'<c r="B1"[^>]*><f>[^<]*</f><v>([^<]*)</v>', summary)
wb = z.read("xl/workbook.xml").decode()
if not (stale and float(stale.group(1)) == 1250) and "fullCalcOnLoad" not in wb: fail("the stored total is stale and nothing recalculates it")
if not re.search(r'state="hidden"[^>]*name="Archive"|name="Archive"[^>]*state="hidden"', wb): fail("the Archive sheet is no longer hidden")
if "Do not touch" not in z.read("xl/worksheets/sheet3.xml").decode(): fail("the Archive sheet changed")
print("ok")
PY
