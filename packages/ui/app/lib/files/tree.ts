// The files pane's pure logic: the visible rows of the lazily loaded tree, git status lookups and their
// aggregation onto folders, the fuzzy file-name filter, file kinds and the targets of Show requests.

import type { EntryKind, FileEntry, FileRoot, GitStatus } from './types'

export type FileKindName = 'dir' | 'md' | 'code' | 'image' | 'svg' | 'file'

export interface TreeRow {
  path: string
  name: string
  kind: EntryKind
  depth: number
  // A working directory at the top of the tree.
  root: boolean
  expanded: boolean
  // Expanded, its listing not loaded yet.
  loading: boolean
  git: GitStatus
  // A folder with changes somewhere inside.
  changed: boolean
  ignored: boolean
  deleted: boolean
  symlink: boolean
  size: number
  modifiedAt: string
}

const MARKDOWN = new Set(['md', 'markdown', 'mdx'])
const IMAGES = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'avif'])
const CODE = new Set([
  'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'vue', 'svelte', 'go', 'rs', 'py', 'rb', 'java', 'kt', 'swift', 'c', 'h',
  'cc', 'cpp', 'hpp', 'cs', 'php', 'sh', 'bash', 'zsh', 'fish', 'ps1', 'sql', 'proto', 'json', 'jsonc', 'yaml', 'yml',
  'toml', 'ini', 'xml', 'html', 'css', 'scss', 'less', 'lua', 'dart', 'scala', 'ex', 'exs', 'zig', 'tf', 'graphql',
  'mod', 'sum', 'lock', 'dockerfile', 'makefile'
])

export function baseName(path: string) {
  const p = path.replace(/\/+$/, '')
  return p.slice(p.lastIndexOf('/') + 1)
}

export function dirName(path: string) {
  const p = path.replace(/\/+$/, '')
  const i = p.lastIndexOf('/')
  return i <= 0 ? '/' : p.slice(0, i)
}

export function extension(path: string) {
  const name = baseName(path).toLowerCase()
  if (name === 'dockerfile' || name === 'makefile') return name
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(i + 1) : ''
}

export function fileKind(path: string, kind: EntryKind = 'file'): FileKindName {
  if (kind === 'dir') return 'dir'
  const ext = extension(path)
  if (MARKDOWN.has(ext)) return 'md'
  if (ext === 'svg') return 'svg'
  if (IMAGES.has(ext)) return 'image'
  if (CODE.has(ext)) return 'code'
  return 'file'
}

// The language name highlightLine takes (it only treats shell-like ones specially) and the pane shows.
export function languageOf(path: string) {
  const ext = extension(path)
  const names: Record<string, string> = {
    ts: 'TypeScript', tsx: 'TypeScript', js: 'JavaScript', jsx: 'JavaScript', mjs: 'JavaScript', vue: 'Vue', go: 'Go',
    rs: 'Rust', py: 'Python', rb: 'Ruby', sh: 'Shell', bash: 'Shell', zsh: 'Shell', json: 'JSON', yaml: 'YAML',
    yml: 'YAML', toml: 'TOML', proto: 'Protobuf', md: 'Markdown', css: 'CSS', html: 'HTML', sql: 'SQL', swift: 'Swift'
  }
  return { lang: ext || 'txt', label: names[ext] ?? (ext ? ext.toUpperCase() : 'Plain text') }
}

export function isWithin(path: string, dir: string) {
  const d = dir.replace(/\/+$/, '')
  return path === d || path.startsWith(`${d}/`)
}

// The working directory containing path (the deepest one).
export function rootOf(path: string, roots: FileRoot[]) {
  let best: FileRoot | undefined
  for (const r of roots) {
    if (isWithin(path, r.path) && (!best || r.path.length > best.path.length)) best = r
  }
  return best
}

// path relative to its working directory; the root's own name for the root itself.
export function relativePath(path: string, roots: FileRoot[]) {
  const r = rootOf(path, roots)
  if (!r) return path
  if (path === r.path) return r.name
  return path.slice(r.path.replace(/\/+$/, '').length + 1)
}

// An absolute path from a path given by the agent or another pane: absolute stays, "~/" expands against the
// cwd's home when the cwd is given that way, anything else is relative to the cwd.
export function absolutePath(path: string, cwd: string) {
  const p = path.trim()
  const c = cwd.replace(/\/+$/, '')
  if (p.startsWith('/')) return normalize(p)
  if (p.startsWith('~')) return normalize(p)
  return normalize(`${c}/${p.replace(/^\.\//, '')}`)
}

// Resolves "." and ".." segments.
export function normalize(path: string) {
  const lead = path.startsWith('/') ? '/' : ''
  const out: string[] = []
  for (const seg of path.split('/')) {
    if (!seg || seg === '.') continue
    if (seg === '..') out.pop()
    else out.push(seg)
  }
  return lead + out.join('/')
}

// The git status of path from a repository's map: its own entry, or 'U' inside an untracked directory.
export function statusOf(path: string, status: Record<string, GitStatus>): GitStatus {
  const own = status[path]
  if (own) return own
  for (let d = dirName(path); d !== '/' && d.includes('/'); d = dirName(d)) {
    if (status[d] === 'U') return 'U'
  }
  return ''
}

// The folders that contain changes: every ancestor of a changed path, up to (and including) the roots it
// is in. An untracked directory counts as changed itself.
export function changedDirs(status: Record<string, GitStatus>, roots: string[]): Set<string> {
  const out = new Set<string>()
  for (const [path, st] of Object.entries(status)) {
    if (!st) continue
    const root = roots.filter(r => isWithin(path, r)).sort((a, b) => b.length - a.length)[0]
    if (!root) continue
    if (st === 'U' && path !== root) out.add(path)
    for (let d = dirName(path); isWithin(d, root); d = dirName(d)) {
      if (out.has(d) && d !== path) break
      out.add(d)
      if (d === root) break
    }
  }
  return out
}

// Counts of changes per status under a folder (the folder's tooltip).
export function changeCounts(dir: string, status: Record<string, GitStatus>) {
  const counts: Partial<Record<GitStatus, number>> = {}
  for (const [path, st] of Object.entries(status)) {
    if (st && path !== dir && isWithin(path, dir)) counts[st] = (counts[st] ?? 0) + 1
  }
  return counts
}

export interface TreeState {
  roots: FileRoot[]
  // Listings by directory path; a directory without one is not loaded yet.
  listings: Record<string, FileEntry[]>
  expanded: Set<string>
  // Git status by absolute path, merged over the repositories of the roots.
  status: Record<string, GitStatus>
}

// The visible rows: each root, and the children of expanded folders, depth first. Git letters come from the
// status map (fresher than the listings) with the listing's as a fallback.
export function buildRows(state: TreeState): TreeRow[] {
  const changed = changedDirs(state.status, state.roots.map(r => r.path))
  const rows: TreeRow[] = []
  const walk = (dir: string, depth: number) => {
    for (const e of state.listings[dir] ?? []) {
      const expanded = e.kind === 'dir' && state.expanded.has(e.path)
      rows.push({
        path: e.path,
        name: e.name,
        kind: e.kind,
        depth,
        root: false,
        expanded,
        loading: expanded && !state.listings[e.path],
        git: e.kind === 'dir' ? '' : (statusOf(e.path, state.status) || e.git),
        changed: e.kind === 'dir' && (changed.has(e.path) || e.git === 'U'),
        ignored: e.ignored,
        deleted: e.deleted,
        symlink: e.symlink,
        size: e.size,
        modifiedAt: e.modifiedAt
      })
      if (expanded) walk(e.path, depth + 1)
    }
  }
  for (const r of state.roots) {
    const expanded = state.expanded.has(r.path)
    rows.push({
      path: r.path,
      name: r.name,
      kind: 'dir',
      depth: 0,
      root: true,
      expanded,
      loading: expanded && !state.listings[r.path],
      git: '',
      changed: changed.has(r.path),
      ignored: false,
      deleted: false,
      symlink: false,
      size: 0,
      modifiedAt: ''
    })
    if (expanded) walk(r.path, 1)
  }
  return rows
}

// The folders to expand so path shows in the tree: its ancestors from its root down.
export function ancestorsToExpand(path: string, roots: FileRoot[]) {
  const r = rootOf(path, roots)
  if (!r) return []
  const out: string[] = []
  for (let d = dirName(path); isWithin(d, r.path); d = dirName(d)) {
    out.unshift(d)
    if (d === r.path) break
  }
  return out
}

// Fuzzy file-name matching (case-insensitive subsequence), scored like the client's SuggestFiles: contiguous
// runs, word starts and hits in the file name score higher, shorter paths win ties. null when it does not match.
export function fuzzyScore(query: string, path: string): number | null {
  const q = query.trim().toLowerCase()
  if (!q) return -path.length
  const lp = path.toLowerCase()
  const base = path.lastIndexOf('/') + 1
  let score = 0
  let qi = 0
  let prev = -2
  for (let i = 0; i < lp.length && qi < q.length; i++) {
    if (lp[i] !== q[qi]) continue
    let s = 1
    if (i === prev + 1) s += 5
    const before = path[i - 1] ?? ''
    if (i === 0 || '/_-. '.includes(before) || (/[A-Z]/.test(path[i]!) && /[a-z]/.test(before))) s += 4
    if (i >= base) s += 2
    score += s
    prev = i
    qi++
  }
  if (qi < q.length) return null
  if (lp.slice(base).includes(q)) score += 10
  return score * 10 - path.length
}

export interface FilterHit {
  // Absolute.
  path: string
  // Relative to its root, as matched and shown.
  rel: string
  dir: boolean
  score: number
}

// Ranks candidate paths (absolute) for a query by their path relative to the roots; best first, deduplicated.
export function filterPaths(query: string, candidates: { path: string, dir?: boolean }[], roots: FileRoot[], limit = 200): FilterHit[] {
  const seen = new Set<string>()
  const hits: FilterHit[] = []
  for (const c of candidates) {
    const path = c.path.replace(/\/+$/, '')
    if (seen.has(path)) continue
    seen.add(path)
    const rel = relativePath(path, roots)
    const score = fuzzyScore(query, rel)
    if (score !== null) hits.push({ path, rel, dir: !!c.dir || c.path.endsWith('/'), score })
  }
  hits.sort((a, b) => b.score - a.score || a.rel.localeCompare(b.rel))
  return hits.slice(0, limit)
}

// Where a Show request lands: the absolute file and the lines to highlight (1-based, inclusive; null without
// a range).
export function showTargetFile(t: { path: string, lineStart: number, lineEnd: number }, cwd: string) {
  const path = absolutePath(t.path, cwd)
  const lines = t.lineStart > 0 ? { start: t.lineStart, end: Math.max(t.lineEnd, t.lineStart) } : null
  return { path, lines }
}

export function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = bytes / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

// Paths a finished tool call wrote (Write, Edit, MultiEdit, NotebookEdit): the pane refreshes after them.
export function writtenPaths(tool: { name: string, input: Record<string, unknown> }): string[] {
  const name = tool.name.toLowerCase()
  if (!['write', 'edit', 'multiedit', 'notebookedit', 'str_replace_based_edit_tool', 'apply_patch'].includes(name)) return []
  const p = tool.input.file_path ?? tool.input.path ?? tool.input.notebook_path
  return typeof p === 'string' && p ? [p] : ['']
}
