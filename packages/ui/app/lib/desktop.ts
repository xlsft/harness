// The desktop shell (packages/desktop, Tauri 2). Inside it the UI gets its agent endpoint and sign-in from
// the shell, which owns the harness-client sidecar, and can use native dialogs and Finder. In a browser
// none of this exists and isDesktop() is false. Tauri modules load lazily so the web build does not need them.

export interface AuthStatus {
  signed_in: boolean
  email?: string
  name?: string
  server?: string
  role?: string
  error?: string
}

export type LoginEvent
  = | { event: 'code', verification_uri: string, user_code: string }
    | { event: 'signed_in', server: string, email: string }
    | { event: 'error', message: string }

export interface Endpoint {
  url: string
  token: string
}

export const isDesktop = () => import.meta.client && '__TAURI_INTERNALS__' in window

async function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const { invoke } = await import('@tauri-apps/api/core')
  return invoke<T>(cmd, args)
}

export const authStatus = () => invoke<AuthStatus>('auth_status')
export const agentEndpoint = () => invoke<Endpoint>('agent_endpoint')
export const logout = () => invoke<null>('logout')
export const cancelLogin = () => invoke<null>('cancel_login')

/** Starts the device-flow sign-in; events arrive until signed_in or error. Returns a stop function. */
export async function login(server: string, onEvent: (e: LoginEvent) => void): Promise<() => void> {
  const { listen } = await import('@tauri-apps/api/event')
  const unlisten = await listen<LoginEvent>('login-event', e => onEvent(e.payload))
  await invoke('login', { server })
  return unlisten
}

/** Native folder picker; null when cancelled. */
export async function pickFolder(defaultPath?: string): Promise<string | null> {
  const { open } = await import('@tauri-apps/plugin-dialog')
  const picked = await open({ directory: true, multiple: false, defaultPath: defaultPath || undefined })
  return typeof picked === 'string' ? picked : null
}

/**
 * A system notification: through the shell in the desktop app, the browser's Notification API on the web.
 * Asks for permission the first time; does nothing when it is refused.
 */
export async function notify(title: string, body: string) {
  if (isDesktop()) {
    const n = await import('@tauri-apps/plugin-notification')
    if (!(await n.isPermissionGranted()) && (await n.requestPermission()) !== 'granted') return
    n.sendNotification({ title, body })
    return
  }
  if (!('Notification' in window)) return
  if (Notification.permission === 'default') await Notification.requestPermission()
  if (Notification.permission === 'granted') new Notification(title, { body })
}

/** Opens a URL in the default browser. */
export async function openExternal(url: string) {
  const { openUrl } = await import('@tauri-apps/plugin-opener')
  await openUrl(url)
}

/** Shows a file in Finder / Explorer. */
export async function revealInFolder(path: string) {
  const { revealItemInDir } = await import('@tauri-apps/plugin-opener')
  await revealItemInDir(path)
}

/** Opens a file with its default application. */
export async function openWithDefaultApp(path: string) {
  const { openPath } = await import('@tauri-apps/plugin-opener')
  await openPath(path)
}

/**
 * Quitting the app (⌘Q, the menu, closing the window) asks `check` first: a message to confirm with a native
 * dialog, or '' to quit at once. Returns a stop function.
 */
export async function guardQuit(check: () => string): Promise<() => void> {
  const { listen } = await import('@tauri-apps/api/event')
  return listen('quit-requested', async () => {
    const warning = check()
    if (warning) {
      const { ask } = await import('@tauri-apps/plugin-dialog')
      const quit = await ask(warning, { title: 'Quit xlsft`s harness?', kind: 'warning', okLabel: 'Quit', cancelLabel: 'Cancel' })
      if (!quit) return
    }
    await invoke('confirm_quit')
  })
}
