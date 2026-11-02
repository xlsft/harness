// Which right pane is open, kept in the URL (?pane=terminal) so every screen of the design is linkable.
// `?menu=<name>` opens a pane's menu on load (used to reach the menu states of the artboards).

export const PANES = ['details', 'terminal', 'diff', 'browser', 'files', 'artifact', 'sidechat', 'tasks', 'agents'] as const
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
  agents: 600
}

export function usePanes() {
  const route = useRoute()
  const router = useRouter()

  // Phones open panes on demand only (they cover the transcript); desktop shows session details by default.
  const desktop = useState('layout-desktop', () => import.meta.client ? window.matchMedia('(min-width: 1024px)').matches : true)

  const pane = computed<PaneName | null>(() => {
    const p = route.query.pane
    if (p === 'none') return null
    if (typeof p === 'string' && (PANES as readonly string[]).includes(p)) return p as PaneName
    return desktop.value ? 'details' : null
  })

  const menu = computed(() => typeof route.query.menu === 'string' ? route.query.menu : '')

  function setPane(next: PaneName | null) {
    const query = { ...route.query }
    delete query.menu
    if (next === 'details') delete query.pane
    else query.pane = next ?? 'none'
    router.replace({ query })
  }

  function togglePane(next: PaneName) {
    setPane(pane.value === next ? null : next)
  }

  return { pane, menu, setPane, togglePane }
}
