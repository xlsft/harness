#!/usr/bin/env bash
# Reads a release tag: vX.Y.Z or vX.Y.Z-suffix (such as -rc.2), nothing else. Prints `version=...`, the form $GITHUB_OUTPUT
# takes. Every release is a normal one: a suffix only names it, it makes no pre-release. Refuses anything that doesn't parse, as the tag is the source of truth for every version.
set -euo pipefail
tag="${1:?usage: parse-version.sh <tag>}"
number='(0|[1-9][0-9]*)'
identifier='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
if [[ ! "$tag" =~ ^v$number\.$number\.$number(-$identifier(\.$identifier)*)?$ ]]; then
  echo "\"$tag\" is not a release tag: expected vX.Y.Z or vX.Y.Z-rc.N" >&2
  exit 1
fi
version="${tag#v}"
echo "version=$version"
