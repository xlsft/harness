// MockFilesApi: a small fake project under each session's cwd for the mock transport, with git changes (a
// modified, an added, a deleted and an untracked file, an untracked folder), ignored folders, markdown, code, an
// SVG and a PNG, a binary and a large file. It follows the client's rules: paths must stay inside the cwd, ignored
// entries only with showIgnored, directories first, etags for unchanged files.

import type { DirListing, FileContent, FileEntry, FileRoot, FilesApi, GitStatus } from './types'
import { FILE_LIMITS } from './types'
import { absolutePath, baseName, dirName, fuzzyScore, isWithin } from './tree'

// A 2×2 PNG.
const PIXEL_PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFklEQVR4nGNk+M/AwMDAxMDAwMDAAAANHQEDasKb6QAAAABJRU5ErkJggg=='

const README = `# Harness

A self-hosted coding-agent platform: a **server** for providers and users, a **client** on each machine that runs
sessions, and this **UI**.

## Packages

| Package | Language | What it does |
| --- | --- | --- |
| core | TypeScript | Providers, users, the gateway |
| client | Go | Sessions, tools, terminals, files |
| ui | Vue | The web and desktop app |

## Getting started

\`\`\`sh
pnpm install
pnpm --filter ui dev
\`\`\`

- Sessions run on your machine.
- The files pane shows the session's working directories.
`

const FILES: Record<string, string> = {
  '.gitignore': 'node_modules/\ndist/\n*.log\n',
  'README.md': README,
  'package.json': '{\n  "name": "harness",\n  "private": true,\n  "scripts": {\n    "dev": "pnpm --filter ui dev"\n  }\n}\n',
  'notes.txt': 'Things to try in the files pane:\n- expand folders\n- filter by name\n- open README.md\n',
  'docs/guide.md': '# Guide\n\nOpen a session, then **Files** in the session menu.\n\n> Changes the agent makes show up here.\n',
  'docs/architecture.md': '# Architecture\n\nThe client serves `harness.agent.v1.AgentService` over Connect.\n',
  'src/index.ts': `import { createServer } from './server'

// Starts the dev server on the given port.
export async function main(port = 3000) {
  const server = await createServer({ port })
  console.log(\`listening on \${port}\`)
  return server
}
`,
  'src/server/files.go': `package server

import (
\t"os"
\t"path/filepath"
)

// ListFiles lists a directory inside the session's working directories.
func ListFiles(dir string) ([]os.DirEntry, error) {
\treturn os.ReadDir(filepath.Clean(dir))
}
`,
  'src/server/routes.ts': Array.from({ length: 120 }, (_, i) => i % 10 === 0 ? `// Route group ${i / 10 + 1}` : `export const route${i} = '/api/v1/items/${i}'`).join('\n') + '\n',
  'assets/logo.svg': '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#f97316"/><path d="M6 6h12v12H6z" fill="#fff"/></svg>\n',
  'scratch/todo.md': '# TODO\n\n- [ ] Wire the files pane\n',
  'dist/bundle.js': 'console.log("built")\n',
  'debug.log': 'debug\n',
  'node_modules/left-pad/index.js': 'module.exports = (s, n) => s.padStart(n)\n',
  '.git/HEAD': 'ref: refs/heads/main\n'
}

const BINARY: Record<string, { size: number, mime: string, data?: string }> = {
  'assets/pixel.png': { size: 75, mime: 'image/png', data: PIXEL_PNG },
  'bin/harness-client': { size: 18_400_000, mime: 'application/octet-stream' },
  'data/dump.sqlite': { size: 3_200_000, mime: 'application/octet-stream' }
}

const INITIAL_STATUS: Record<string, GitStatus> = {
  'README.md': 'M',
  'src/server/files.go': 'A',
  'src/server/legacy.go': 'D',
  'notes.txt': 'U',
  'scratch': 'U'
}

const IGNORED = (rel: string) => /(^|\/)(node_modules|\.git)(\/|$)/.test(rel) || /^dist(\/|$)/.test(rel) || rel.endsWith('.log')

interface MockFile {
  text?: string
  binary?: { size: number, mime: string, data?: string }
  mtime: number
}

export class MockFilesApi implements FilesApi {
  private sessionCwd: (id: string) => string | undefined
  private files = new Map<string, Map<string, MockFile>>()
  private status = new Map<string, Record<string, GitStatus>>()

  constructor(opts: { sessionCwd: (id: string) => string | undefined }) {
    this.sessionCwd = opts.sessionCwd
  }

  private project(sessionId: string) {
    const cwd = this.sessionCwd(sessionId)
    if (cwd === undefined) throw new Error(`[not_found] session ${sessionId} is not open; call CreateSession first`)
    const root = cwd.replace(/\/+$/, '')
    let files = this.files.get(sessionId)
    if (!files) {
      files = new Map()
      const t = Date.parse('2026-11-03T12:00:00Z')
      for (const [rel, text] of Object.entries(FILES)) files.set(rel, { text, mtime: t })
      for (const [rel, binary] of Object.entries(BINARY)) files.set(rel, { binary, mtime: t })
      this.files.set(sessionId, files)
      this.status.set(sessionId, { ...INITIAL_STATUS })
    }
    return { root, files, status: this.status.get(sessionId)! }
  }

  private resolve(sessionId: string, path: string) {
    const p = this.project(sessionId)
    const abs = path ? absolutePath(path, p.root) : p.root
    if (!isWithin(abs, p.root)) throw new Error(`[permission_denied] ${abs} is outside the session's working directories`)
    return { ...p, abs, rel: abs === p.root ? '' : abs.slice(p.root.length + 1) }
  }

  // Changes a file as the agent would (tests and the mock scenario).
  write(sessionId: string, rel: string, text: string) {
    const { files, status } = this.project(sessionId)
    const existed = files.has(rel)
    files.set(rel, { text, mtime: Math.max(Date.now(), (files.get(rel)?.mtime ?? 0) + 1) })
    if (!status[rel]) status[rel] = existed ? 'M' : 'U'
  }

  async roots(sessionId: string): Promise<FileRoot[]> {
    const { root } = this.project(sessionId)
    return [{ path: root, name: baseName(root), gitRoot: root }]
  }

  async list(sessionId: string, path: string, opts: { showIgnored?: boolean } = {}): Promise<DirListing> {
    const { files, status, abs, rel } = this.resolve(sessionId, path)
    const prefix = rel ? `${rel}/` : ''
    if (rel && files.has(rel)) throw new Error(`[invalid_argument] ${abs} is not a directory`)
    const children = new Map<string, FileEntry>()
    const add = (childRel: string, dir: boolean, f?: MockFile, deleted = false) => {
      const name = childRel.slice(prefix.length).split('/')[0]!
      const cr = prefix + name
      if (children.has(name)) return
      const size = f?.binary?.size ?? (f?.text !== undefined ? new TextEncoder().encode(f.text).length : 0)
      children.set(name, {
        name,
        path: `${abs}/${name}`,
        kind: dir ? 'dir' : 'file',
        symlink: false,
        size: dir ? 0 : size,
        modifiedAt: deleted ? '' : new Date(f?.mtime ?? 0).toISOString(),
        git: dir ? (status[cr] === 'U' ? 'U' : '') : (status[cr] ?? (isUnder(cr, 'scratch') ? 'U' : '')),
        ignored: IGNORED(cr),
        deleted
      })
    }
    let found = !rel
    for (const [r, f] of files) {
      if (!r.startsWith(prefix)) continue
      found = true
      add(r, r.slice(prefix.length).includes('/'), f)
    }
    for (const [r, st] of Object.entries(status)) {
      if (st === 'D' && dirName(`/${r}`).slice(1) === rel) add(r, false, undefined, true)
    }
    if (!found) throw new Error(`[not_found] ${abs} does not exist`)
    const entries = [...children.values()]
      .filter(e => opts.showIgnored || !e.ignored)
      .sort((a, b) => (a.kind === 'dir' ? 0 : 1) - (b.kind === 'dir' ? 0 : 1) || a.name.toLowerCase().localeCompare(b.name.toLowerCase()))
    return { path: abs, entries: entries.slice(0, FILE_LIMITS.entries), truncated: entries.length > FILE_LIMITS.entries }
  }

  async read(sessionId: string, path: string, opts: { maxBytes?: number, ifNoneMatch?: string } = {}): Promise<FileContent> {
    const { files, abs, rel } = this.resolve(sessionId, path)
    const f = files.get(rel)
    if (!f) {
      if ([...files.keys()].some(r => r.startsWith(`${rel}/`))) throw new Error(`[invalid_argument] ${abs} is a directory`)
      throw new Error(`[not_found] ${abs} does not exist`)
    }
    const size = f.binary?.size ?? new TextEncoder().encode(f.text ?? '').length
    const etag = `${size.toString(16)}-${f.mtime.toString(16)}`
    const base: FileContent = {
      path: abs, size, modifiedAt: new Date(f.mtime).toISOString(), etag, notModified: false, mime: '', binary: false,
      text: '', truncated: false, tooLarge: false
    }
    if (opts.ifNoneMatch === etag) return { ...base, notModified: true }
    if (f.binary) {
      const imageLimit = Math.min(FILE_LIMITS.image, opts.maxBytes || Infinity)
      const image = f.binary.mime.startsWith('image/')
      return {
        ...base,
        mime: f.binary.mime,
        binary: true,
        data: image && f.binary.data && size <= imageLimit ? Uint8Array.from(atob(f.binary.data), c => c.charCodeAt(0)) : undefined,
        tooLarge: image && size > imageLimit
      }
    }
    const textLimit = Math.min(FILE_LIMITS.text, opts.maxBytes || Infinity)
    const text = f.text ?? ''
    const ext = rel.slice(rel.lastIndexOf('.') + 1)
    const mime = ext === 'md' ? 'text/markdown' : ext === 'svg' ? 'image/svg+xml' : ext === 'json' ? 'application/json' : 'text/plain'
    return { ...base, mime, text: size > textLimit ? text.slice(0, textLimit) : text, truncated: size > textLimit }
  }

  async gitStatus(sessionId: string) {
    const { root, status } = this.project(sessionId)
    const files: Record<string, GitStatus> = {}
    for (const [rel, st] of Object.entries(status)) files[`${root}/${rel}`] = st
    return { root, files, truncated: false }
  }

  async search(sessionId: string, query: string, limit: number) {
    const { files } = this.project(sessionId)
    return [...files.keys()]
      .filter(r => !IGNORED(r))
      .map(r => ({ r, s: fuzzyScore(query, r) }))
      .filter((h): h is { r: string, s: number } => h.s !== null)
      .sort((a, b) => b.s - a.s)
      .slice(0, limit)
      .map(h => h.r)
  }
}

function isUnder(rel: string, dir: string) {
  return rel === dir || rel.startsWith(`${dir}/`)
}
