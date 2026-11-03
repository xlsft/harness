// How a tool call is labeled: icon, the white "target" and the right-hand meta (Tool.dc.html, Main.dc.html rows).
import { structuredPatch } from 'diff'
import type { ToolItem } from '~/lib/session/types'
import type { IconName } from './icons'
import { showLabel, showPaneTitle, showTargetOf } from './show'

export interface ToolMeta {
  text: string
  cls: string
}

export function mcpParts(name: string) {
  const m = name.match(/^mcp__([^_]+(?:_[^_]+)*?)__(.+)$/)
  return m ? { server: m[1]!, tool: m[2]! } : null
}

export function toolIcon(t: ToolItem): IconName {
  if (t.status === 'running' && t.name !== 'Task' && t.name !== 'Agent') return 'loading'
  if (t.status === 'error' && t.name === 'Bash') return 'x'
  if (mcpParts(t.name)) return 'box'
  switch (t.name) {
    case 'Bash': return 'term'
    case 'Read': return 'file'
    case 'Glob':
    case 'LS': return 'folder'
    case 'Grep': return 'search'
    case 'Edit':
    case 'MultiEdit':
    case 'Write': return 'edit'
    case 'WebSearch':
    case 'WebFetch': return 'web-search'
    case 'Task':
    case 'Agent': return 'agent'
    case 'Show': return t.input.target === 'url' ? 'globe' : t.input.target === 'diff' ? 'diff' : 'file'
    default: return 'box'
  }
}

export function toolTarget(t: ToolItem): string {
  const i = t.input
  switch (t.name) {
    case 'Bash': return String(i.command ?? t.summary)
    case 'Read':
    case 'Edit':
    case 'MultiEdit':
    case 'Write': return String(i.file_path ?? t.summary)
    case 'Glob': return String(i.pattern ?? t.summary)
    case 'Grep': return `"${String(i.pattern ?? '')}"`
    case 'WebSearch': return `"${String(i.query ?? t.summary)}"`
    case 'WebFetch': return String(i.url ?? t.summary)
    case 'Task':
    case 'Agent': return String(i.description ?? t.summary)
    case 'Show': {
      // As the model wrote it (paths relative to the session's directory read best).
      const target = showTargetOf(i, null, '')
      return target ? showLabel(target, '') : t.summary
    }
    default: return t.summary || mcpParts(t.name)?.tool || t.name
  }
}

export interface PatchHunk {
  oldStart: number
  oldLines: number
  newStart: number
  newLines: number
  header?: string
  lines: string[]
}

export function patchOf(t: ToolItem): PatchHunk[] {
  const p = t.result?.structuredPatch
  return Array.isArray(p) ? p as PatchHunk[] : []
}

// File content after one Edit / MultiEdit / Write applied to `before`; null when it cannot be replayed
// (the old text is not there, or the call has no usable input).
function applyEdit(t: ToolItem, before: string): string | null {
  const replace = (text: string, oldS: unknown, newS: unknown, all: unknown) => {
    if (typeof oldS !== 'string' || typeof newS !== 'string' || !text.includes(oldS)) return null
    return all ? text.split(oldS).join(newS) : text.replace(oldS, () => newS)
  }
  if (t.name === 'Write') return typeof t.input.content === 'string' ? t.input.content : null
  if (t.name === 'Edit') return replace(before, t.input.old_string, t.input.new_string, t.input.replace_all)
  if (t.name === 'MultiEdit' && Array.isArray(t.input.edits)) {
    let text: string | null = before
    for (const e of t.input.edits as Record<string, unknown>[]) {
      text = text === null ? null : replace(text, e.old_string, e.new_string, e.replace_all)
    }
    return text
  }
  return null
}

/**
 * The net change of one file across several edits (in order): the content before the first edit against
 * the content after the last, as one diff. Null when the edits cannot be replayed (no recorded original);
 * callers then show each edit's own hunks.
 */
export function netPatch(edits: ToolItem[]): PatchHunk[] | null {
  if (!edits.length) return null
  const first = edits[0]!.result?.originalFile
  if (first !== null && typeof first !== 'string') return null
  const start = first ?? ''
  let text = start
  for (const t of edits) {
    // Each call records the file as it found it; that wins over our replay (the user may have edited too).
    const before = t.result?.originalFile
    if (typeof before === 'string') text = before
    const after = applyEdit(t, text)
    if (after === null) return null
    text = after
  }
  if (text === start) return []
  return structuredPatch('a', 'b', start, text, '', '', { context: 3 }).hunks.map(h => ({
    oldStart: h.oldStart,
    oldLines: h.oldLines,
    newStart: h.newStart,
    newLines: h.newLines,
    lines: h.lines.filter(l => !l.startsWith('\\'))
  }))
}

export function patchStats(hunks: PatchHunk[]) {
  let add = 0
  let del = 0
  for (const h of hunks) {
    for (const l of h.lines) {
      if (l.startsWith('+')) add++
      else if (l.startsWith('-')) del++
    }
  }
  return { add, del }
}

// `compact` is the collapsed row of a group: a clean exit reads "ok · 4.1s" there and "exit 0 · 4.1s" on the card.
export function toolMeta(t: ToolItem, now: number, compact = false): ToolMeta {
  if (t.status === 'running') return { text: t.name === 'Task' || t.name === 'Agent' ? 'running' : `running · ${formatDuration(now - t.startedAt)}`, cls: 'text-accent' }
  if (t.status === 'interrupted') return { text: 'interrupted', cls: 'text-muted' }
  const r = t.result ?? {}
  switch (t.name) {
    case 'Bash': {
      if (t.input.run_in_background) return { text: 'background', cls: 'text-muted' }
      const code = typeof r.exitCode === 'number' ? r.exitCode : t.status === 'error' ? 1 : 0
      if (compact && code === 0) return { text: `ok · ${formatSeconds(t.durationMs)}`, cls: 'text-ok' }
      return { text: `exit ${code} · ${formatSeconds(t.durationMs)}`, cls: code === 0 ? 'text-ok' : 'text-bad' }
    }
    case 'Read': {
      const f = r.file as { numLines?: number } | undefined
      return { text: f?.numLines ? `${f.numLines} lines` : t.resultSummary, cls: '' }
    }
    case 'Edit':
    case 'MultiEdit':
    case 'Write': {
      const s = patchStats(patchOf(t))
      return { text: `+${s.add} −${s.del}`, cls: 'diff' }
    }
    case 'Show':
      if (t.status === 'error') return { text: 'error', cls: 'text-bad' }
      if (r.shown === false) return { text: 'not shown', cls: 'text-muted' }
      return { text: showPaneTitle(r.target === 'url' ? 'url' : r.target === 'diff' ? 'diff' : 'file'), cls: '' }
    default:
      if (mcpParts(t.name)) return { text: `${t.status === 'error' ? 'error' : 'ok'} · ${formatSeconds(t.durationMs)}`, cls: t.status === 'error' ? 'text-bad' : 'text-ok' }
      return { text: t.resultSummary, cls: t.status === 'error' ? 'text-bad' : '' }
  }
}

export interface FileChange {
  kind: 'A' | 'M' | 'D'
  path: string
  add: number
  del: number
}

// Files changed by the Edit / Write calls of a list of tool items (the turn summary and the Diff pane).
export function fileChanges(tools: ToolItem[]): FileChange[] {
  const map = new Map<string, FileChange>()
  for (const t of tools) {
    if (!['Edit', 'MultiEdit', 'Write'].includes(t.name) || t.status !== 'ok') continue
    const path = String(t.input.file_path ?? '')
    if (!path) continue
    const s = patchStats(patchOf(t))
    const prev = map.get(path)
    if (prev) {
      prev.add += s.add
      prev.del += s.del
    } else {
      map.set(path, { kind: t.name === 'Write' && !s.del ? 'A' : 'M', path, add: s.add, del: s.del })
    }
  }
  return [...map.values()]
}
