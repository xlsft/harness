// Calls to the client from the extension host: Connect unary with JSON, so no library is needed.
import type { Endpoint } from './locate.ts'

export class RpcError extends Error {
  readonly code: string
  constructor(code: string, message: string) {
    super(message)
    this.code = code
  }
}

export async function call<T>(ep: Endpoint, service: string, method: string, body: unknown = {}, timeoutMs = 15000): Promise<T> {
  let res: Response
  try {
    res = await fetch(`${ep.url}/${service}/${method}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', 'Authorization': `Bearer ${ep.token}` },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs)
    })
  } catch (err) {
    throw new RpcError('unavailable', err instanceof Error ? err.message : String(err))
  }
  const text = await res.text()
  let parsed: { code?: string, message?: string } = {}
  try {
    parsed = text ? JSON.parse(text) : {}
  } catch {
    throw new RpcError('internal', `answer is not JSON (status ${res.status})`)
  }
  if (!res.ok) throw new RpcError(parsed.code ?? 'unknown', parsed.message ?? `status ${res.status}`)
  return parsed as T
}

export const AGENT = 'harness.agent.v1.AgentService'
export const IDE = 'harness.agent.v1.IdeService'

/** Whether a client answers at this endpoint with this token. */
export async function answers(ep: Endpoint): Promise<boolean> {
  try {
    await call(ep, AGENT, 'Initialize', {}, 5000)
    return true
  } catch {
    return false
  }
}

export interface FileDiffInfo {
  path: string
  displayPath?: string
  status?: string
  additions?: number
  deletions?: number
  binary?: boolean
  tooLarge?: boolean
}

export interface SessionFileBase {
  exists?: boolean
  content?: string
  binary?: boolean
}

/** What the session changed that a review can act on: what it changed against HEAD, not against the base branch. */
export async function sessionChanges(ep: Endpoint, sessionId: string): Promise<FileDiffInfo[]> {
  const res = await call<{ files?: FileDiffInfo[] }>(ep, AGENT, 'GetSessionDiff', { sessionId, base: { kind: 'DIFF_BASE_KIND_UNCOMMITTED' } }, 30000)
  return res.files ?? []
}

export function sessionFileBase(ep: Endpoint, sessionId: string, path: string): Promise<SessionFileBase> {
  return call(ep, IDE, 'GetSessionFileBase', { sessionId, path })
}

export function revertSessionFile(ep: Endpoint, sessionId: string, path: string): Promise<{ removed?: boolean }> {
  return call(ep, IDE, 'RevertSessionFile', { sessionId, path })
}
