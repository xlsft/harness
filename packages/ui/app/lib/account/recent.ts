// Accounts this window has been signed in to (desktop): server, email and name, for the accounts in the account menu. Kept in local storage
// of this device only; no tokens. The sign-in screen reads LAST_SERVER, and starts at once when AUTO_SIGN_IN is set.

export const LAST_SERVER = 'harness-server-url'
export const AUTO_SIGN_IN = 'harness-sign-in-auto'
const RECENT = 'harness-recent-accounts'
const MAX = 6

export interface RecentAccount {
  server: string
  email: string
  name: string
}

export function recentAccounts(): RecentAccount[] {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT) || '[]')
    return Array.isArray(v) ? v.filter(a => a && typeof a.server === 'string' && typeof a.email === 'string').slice(0, MAX) : []
  } catch {
    return []
  }
}

// rememberAccount puts the signed-in account first.
export function rememberAccount(a: RecentAccount) {
  if (!a.server || !a.email) return
  try {
    const rest = recentAccounts().filter(r => !(r.server === a.server && r.email === a.email))
    localStorage.setItem(RECENT, JSON.stringify([a, ...rest].slice(0, MAX)))
  } catch {
    // Not remembered.
  }
}

// takeAutoSignIn reports whether the sign-in screen should start on its own, once.
export function takeAutoSignIn(): boolean {
  try {
    const v = localStorage.getItem(AUTO_SIGN_IN) === '1'
    localStorage.removeItem(AUTO_SIGN_IN)
    return v
  } catch {
    return false
  }
}
