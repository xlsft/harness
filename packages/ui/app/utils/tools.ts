// How a tool call is labeled: icon, the white "target" and the right-hand meta (Tool.dc.html, Main.dc.html rows).
import { structuredPatch } from 'diff'
import type { PermissionModeName, ToolItem } from '~/lib/session/types'
import { MODE_LABEL } from './composer'
import { formatDuration, formatSeconds } from './format'
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

export const computerGroupLabel = (n: number) => n === 1 ? 'used the computer' : `took ${n} computer steps`

export function toolIcon(t: ToolItem): IconName {
  if (t.status === 'running' && t.name !== 'Task' && t.name !== 'Agent' && t.name !== 'Workflow') return 'loading'
  if (t.status === 'error' && t.name === 'Bash') return 'x'
  if (mcpParts(t.name)) return 'box'
  if (t.name.startsWith('Browser') || t.name === 'PreviewStart') return 'globe'
  if (t.name === 'ComputerClick' || t.name === 'ComputerDrag') return 'cursor'
  if (t.name.startsWith('Computer')) return 'monitor'
  if (t.name.startsWith('Terminal')) return 'term'
  switch (t.name) {
    case 'Bash':
    case 'PowerShell':
    case 'TaskOutput': return 'term'
    case 'Wait':
    case 'Monitor': return 'clock'
    case 'PreviewStop':
    case 'PreviewList':
    case 'PreviewLogs': return 'term'
    case 'Read':
    case 'DocumentRead': return 'file'
    case 'EnterWorktree':
    case 'ExitWorktree':
    case 'ProposeBranch': return 'branch'
    case 'SyncWithBase': return 'diff'
    case 'ChangeDirectory':
    case 'RequestDirectory': return 'folder'
    case 'Glob':
    case 'LS': return 'folder'
    case 'Grep':
    case 'LSP': return 'search'
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
    case 'DocumentEdit':
    case 'Write': return 'edit'
    case 'NotebookRun': return 'play'
    case 'Memory': return 'history'
    case 'ArtifactPublish': return 'copy'
    case 'KnowledgeSearch':
    case 'KnowledgeRead': return 'page'
    case 'Issues': return 'tasks'
    case 'WebSearch':
    case 'WebFetch': return 'web-search'
    case 'Task':
    case 'Agent':
    case 'SendMessage':
    case 'ListAgents':
    case 'Workflow': return 'agent'
    case 'PushNotification':
    case 'ReadNotifications': return 'alert'
    case 'SendUserFile': return 'file'
    case 'ReportFindings': return 'search'
    case 'Skill': return 'page'
    case 'Show': return t.input.target === 'url' ? 'globe' : t.input.target === 'diff' ? 'diff' : 'file'
    case 'SessionStart':
    case 'SessionHandoff': return 'branch'
    case 'SessionList': return 'list'
    case 'SessionSend': return 'session'
    case 'SessionDismiss': return 'close'
    case 'SessionArchive': return 'archive'
    case 'MarkChapter': return 'flag'
    case 'PrStatus':
    case 'PrBind':
    case 'PrUnbind':
    case 'PrMonitor':
    case 'PrAutoMerge': return 'pull-request'
    case 'SettingsGet':
    case 'SettingsSet': return 'settings'
    case 'OpenInEditor': return 'file'
    case 'RevealPath': return 'folder'
    case 'KeepAwake': return 'sun'
    case 'SessionSetModel':
    case 'SessionSetEffort':
    case 'SessionSetMode':
    case 'SessionSetTitle': return 'sliders'
    default: return 'box'
  }
}

// What a Bash call does, after "Bash": "Running …" / "Ran …" / "Failed to run …" with the title model's label;
// until it arrives (or when it never does), the model's own description, else the command.
export function bashLabel(t: ToolItem): { verb: string, text: string } {
  const verb = t.status === 'running' ? 'Running' : t.status === 'error' ? 'Failed to run' : 'Ran'
  if (t.title) return { verb, text: t.title }
  // The model's description is a phrase of its own ("Run the tests", "Sleep 3 seconds"): after the verb, without a
  // leading "Run" and its capital.
  const d = String(t.input.description ?? '').trim().replace(/^run\s+/i, '')
  if (!d) return { verb, text: String(t.input.command ?? '') }
  return { verb, text: /^[A-Z][a-z]/.test(d) ? d[0]!.toLowerCase() + d.slice(1) : d }
}

// A tool call's row: the model's own label (the description parameter), else what it acts on.
export function toolLabel(t: ToolItem): string {
  return String(t.input.description ?? '').trim() || toolTarget(t)
}

// The mode names of the SessionSetMode tool that differ from the UI's.
const TOOL_MODE_NAMES: Record<string, PermissionModeName> = { acceptEdits: 'accept_edits', dontAsk: 'dont_ask' }

export function toolTarget(t: ToolItem): string {
  const i = t.input
  switch (t.name) {
    case 'Bash':
    case 'PowerShell': return String(i.command ?? t.summary)
    case 'Read':
    case 'DocumentRead':
    case 'DocumentEdit':
    case 'Edit':
    case 'MultiEdit':
    case 'Write': return String(i.file_path ?? t.summary)
    case 'NotebookEdit':
    case 'NotebookRun': return String(i.notebook_path ?? t.summary)
    case 'Memory': return [i.command, i.name].filter(Boolean).join(' ') || t.summary
    case 'ArtifactPublish': return String(i.title ?? i.file_path ?? i.name ?? t.summary)
    case 'KnowledgeSearch': return `"${String(i.query ?? t.summary)}"`
    case 'KnowledgeRead': return String(i.document_id ?? 'documents')
    case 'Issues': return [i.operation, i.title ?? i.id].filter(Boolean).join(' ') || t.summary
    case 'Glob': return String(i.pattern ?? t.summary)
    case 'Grep': return `"${String(i.pattern ?? '')}"`
    case 'LSP': return lspTarget(i) || t.summary
    case 'WebSearch': return `"${String(i.query ?? t.summary)}"`
    case 'WebFetch': return String(i.url ?? t.summary)
    case 'Task':
    case 'Agent': return String(i.description ?? t.summary)
    case 'SendMessage': return String(i.to ?? t.summary)
    case 'Skill': return `/${String(i.skill ?? t.summary)}${i.args ? ` ${String(i.args)}` : ''}`
    case 'ListAgents': return 'agents'
    case 'Workflow': return String(i.name ?? i.resume_from ?? i.script_path ?? t.summary)
    case 'Show': {
      // As the model wrote it (paths relative to the session's directory read best).
      const target = showTargetOf(i, null, '')
      return target ? showLabel(target, '') : t.summary
    }
    case 'PushNotification': return String(i.title ?? t.summary)
    case 'SendUserFile': return Array.isArray(i.files) ? i.files.join(', ') : t.summary
    case 'TaskOutput': return String(i.task_id ?? t.summary) + patternSuffix(i.pattern)
    case 'Wait': return waitTarget(i) || t.summary
    case 'EnterWorktree': return `Enter worktree${i.name ? ` ${String(i.name)}` : ''}${i.base_ref ? ` from ${String(i.base_ref)}` : ''}`
    case 'ExitWorktree': return i.action === 'remove' ? (i.discard_changes ? 'Remove worktree, discarding changes' : 'Remove worktree') : 'Leave worktree'
    case 'ChangeDirectory': return `Move to ${String(i.path ?? '')}`
    case 'RequestDirectory': return `Ask for ${String(i.path ?? '')}`
    case 'ProposeBranch': return `Suggest branch ${String(i.name ?? '')}`
    case 'SyncWithBase': return `Sync with ${i.base_ref ? String(i.base_ref) : 'base'}`
    case 'Monitor': return (i.task_id ? `task ${String(i.task_id)}` : String(i.command ?? t.summary)) + patternSuffix(i.pattern)
    case 'SessionStart':
    case 'SessionHandoff':
    case 'MarkChapter': return String(i.title ?? t.summary)
    case 'SessionSend': return String(i.to ?? t.summary)
    case 'SessionArchive': return String(i.id ?? t.summary)
    case 'SessionDismiss': return String(i.task_id ?? t.summary)
    case 'PrStatus':
    case 'PrBind': return String(i.pr ?? t.summary ?? '') || t.name
    case 'PrMonitor': return i.enabled ? 'Watch the pull request' : 'Stop watching the pull request'
    case 'PrAutoMerge': return i.enabled ? `Turn on auto-merge${i.method ? ` (${String(i.method)})` : ''}` : 'Turn off auto-merge'
    case 'PreviewStart': return String(i.name ?? i.url ?? t.summary)
    case 'PreviewStop':
    case 'PreviewLogs': return String(i.server_id ?? t.summary)
    case 'ComputerAccess': return Array.isArray(i.apps) ? i.apps.join(', ') : t.summary
    case 'ComputerType': return `${String(i.app ?? '')}: "${String(i.text ?? '').split('\n')[0]!.slice(0, 80)}"`
    case 'ComputerKey': return `${String(i.app ?? '')}: ${String(i.keys ?? '')}`
    case 'ComputerMenu': return `${String(i.app ?? '')}: ${Array.isArray(i.path) ? i.path.join(' > ') : ''}`
    case 'ComputerApps': return 'apps on this machine'
    case 'ComputerClick':
    case 'ComputerDrag':
    case 'ComputerScroll':
    case 'ComputerScreenshot':
    case 'ComputerActivate':
    case 'ComputerFind':
    case 'ComputerBatch': return String(i.app ?? t.summary)
    case 'SettingsGet': return String(i.key ?? 'Settings')
    case 'SettingsSet': return `${String(i.key ?? '')} = ${String(i.value ?? '')}`
    case 'OpenInEditor': return i.line ? `${String(i.path ?? '')}:${String(i.line)}` : String(i.path ?? t.summary)
    case 'RevealPath': return String(i.path ?? t.summary)
    case 'KeepAwake': return i.enabled ? 'Keep computer awake' : 'Allow sleep'
    case 'SessionSetModel': return `Model: ${String(i.model ?? '')}`
    case 'SessionSetEffort': return `Effort: ${String(i.effort ?? '')}`
    case 'SessionSetMode': return `Mode: ${MODE_LABEL[TOOL_MODE_NAMES[String(i.mode)] ?? i.mode as PermissionModeName] ?? String(i.mode ?? '')}`
    case 'SessionSetTitle': return `Title: ${String(i.title ?? '')}`
    case 'TerminalRun': return String(i.command ?? t.summary)
    case 'BrowserNavigate': return String(i.url ?? i.action ?? t.summary)
    case 'BrowserClick':
    case 'BrowserType':
    case 'BrowserScroll': return String(i.ref ?? i.direction ?? t.summary ?? '')
    case 'BrowserKey': return String(i.keys ?? t.summary)
    case 'BrowserEval': return String(i.expression ?? t.summary).split('\n')[0]!
    default: return t.summary || mcpParts(t.name)?.tool || t.name
  }
}

// What an LSP call asks about: "hover on read.go:12:5", "symbols matching Foo", "outline of read.go".
function lspTarget(i: Record<string, unknown>): string {
  const file = String(i.file_path ?? '').split('/').pop() ?? ''
  switch (i.operation) {
    case 'definition': return `definition at ${file}:${String(i.line)}:${String(i.character)}`
    case 'references': return `references at ${file}:${String(i.line)}:${String(i.character)}`
    case 'hover': return `hover on ${file}:${String(i.line)}:${String(i.character)}`
    case 'document_symbols': return `outline of ${file}`
    case 'workspace_symbols': return `symbols matching ${String(i.query ?? '')}`
    default: return ''
  }
}

const patternSuffix = (p: unknown) => typeof p === 'string' && p ? ` /${p}/` : ''

// What a Wait call waits for: "port 3000", "port example.com:443", "task b1f2", a path; plus the pattern.
// 5s · 1m · 1m 30s: a limit the model set, in whole seconds.
function roughDuration(ms: number): string {
  const sec = Math.round(ms / 1000)
  return sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m${sec % 60 ? ` ${sec % 60}s` : ''}`
}

// "up to 1m · port 3000": how long it may wait first, then what for (a file by its name, not its path).
function waitTarget(i: Record<string, unknown>): string {
  const limit = typeof i.timeout === 'number' ? `up to ${roughDuration(i.timeout)}` : ''
  const what = i.port != null
    ? `port ${i.host ? `${String(i.host)}:` : ''}${String(i.port)}`
    : i.task_id
      ? `task ${String(i.task_id)}${patternSuffix(i.pattern)}`
      : i.path ? `${String(i.path).split('/').pop()}${i.changed ? ' to change' : ''}${patternSuffix(i.pattern)}` : ''
  return [limit, what].filter(Boolean).join(' · ')
}

// The tools whose results carry a structuredPatch of one file, and that file.
const EDIT_TOOLS = ['Edit', 'MultiEdit', 'Write', 'NotebookEdit', 'DocumentEdit']
export const isEditTool = (name: string) => EDIT_TOOLS.includes(name)
export const editedPath = (t: ToolItem) => String((t.name === 'NotebookEdit' ? t.input.notebook_path : t.input.file_path) ?? '')

// What a notebook edit did: "replace cell 3f2a", "insert cell 2" (its diff shows the cell's source, not the JSON).
export function notebookCellLabel(t: ToolItem): string {
  const r = t.result ?? {}
  const mode = String(r.editMode ?? t.input.edit_mode ?? 'replace')
  const cell = r.cellId || (r.cellIndex ?? t.input.cell_id ?? t.input.cell_index)
  return cell === undefined || cell === null || cell === '' ? `${mode} cell` : `${mode} cell ${String(cell)}`
}

// The line that closes a Bash result when the command ended in another directory; the client sets it apart from the
// output so it can be drawn dimmed. In a failed call's text it is the last line: withoutCwdNote takes it off.
export function bashCwdNote(t: ToolItem): string {
  const n = t.result?.cwdNote
  return typeof n === 'string' ? n : ''
}

// A command the sandbox would have confined ran outside it, with the user's approval: the mark on its row.
export function ranOutsideSandbox(t: ToolItem): boolean {
  return t.result?.sandbox === 'outside'
}

// What the sandbox refused a Bash command, in short. The model's longer note with its advice closes the result's text:
// withoutCwdNote takes it off there.
export function bashSandboxDenied(t: ToolItem): { summary: string, note: string } {
  const summary = t.result?.sandboxDenied
  const note = t.result?.sandboxNote
  return { summary: typeof summary === 'string' ? summary : '', note: typeof note === 'string' ? note : '' }
}

export function withoutCwdNote(text: string, note: string): string {
  const s = text.trimEnd()
  return note && s.endsWith(note) ? s.slice(0, -note.length).trimEnd() : text
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
    case 'Bash':
    case 'PowerShell': {
      if (t.input.run_in_background) return { text: 'background', cls: 'text-muted' }
      const code = typeof r.exitCode === 'number' ? r.exitCode : t.status === 'error' ? 1 : 0
      if (compact && code === 0) return { text: `ok · ${formatSeconds(t.durationMs)}`, cls: 'text-ok' }
      return { text: `exit ${code} · ${formatSeconds(t.durationMs)}`, cls: code === 0 ? 'text-ok' : 'text-bad' }
    }
    case 'TaskOutput': {
      if (t.status === 'error') return { text: t.resultSummary || 'error', cls: 'text-bad' }
      const wait = r.timed_out ? ' · timed out' : ''
      if (r.status === 'exited') return { text: `exit ${String(r.exit_code ?? 0)}${wait}`, cls: r.exit_code ? 'text-bad' : 'text-ok' }
      if (r.status === 'killed') return { text: `killed${wait}`, cls: 'text-bad' }
      return { text: `running${wait}`, cls: 'text-muted' }
    }
    case 'Wait': {
      if (t.status === 'error') return { text: t.resultSummary || 'error', cls: 'text-bad' }
      const elapsed = formatSeconds(typeof r.elapsed_ms === 'number' ? r.elapsed_ms : t.durationMs)
      // Running out the time is a normal end (a wait used as a timer), not a failure.
      return r.met === false ? { text: `waited ${elapsed}`, cls: 'text-muted' } : { text: `ready · ${elapsed}`, cls: 'text-ok' }
    }
    case 'Monitor': {
      if (t.status === 'error') return { text: t.resultSummary || 'error', cls: 'text-bad' }
      return { text: r.persistent ? 'watching' : 'until first match', cls: 'text-muted' }
    }
    case 'LSP':
      if (t.status === 'error') return { text: 'no answer', cls: 'text-muted' }
      if (r.state === 'indexing' || r.state === 'timeout') return { text: String(r.state), cls: 'text-muted' }
      return { text: t.resultSummary, cls: '' }
    case 'Read': {
      const f = r.file as { numLines?: number } | undefined
      return { text: f?.numLines ? `${f.numLines} lines` : t.resultSummary, cls: '' }
    }
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
    case 'DocumentEdit':
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
  kind: 'A' | 'M' | 'D' | 'R'
  path: string
  add: number
  del: number
}

// Files changed by the Edit / Write calls of a list of tool items (the turn summary and the Diff pane).
export function fileChanges(tools: ToolItem[]): FileChange[] {
  const map = new Map<string, FileChange>()
  for (const t of tools) {
    if (!isEditTool(t.name) || t.status !== 'ok') continue
    const path = editedPath(t)
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
