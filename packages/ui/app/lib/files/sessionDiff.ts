// The session's changes as they are on disk (AgentService GetSessionDiff), for the Diff pane: what the client measured,
// edits made by shell commands included, instead of what the transcript's Edit / Write calls say.

import type { FileChange, PatchHunk } from '~/utils/tools'

export interface SessionFileDiff {
  // Absolute, as the session knows it.
  path: string
  // Relative to the cwd or its root; "scratchpad/<name>" in the scratch dir.
  label: string
  kind: FileChange['kind']
  // Renames: the absolute path it had.
  oldPath: string
  add: number
  del: number
  hunks: PatchHunk[]
  binary: boolean
  tooLarge: boolean
}

// What a view compares with (DiffBase in session_diff.proto). The choice is the view's: it is not session state, and
// SessionInfo.diff_stats always counts 'all'.
export type DiffBaseKind = 'all' | 'uncommitted' | 'ref' | 'commit'
export type DiffBaseChoice = { kind: 'all' } | { kind: 'uncommitted' } | { kind: 'ref', ref: string } | { kind: 'commit', sha: string }

export interface SessionDiff {
  // Equal versions mean nothing changed.
  version: string
  // The request's ifNoneMatch was still current: files is empty and the caller keeps what it has. The rest is filled in.
  notModified: boolean
  files: SessionFileDiff[]
  truncated: boolean
  filesChanged: number
  additions: number
  deletions: number
  // What was compared, for showing: "main" and "working tree", say.
  baseLabel: string
  headLabel: string
  // The base in effect: 'all' outside git, whatever was asked for.
  baseKind: DiffBaseKind
  // Files have no hunks: those come per file from sessionFileDiff.
  large: boolean
  // When to ask again, as the client suggests.
  refreshAfterMs: number
}

export interface DiffBranch {
  // For DiffBaseChoice.ref.
  name: string
  current: boolean
  remote: boolean
}

export interface DiffCommit {
  sha: string
  subject: string
  // RFC 3339, UTC.
  committedAt: string
}

// What a view can compare with (AgentService ListDiffBases).
export interface DiffBases {
  // false: only 'all' applies, and the rest is empty.
  inGit: boolean
  // Local branches first, then remote-tracking ones, each most recently committed first; at most 500.
  branches: DiffBranch[]
  branchesTruncated: boolean
  // What 'all' compares with. Ref and sha are empty before the first commit.
  defaultRef: string
  defaultLabel: string
  defaultSha: string
  // The commits on this branch since that base, newest first; at most 200.
  commits: DiffCommit[]
  commitsTruncated: boolean
}

// Until the client has answered.
const DEFAULT_REFRESH_MS = 5000

function sameBase(a: DiffBaseChoice | undefined, b: DiffBaseChoice | undefined): boolean {
  return JSON.stringify(a ?? { kind: 'all' }) === JSON.stringify(b ?? { kind: 'all' })
}

interface LoaderOptions {
  fetch: (ifNoneMatch: string, base: DiffBaseChoice | undefined) => Promise<SessionDiff>
  // A new state of the session's changes (never for a repeat of the current one).
  onChange: (diff: SessionDiff) => void
  // A failed fetch; the last state remains valid.
  onError: (error: unknown) => void
  // The base to start with; setBase changes it.
  base?: DiffBaseChoice
  // Whether a poll that came due should ask now (the page is visible, say). Absent: always.
  shouldPoll?: () => boolean
}

/**
 * Fetches for the Diff pane: one request at a time (asking during one fetches once more after it), an answer that
 * did not change anything is dropped, and an answer for an older load() generation is ignored (reset).
 * start() polls at the pace each answer's refreshAfterMs sets; load() asks at once, whatever the pace.
 */
export class SessionDiffLoader {
  private version = ''
  private running: Promise<void> | null = null
  private again = false
  private generation = 0
  private base: DiffBaseChoice | undefined
  private polling = false
  private timer: ReturnType<typeof setTimeout> | undefined
  // The pace the last answer asked for.
  refreshAfterMs = DEFAULT_REFRESH_MS

  constructor(private opts: LoaderOptions) {
    this.base = opts.base
  }

  // Compare with something else: as a reset, since the state on show is of the old base. The caller then calls load().
  setBase(base: DiffBaseChoice | undefined) {
    if (sameBase(this.base, base)) return
    this.base = base
    this.reset()
  }

  // Forget the state: the next answer counts as new, whatever is in flight is dropped. `known` is a state already
  // on show (the pane reopening).
  reset(known = '') {
    this.generation++
    this.version = known
    this.running = null
    this.again = false
    this.schedule()
  }

  // Polls until stop(): the next ask comes refreshAfterMs after the last answer, whichever call got that answer.
  start() {
    this.polling = true
    this.schedule()
  }

  stop() {
    this.polling = false
    clearTimeout(this.timer)
  }

  private schedule() {
    clearTimeout(this.timer)
    if (!this.polling) return
    this.timer = setTimeout(() => {
      if (this.opts.shouldPoll?.() === false) this.schedule()
      else void this.load()
    }, this.refreshAfterMs)
  }

  load(): Promise<void> {
    if (this.running) {
      this.again = true
      return this.running
    }
    const run = this.run(this.generation).finally(() => {
      if (this.running !== run) return
      this.running = null
      if (this.again) {
        this.again = false
        void this.load()
      }
    })
    this.running = run
    return run
  }

  private async run(generation: number) {
    try {
      const diff = await this.opts.fetch(this.version, this.base)
      if (generation !== this.generation) return
      if (diff.refreshAfterMs > 0) this.refreshAfterMs = diff.refreshAfterMs
      if (diff.notModified || diff.version === this.version) return
      this.version = diff.version
      this.opts.onChange(diff)
    } catch (e) {
      if (generation === this.generation) this.opts.onError(e)
    } finally {
      if (generation === this.generation) this.schedule()
    }
  }
}

/** A deterministic version for a state the mock transport computes (the client hashes its response). */
export function diffVersion(files: SessionFileDiff[], truncated = false, base: DiffBaseKind = 'all'): string {
  let h = 5381
  const text = JSON.stringify([files, truncated, base])
  for (let i = 0; i < text.length; i++) h = (h * 33 + text.charCodeAt(i)) >>> 0
  return h.toString(16)
}
