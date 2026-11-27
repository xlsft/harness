#!/usr/bin/env bash
# Regenerates the device management templates in deploy/mdm from packages/client/settings/policy.schema.json.
# The client's tests (internal/policytemplates) fail while the committed files differ from what the schema generates.
#
# Usage: scripts/policy-templates.sh           write them
#        scripts/policy-templates.sh --check   only say whether they are current (exit 1 if not)
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "${1:-}" == --check ]]; then
  exec go -C "$root/packages/client" test -count=1 -run 'TestTemplatesAreCurrent' ./internal/policytemplates
fi
go -C "$root/packages/client" run ./internal/policytemplates/generate "$root/deploy/mdm"
