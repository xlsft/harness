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

export interface SessionDiff {
  // Equal versions mean nothing changed.
  version: string
  // The request's ifNoneMatch was still current: files is empty and the caller keeps what it has.
  notModified: boolean
  files: SessionFileDiff[]
  truncated: boolean
  // Past the client's thresholds: files start collapsed.
  large?: boolean
  filesChanged: number
  additions: number
  deletions: number
}

interface LoaderOptions {
  fetch: (ifNoneMatch: string) => Promise<SessionDiff>
  // A new state of the session's changes (never for a repeat of the current one).
  onChange: (diff: SessionDiff) => void
  // A failed fetch; the last state remains valid.
  onError: (error: unknown) => void
}

/**
 * Fetches for the Diff pane: one request at a time (asking during one fetches once more after it), an answer that
 * did not change anything is dropped, and an answer for an older load() generation is ignored (reset).
 */
export class SessionDiffLoader {
  private version = ''
  private running: Promise<void> | null = null
  private again = false
  private generation = 0

  constructor(private opts: LoaderOptions) {}

  // Forget the state: the next answer counts as new, whatever is in flight is dropped. `known` is a state already
  // on show (the pane reopening).
  reset(known = '') {
    this.generation++
    this.version = known
    this.running = null
    this.again = false
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
      const diff = await this.opts.fetch(this.version)
      if (generation !== this.generation || diff.notModified || diff.version === this.version) return
      this.version = diff.version
      this.opts.onChange(diff)
    } catch (e) {
      if (generation === this.generation) this.opts.onError(e)
    }
  }
}

/** A deterministic version for a state the mock transport computes (the client hashes its response). */
export function diffVersion(files: SessionFileDiff[], truncated = false): string {
  let h = 5381
  const text = JSON.stringify([files, truncated])
  for (let i = 0; i < text.length; i++) h = (h * 33 + text.charCodeAt(i)) >>> 0
  return h.toString(16)
}
