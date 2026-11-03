// System notifications when a session needs you and you are not looking: a turn finished, a turn stopped
// with an error, or the agent waits for input (a permission, a question, a plan). Nothing is sent while the
// window has focus, and a session notifies at most once per COOLDOWN_MS.
import { notify } from '~/lib/desktop'
import type { SessionState } from '~/lib/session/types'

const COOLDOWN_MS = 30_000

export function useTurnNotifications() {
  const store = useSessionStore()
  const last: Record<string, number> = {}

  watch(() => store.sessionList.map(s => [s.meta.id, s.meta.status] as const), (now, before) => {
    const prev = new Map(before ?? [])
    for (const [id, status] of now) {
      const was = prev.get(id)
      if (!was || was === status || document.hasFocus()) continue
      const s = store.sessions[id] as SessionState
      const body = status === 'needs_input'
        ? 'Needs your input'
        : was === 'working' && status === 'idle'
          ? 'Finished'
          : was === 'working' && status === 'error' ? 'Stopped with an error' : ''
      if (!body || Date.now() - (last[id] ?? 0) < COOLDOWN_MS) continue
      last[id] = Date.now()
      notify(s.meta.title || 'xlsft`s harness', body).catch(() => {})
    }
  })
}
