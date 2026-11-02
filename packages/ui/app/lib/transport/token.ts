// The bearer token for `harness-client serve --listen`. It comes from, in order:
//   1. the `agentToken` query parameter (saved to sessionStorage and removed from the URL, so it does not
//      linger in history or leak through Referer),
//   2. sessionStorage (a token passed earlier in this tab),
//   3. runtime config (NUXT_PUBLIC_AGENT_TOKEN).

export const AGENT_TOKEN_PARAM = 'agentToken'
export const AGENT_TOKEN_STORAGE_KEY = 'harness.agentToken'

export interface TokenEnv {
  /** The current URL (location.href). */
  href?: string
  storage?: Pick<Storage, 'getItem' | 'setItem'>
  /** Replaces the current URL without navigating (history.replaceState). */
  replaceUrl?: (url: string) => void
}

export function resolveAgentToken(configToken: string, env: TokenEnv = {}): string {
  if (env.href) {
    const url = new URL(env.href)
    const fromQuery = url.searchParams.get(AGENT_TOKEN_PARAM)
    if (fromQuery !== null) {
      url.searchParams.delete(AGENT_TOKEN_PARAM)
      env.replaceUrl?.(url.toString())
      if (fromQuery) {
        try {
          env.storage?.setItem(AGENT_TOKEN_STORAGE_KEY, fromQuery)
        } catch {
          // Storage can be unavailable (private mode, blocked site data): use the token for this load only.
        }
        return fromQuery
      }
    }
  }
  try {
    const stored = env.storage?.getItem(AGENT_TOKEN_STORAGE_KEY)
    if (stored) return stored
  } catch {
    // See above.
  }
  return configToken
}

/** resolveAgentToken against the browser's location and sessionStorage. */
export function browserAgentToken(configToken: string): string {
  if (typeof window === 'undefined') return configToken
  let storage: Storage | undefined
  try {
    storage = window.sessionStorage
  } catch {
    storage = undefined
  }
  return resolveAgentToken(configToken, {
    href: window.location.href,
    storage,
    replaceUrl: url => window.history.replaceState(window.history.state, '', url)
  })
}
