// An editor extension hosting this UI (packages/vscode). The extension puts `window.__HARNESS_IDE__` in the page before
// the app starts, with the client's endpoint and a way to post messages; everything else about the editor stays on its
// side. In a browser or the desktop shell none of this exists and ideHost() is null. docs/design/ide.md has the design.

export interface IdeHost {
  /** AgentService base URL and its token. */
  url: string
  token: string
  /** The folder a new session starts in. */
  folder: string
  /** Light or dark, as the editor's theme is now. */
  kind: 'light' | 'dark'
  post: (message: ToHost) => void
}

/** What the page tells the editor. */
export type ToHost
  = | { type: 'ready', connected: boolean, error: string }
    | { type: 'session', id: string, cwd: string }
    | { type: 'open-file', path: string, line: number, endLine: number }
    | { type: 'open-diff', sessionId: string, path: string }
    | { type: 'connection-lost' }

/** What the editor tells the page. */
export type FromHost
  = | { type: 'add-context', path: string, lineStart: number, lineEnd: number }
    | { type: 'add-text', text: string }
    | { type: 'new-session', folder: string }
    | { type: 'interrupt' }
    | { type: 'focus-composer' }
    | { type: 'theme', kind: 'light' | 'dark' }

declare global {
  interface Window { __HARNESS_IDE__?: IdeHost }
}

export function ideHost(): IdeHost | null {
  return import.meta.client ? window.__HARNESS_IDE__ ?? null : null
}

export const isIde = () => ideHost() !== null

/** Listens to the editor; returns a stop function. Messages are told apart from anything else the page hears by type. */
export function onHostMessage(handle: (message: FromHost) => void): () => void {
  const listener = (e: MessageEvent) => {
    const m = e.data as FromHost | undefined
    if (m && typeof m.type === 'string') handle(m)
  }
  window.addEventListener('message', listener)
  return () => window.removeEventListener('message', listener)
}

/**
 * The mention a path and lines make in the composer: relative inside the session's folder, a space written "\ ".
 * The client expands one with lines into the text of those lines; one without stays for the model to read.
 */
export function ideMention(path: string, lineStart: number, lineEnd: number, cwd: string): string {
  const base = cwd.replace(/\/+$/, '')
  const shown = base && path.startsWith(`${base}/`) ? path.slice(base.length + 1) : path
  const escaped = shown.replace(/ /g, '\\ ')
  if (lineStart <= 0) return escaped
  return lineEnd > lineStart ? `${escaped}#L${lineStart}-${lineEnd}` : `${escaped}#L${lineStart}`
}

export interface FileRef {
  path: string
  line: number
  endLine: number
}

/**
 * A file reference in the text of inline code: `src/a.ts`, `src/a.ts:12`, `src/a.ts:12-20`, `src/a.ts#L12`. Needs an
 * extension or a directory part, so that an ordinary word stays a word; the editor checks that the file exists.
 */
export function parseFileRef(text: string, cwd: string): FileRef | null {
  const m = /^((?:\/|\.{1,2}\/)?[\w.@+-]+(?:\/[\w.@+-]+)*)(?:(?::|#L)(\d+)(?:-(?:L)?(\d+))?)?(?::\d+)?$/.exec(text.trim())
  if (!m) return null
  const raw = m[1]!
  const name = raw.split('/').pop()!
  if (!raw.includes('/') && !/\.[A-Za-z0-9]+$/.test(name)) return null
  if (/^\d+(\.\d+)+$/.test(name)) return null // a version
  const path = raw.startsWith('/') || !cwd ? raw : `${cwd.replace(/\/+$/, '')}/${raw.replace(/^\.\//, '')}`
  const line = m[2] ? Number(m[2]) : 0
  return { path, line, endLine: m[3] ? Number(m[3]) : 0 }
}
