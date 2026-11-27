// Ready-made scheduled tasks to start from. Picking one only fills the form; nothing is created until the user saves it.
// The instructions are ours and plain: they use what the agent can reach (the folder, git, the code host's CLI, the
// connected issue trackers) and say what to leave alone.
import { tt } from '~/utils/i18n'
import type { IconName } from '~/utils/icons'
import { emptyScheduleDraft, type ScheduleDraft } from './types'

export interface ScheduleTemplate {
  id: string
  icon: IconName
  name: string
  about: string
  prompt: string
  cron: string
  // Each run on a branch of its own, for templates that change files.
  worktree?: boolean
}

export const SCHEDULE_TEMPLATES: ScheduleTemplate[] = [
  {
    id: 'morning-catch-up',
    icon: 'sun',
    name: tt('Morning catch-up'),
    about: tt('What changed since yesterday: commits, pull requests, issues and builds.'),
    cron: '0 9 * * 1-5',
    prompt: `Give me a short catch-up on this repository since the last working day.

- Commits on the default branch: who changed what, grouped by area.
- Pull requests opened, merged or waiting on me.
- Issues assigned to me or mentioning me in the connected trackers.
- Builds or checks that are failing now.

End with the three things that most need my attention today. Do not change any files.`
  },
  {
    id: 'pr-review-digest',
    icon: 'pull-request',
    name: tt('Pull request digest'),
    about: tt('Open pull requests, where each one is stuck and who it waits on.'),
    cron: '30 9 * * 1-5',
    prompt: `List the open pull requests in this repository.

For each one: title, author, age, review state, failing checks and merge conflicts, and who it is waiting on. Put the ones waiting on me first, then the ones stuck longest. Flag any that look abandoned (no activity for a week). Do not comment on or change any pull request.`
  },
  {
    id: 'issue-triage',
    icon: 'tasks',
    name: tt('Issue triage'),
    about: tt('Sort new issues by kind and urgency and suggest who should take them.'),
    cron: '0 10 * * 1-5',
    prompt: `Look at the issues created or reopened since the last run in the connected trackers.

For each: say whether it is a bug, a feature request or a question, how urgent it looks, which part of the code it touches, and who changed that code most recently. Point out duplicates of existing issues. Give the result as a table. Do not edit or comment on the issues.`
  },
  {
    id: 'build-health',
    icon: 'chart',
    name: tt('Build health check'),
    about: tt('Failing and slow pipelines on the default branch, and what broke them.'),
    cron: '0 8 * * *',
    prompt: `Check the CI runs on the default branch from the last day.

Report which jobs failed, since which commit, and the likely cause from their logs. Note jobs that got noticeably slower than usual. If everything is green, say so in one line. Do not re-run or cancel anything.`
  },
  {
    id: 'dependency-check',
    icon: 'shield',
    name: tt('Dependency check'),
    about: tt('Outdated packages, security advisories and upgrades that break things.'),
    cron: '0 9 * * 1',
    prompt: `Check the dependencies of this repository with the package managers it uses.

List packages with known security advisories first, then major-version upgrades, then the rest that are out of date. For each upgrade worth doing, say what changes in its release notes that could break this code. Do not change lock files or manifests.`
  },
  {
    id: 'flaky-tests',
    icon: 'retry',
    name: tt('Flaky test tracker'),
    about: tt('Tests that pass and fail on the same code across recent CI runs.'),
    cron: '30 10 * * 1',
    prompt: `Go through the CI runs of the last week and find tests that both passed and failed on the same commit, or failed and then passed on a re-run.

For each flaky test: how often it failed, the error it fails with, and a guess at the cause (timing, order, shared state, network). Rank them by how often they fail. Do not change the tests.`
  },
  {
    id: 'release-notes',
    icon: 'doc',
    name: tt('Release notes draft'),
    about: tt('A user-facing draft of what changed since the last release tag.'),
    cron: '0 16 * * 5',
    worktree: true,
    prompt: `Draft release notes for everything merged since the latest release tag.

Write for users, not developers: group changes under New, Improved and Fixed, one line each, and leave out internal refactors and test changes. Put the draft in RELEASE_NOTES_DRAFT.md on this run's branch and commit it.`
  },
  {
    id: 'code-health',
    icon: 'code',
    name: tt('Code health sweep'),
    about: tt('New TODOs, dead code and docs that no longer match the code.'),
    cron: '0 11 * * 3',
    prompt: `Review the changes merged in the last week for loose ends.

- TODO, FIXME and HACK comments that were added.
- Functions, files or exports nothing uses any more.
- Docs and README sections that describe behaviour the code no longer has.

List each with its file and line and a one-line suggestion. Do not change any files.`
  }
]

// The form for a template: its fields on top of an empty draft for the folder.
export function draftOfTemplate(t: ScheduleTemplate, cwd = ''): ScheduleDraft {
  return { ...emptyScheduleDraft(cwd), name: t.name, prompt: t.prompt, trigger: 'cron', cron: t.cron, worktree: t.worktree ?? false }
}
