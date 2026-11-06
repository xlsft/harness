// Screen reader mode (Settings > Appearance, kept in this browser). On: <html data-screen-reader> takes away motion and decoration
// (main.css), and the session on screen is spoken as moments through two live regions (lib/a11y/announce.ts). Off, a reader still
// gets the ARIA labels every screen has; only the announcements and the plainer look are this mode's.
import { announcements, momentOf, type Announcement, type SessionMoment } from '~/lib/a11y/announce'

const KEY = 'harness-screen-reader'

const enabled = ref(false)
const polite = ref('')
const assertive = ref('')
let started = false

function apply(on: boolean) {
  if (import.meta.client) {
    if (on) document.documentElement.dataset.screenReader = ''
    else delete document.documentElement.dataset.screenReader
  }
}

// A region only speaks when its text changes: saying the same words twice needs a change in between.
function say(a: Announcement) {
  const region = a.priority === 'assertive' ? assertive : polite
  region.value = ''
  void nextTick(() => {
    region.value = a.text
  })
}

export function useScreenReader() {
  function start() {
    if (started || !import.meta.client) return
    started = true
    try {
      enabled.value = localStorage.getItem(KEY) === '1'
    } catch { /* private mode: off */ }
    apply(enabled.value)
    const store = useSessionStore()
    let last: SessionMoment | null = null
    watch(() => (enabled.value && store.current ? momentOf(store.current) : null), (next) => {
      if (next) for (const a of announcements(last, next)) say(a)
      last = next
    })
  }
  function set(on: boolean) {
    enabled.value = on
    apply(on)
    try {
      if (on) localStorage.setItem(KEY, '1')
      else localStorage.removeItem(KEY)
    } catch { /* kept for this page only */ }
  }
  return { start, enabled: readonly(enabled), set, polite: readonly(polite), assertive: readonly(assertive) }
}
