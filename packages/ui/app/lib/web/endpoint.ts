// The client the web UI talks to when the server serves it (transport "web"). Chosen per tab: sessionStorage, nothing on the
// server. `?agentUrl=` and `?agentToken=` work once, for a link a client can print; they are removed from the address bar.

import { AGENT_TOKEN_PARAM, AGENT_TOKEN_STORAGE_KEY } from '~/lib/transport/token'

export const AGENT_URL_PARAM = 'agentUrl'
export const AGENT_URL_STORAGE_KEY = 'harness.agentUrl'

export interface ClientEndpoint {
  url: string
  token: string
}

interface Env {
  href: string
  storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>
  replaceUrl: (url: string) => void
}

/** An address a browser can call: http or https, nothing else. */
function validUrl(raw: string): string {
  try {
    const u = new URL(raw)
    return u.protocol === 'http:' || u.protocol === 'https:' ? raw.replace(/\/+$/, '') : ''
  } catch {
    return ''
  }
}

export function resolveClientEndpoint(env: Env): ClientEndpoint | null {
  const url = new URL(env.href)
  const fromQuery = url.searchParams.get(AGENT_URL_PARAM)
  const tokenFromQuery = url.searchParams.get(AGENT_TOKEN_PARAM)
  if (fromQuery !== null || tokenFromQuery !== null) {
    url.searchParams.delete(AGENT_URL_PARAM)
    url.searchParams.delete(AGENT_TOKEN_PARAM)
    env.replaceUrl(url.toString())
  }
  if (fromQuery && validUrl(fromQuery)) env.storage.setItem(AGENT_URL_STORAGE_KEY, validUrl(fromQuery))
  if (tokenFromQuery) env.storage.setItem(AGENT_TOKEN_STORAGE_KEY, tokenFromQuery)
  const saved = validUrl(env.storage.getItem(AGENT_URL_STORAGE_KEY) ?? '')
  if (!saved) return null
  return { url: saved, token: env.storage.getItem(AGENT_TOKEN_STORAGE_KEY) ?? '' }
}

function browserEnv(): Env {
  return {
    href: window.location.href,
    storage: window.sessionStorage,
    replaceUrl: u => window.history.replaceState(window.history.state, '', u)
  }
}

export function browserClientEndpoint(): ClientEndpoint | null {
  try {
    return resolveClientEndpoint(browserEnv())
  } catch {
    return null
  }
}

export function saveClientEndpoint(url: string, token: string): void {
  window.sessionStorage.setItem(AGENT_URL_STORAGE_KEY, url.replace(/\/+$/, ''))
  window.sessionStorage.setItem(AGENT_TOKEN_STORAGE_KEY, token)
}

export function forgetClientEndpoint(): void {
  window.sessionStorage.removeItem(AGENT_URL_STORAGE_KEY)
  window.sessionStorage.removeItem(AGENT_TOKEN_STORAGE_KEY)
}
