// Which right panes are open, per session (each session keeps its own set, remembered in this browser).
// Several panes can be open at once; session details is always the first column. An old ?panes= / ?pane=
// link is applied once to the session it opens.
// `?menu=<name>` opens a pane's menu on load (used to reach the menu states of the artboards).
import { isDesktop } from '~/lib/desktop'

export const PANES = ['details', 'terminal', 'diff', 'browser', 'files', 'artifact', 'sidechat', 'tasks', 'agents', 'sentfile', 'visual'] as const
export type PaneName = typeof PANES[number]

export const PANE_WIDTH: Record<PaneName, number> = {
  details: 264,
  terminal: 600,
  diff: 600,
  browser: 600,
  files: 640,
  artifact: 640,
  sidechat: 600,
  tasks: 600,
  agents: 600,
  sentfile: 640,
  visual: 640
}

const isPane = (p: string): p is PaneName => (PANES as readonly string[]).includes(p)

// Details is the narrow summary column: it stays next to the transcript, before the other panes, whatever the
// opening order.
function normalize(list: PaneName[]): PaneName[] {
  const uniq = [...new Set(list)]
  return uniq.includes('details') ? ['details', ...uniq.filter(p => p !== 'details')] : uniq
}

const STORAGE_KEY = 'harness-panes'

function loadAll(): Record<string, PaneName[]> {
  if (!import.meta.client) return {}
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}') as Record<string, string[]>
    return Object.fromEntries(Object.entries(raw).map(([id, list]) => [id, normalize(list.filter(isPane))]))
  } catch {
    return {}
  }
}

function saveAll(all: Record<string, PaneName[]>) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(all))
  } catch {
    // Storage unavailable: panes reset on reload.
  }
}

export function usePanes() {
  const route = useRoute()
  const router = useRouter()
  const store = useSessionStore()

  // Phones open panes on demand only (they cover the transcript); desktop shows session details by default.
  const desktop = useState('layout-desktop', () => import.meta.client ? isDesktop() || window.matchMedia('(min-width: 1024px)').matches : true)

  // Open panes per session (each session keeps its own set), remembered in this browser.
  const bySession = useState<Record<string, PaneName[]>>('panes-by-session', loadAll)
  const sid = computed(() => store.currentId || '_')

  const panes = computed<PaneName[]>(() => bySession.value[sid.value] ?? (desktop.value ? ['details'] : []))

  const menu = computed(() => typeof route.query.menu === 'string' ? route.query.menu : '')

  function setPanes(next: PaneName[]) {
    bySession.value = { ...bySession.value, [sid.value]: normalize(next) }
    saveAll(bySession.value)
    if (route.query.menu) {
      const { menu: _menu, ...query } = route.query
      router.replace({ query })
    }
  }

  // Old links (?panes=terminal,browser or ?pane=terminal) apply once to the session they open, then leave the URL.
  if (import.meta.client) {
    watch([() => route.query.panes ?? route.query.pane, sid], ([raw]) => {
      if (typeof raw !== 'string' || !store.currentId) return
      setPanes(raw === 'none' ? [] : raw.split(',').filter(isPane))
      const { panes: _p, pane: _q, ...query } = route.query
      router.replace({ query })
    }, { immediate: true })
  }

  const isOpen = (p: PaneName) => panes.value.includes(p)
  const openPane = (p: PaneName) => {
    if (!isOpen(p)) setPanes([...panes.value, p])
  }
  const closePane = (p: PaneName) => setPanes(panes.value.filter(x => x !== p))
  const togglePane = (p: PaneName) => isOpen(p) ? closePane(p) : openPane(p)

  return { panes, menu, isOpen, openPane, closePane, togglePane }
}
