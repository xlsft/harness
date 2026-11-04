// The tools that reach the user, as the frontends read them: SendUserFile cards and ReportFindings lists come from the
// call's structured result (the client validated, ordered and merged them), PushNotification from its event.
import type { ToolItem, UserNotificationVM } from './types'

export interface SentFile {
  name: string
  // Absolute, on the session's machine.
  path: string
  size: number
  mime: string
  kind: 'image' | 'svg' | 'html' | 'pdf' | 'markdown' | 'file'
  display: 'inline' | 'attachment'
  sha256: string
  // The file, base64, when it was small enough to travel with the result and the server has no object store: other devices
  // read it from here.
  data?: string
  // The file went to the object store: other devices read it from there, by sha256.
  stored?: boolean
}

export interface Finding {
  id: string
  file: string
  path: string
  line: number
  summary: string
  failure_scenario: string
  category?: string
  verdict?: string
  outcome: string
  short_summary?: string
  severity: 'critical' | 'high' | 'medium' | 'low'
}

/** The files of a finished SendUserFile call; null when it has no usable result. */
export function sentFilesOf(t: ToolItem): SentFile[] | null {
  const files = t.result?.files
  return t.name === 'SendUserFile' && t.status === 'ok' && Array.isArray(files) && files.length ? files as SentFile[] : null
}

/** The list of a finished ReportFindings call (empty: no findings); null when it has no usable result. */
export function findingsOf(t: ToolItem): Finding[] | null {
  const f = t.result?.findings
  return t.name === 'ReportFindings' && t.status === 'ok' && Array.isArray(f) ? f as Finding[] : null
}

export function decodeBase64(data: string): Uint8Array<ArrayBuffer> {
  const bin = atob(data)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

/** "812 B", "14 KB", "3.2 MB". */
export function formatFileSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1 << 20) return `${Math.round(n / 1024)} KB`
  return `${(n / (1 << 20)).toFixed(1)} MB`
}

// A notification older than this is history (a reload, a reconnect), not news.
export const NOTIFICATION_FRESH_MS = 5 * 60_000
const KEEP_SHOWN = 200

/**
 * Whether a notification still has to be shown, given the ids shown so far; returns the updated list when it does. A
 * frontend with the session open and focused does not show it, and neither does it later.
 */
export function takeUserNotification(n: UserNotificationVM | null | undefined, shown: string[], now: number): string[] | null {
  if (!n || shown.includes(n.id) || now - n.at > NOTIFICATION_FRESH_MS) return null
  return [...shown, n.id].slice(-KEEP_SHOWN)
}
