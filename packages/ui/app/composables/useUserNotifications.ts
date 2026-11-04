// The PushNotification tool in this frontend. It tells the client whether the user is looking at a session (the client
// decides what goes out), and shows the notifications that reach it as system notifications, once each. A viewer on
// another device gets them too while it follows the session; there is no push to a device that is not connected.
import { notify } from '~/lib/desktop'
import { takeUserNotification } from '~/lib/session/userTools'
import { newUuid } from '~/stores/sessions'

const STORAGE_KEY = 'harness-notified'
const RENEW_MS = 20_000

function loadShown(): string[] {
  if (!import.meta.client) return []
  try {
    const v = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

export function useUserNotifications() {
  const store = useSessionStore()
  let shown = loadShown()
  const focused = () => document.hasFocus() && !document.hidden

  watch(() => store.sessionList.map(s => s.userNotification?.id), () => {
    for (const s of store.sessionList) {
      const n = s.userNotification
      const next = takeUserNotification(n, shown, Date.now())
      if (!n || !next) continue
      shown = next
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(shown))
      } catch {
        // Storage unavailable: a reload may show the last one again, while it is fresh.
      }
      if (focused() && store.currentId === s.meta.id) continue
      notify(n.title, n.body).catch(() => {})
    }
  })

  const viewerId = newUuid()
  let reported = ''
  function send(sessionId: string, on: boolean) {
    store.transport?.updateViewerPresence(sessionId, viewerId, on).catch(() => {})
  }
  // The session the user is looking at; renewed while it stays the same, as the client lets a report expire.
  function report() {
    const now = focused() ? store.currentId ?? '' : ''
    if (reported && reported !== now) send(reported, false)
    if (now) send(now, true)
    reported = now
  }

  watch(() => [store.currentId, store.transport], report)
  onMounted(() => {
    const timer = setInterval(report, RENEW_MS)
    window.addEventListener('focus', report)
    window.addEventListener('blur', report)
    document.addEventListener('visibilitychange', report)
    report()
    onBeforeUnmount(() => {
      clearInterval(timer)
      window.removeEventListener('focus', report)
      window.removeEventListener('blur', report)
      document.removeEventListener('visibilitychange', report)
    })
  })
}
