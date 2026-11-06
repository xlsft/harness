#!/usr/bin/env bash
# A release may only come from a commit whose CI is green: every workflow listed here must have a successful run on that
# exact commit. Its newest run decides, so a failed re-run counts as failed. A missing result or one still running never
# passes: with --wait it is waited for, and when the time is up it fails. There is no way to skip this.
#
# Usage: ci-green.sh <sha> [--wait <seconds>]    (GITHUB_REPOSITORY names the repository; default xlsft/harness; needs gh)
set -euo pipefail
sha="${1:?usage: ci-green.sh <sha> [--wait <seconds>]}"
wait=0
if [[ "${2:-}" == --wait ]]; then wait="${3:?--wait needs seconds}"; fi
repo="${GITHUB_REPOSITORY:-xlsft/harness}"
POLL="${HARNESS_CI_POLL_SECONDS:-30}"
# How long a result that isn't there at all is waited for: only until a push's workflows have had time to start.
MISSING_GRACE=300

REQUIRED=(cli client core desktop e2e image proto ui workflows)

# Prints the state of a workflow's newest run on the commit: success, failure:<conclusion>, running or missing.
state() {
  local answer status conclusion
  # Runs on the commit itself: pull request runs test a merge with main, not this tree.
  answer="$(gh api "repos/$repo/actions/workflows/$1.yml/runs?head_sha=$sha&per_page=100" --jq '
    [.workflow_runs[] | select(.event == "push" or .event == "workflow_dispatch")]
    | sort_by(.created_at) | last | if . == null then "missing" else "\(.status) \(.conclusion)" end')" || {
    echo "unknown"
    return
  }
  status="${answer%% *}"
  conclusion="${answer#* }"
  case "$status" in
    missing) echo missing ;;
    completed) if [[ "$conclusion" == success ]]; then echo success; else echo "failure:$conclusion"; fi ;;
    *) echo running ;;
  esac
}

started="$SECONDS"
while :; do
  bad=()
  waiting=()
  missing=()
  for workflow in "${REQUIRED[@]}"; do
    s="$(state "$workflow")"
    case "$s" in
      success) ;;
      running | unknown) waiting+=("$workflow ($s)") ;;
      missing) missing+=("$workflow") ;;
      *) bad+=("$workflow (${s#failure:})") ;;
    esac
  done
  if ((${#bad[@]} == 0 && ${#waiting[@]} == 0 && ${#missing[@]} == 0)); then
    echo "CI is green on $sha: ${REQUIRED[*]}"
    exit 0
  fi
  elapsed=$((SECONDS - started))
  if ((${#bad[@]} > 0)); then break; fi
  if ((${#waiting[@]} == 0 && elapsed >= MISSING_GRACE)); then break; fi
  if ((elapsed >= wait)); then break; fi
  echo "waiting ($elapsed s): ${waiting[*]:-} ${missing[*]:-}"
  sleep "$POLL"
done

echo "CI isn't green on $sha, so this can't be released:" >&2
for b in ${bad[@]+"${bad[@]}"}; do echo "  failed: $b" >&2; done
for w in ${waiting[@]+"${waiting[@]}"}; do echo "  not finished: $w" >&2; done
for m in ${missing[@]+"${missing[@]}"}; do echo "  no run: $m" >&2; done
if ((${#missing[@]} > 0)); then
  echo "Path filters mean a workflow may not have run on this commit. With main at this commit, run:" >&2
  for m in "${missing[@]}"; do echo "  gh workflow run $m.yml --ref main" >&2; done
fi
exit 1
