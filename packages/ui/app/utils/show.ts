// Targets of the Show tool (ShowRequested events and Show tool calls): what to open, in which pane, and its label.
import type { ShowTarget } from '~/lib/session/types'
import type { PaneName } from '~/composables/usePanes'

export function showPane(t: ShowTarget): PaneName {
  return t.kind === 'file' ? 'files' : t.kind === 'diff' ? 'diff' : 'browser'
}

/** The pane's title, as the client names it in the tool result ("Opened … in the Files pane"). */
export function showPaneTitle(kind: ShowTarget['kind']): string {
  return kind === 'file' ? 'Files' : kind === 'diff' ? 'Changes' : 'Browser'
}

const relativeTo = (cwd: string, path: string) => {
  const base = cwd.replace(/\/+$/, '')
  return base && path.startsWith(`${base}/`) ? path.slice(base.length + 1) : path
}

const absoluteIn = (cwd: string, path: string) => path.startsWith('/') || !cwd ? path : `${cwd.replace(/\/+$/, '')}/${path.replace(/^\.\//, '')}`

/** "src/x.go:10-20", "Diff src/x.go", "Diff", or the URL. */
export function showLabel(t: ShowTarget, cwd: string): string {
  if (t.kind === 'url') return t.url
  if (t.kind === 'diff') return t.path ? `Diff ${relativeTo(cwd, t.path)}` : 'Diff'
  const p = relativeTo(cwd, t.path)
  if (t.lineEnd > t.lineStart) return `${p}:${t.lineStart}-${t.lineEnd}`
  return t.lineStart > 0 ? `${p}:${t.lineStart}` : p
}

const int = (v: unknown) => typeof v === 'number' && Number.isInteger(v) && v > 0 ? v : 0

/**
 * The target of a Show tool call, from its input (relative paths resolved against the session cwd) and the
 * validated result when there is one (it carries the absolute path and the clamped line range).
 */
export function showTargetOf(input: Record<string, unknown>, result: Record<string, unknown> | null, cwd: string): ShowTarget | null {
  const r = result ?? {}
  const path = typeof r.path === 'string' ? r.path : typeof input.path === 'string' ? absoluteIn(cwd, input.path) : ''
  switch (input.target) {
    case 'file': {
      if (!path) return null
      const lineStart = int(r.line ?? input.line)
      const lineEnd = lineStart ? int(r.end_line ?? input.end_line) : 0
      return { kind: 'file', path, lineStart, lineEnd: lineEnd >= lineStart ? lineEnd : 0 }
    }
    case 'diff':
      return { kind: 'diff', path }
    case 'url': {
      const url = typeof r.url === 'string' ? r.url : typeof input.url === 'string' ? input.url : ''
      return /^https?:\/\/[^/]/i.test(url) ? { kind: 'url', url } : null
    }
    default:
      return null
  }
}
