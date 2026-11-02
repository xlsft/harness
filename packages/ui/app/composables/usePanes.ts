// Which right panes are open, kept in the URL (?panes=terminal,browser) so every screen of the design is
// linkable. Several panes can be open at once; they sit side by side in the order they were opened, with
// session details always last. `?pane=<name>` (a single pane) is still accepted.
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

const isPane = (p: string): p is PaneName => (PANES as readonly string[]).includes(p)

// Details is the narrow summary column: it stays at the far right whatever the opening order.
function normalize(list: PaneName[]): PaneName[] {
  const uniq = [...new Set(list)]
  return uniq.includes('details') ? [...uniq.filter(p => p !== 'details'), 'details'] : uniq
}

export function usePanes() {
  const route = useRoute()
  const router = useRouter()

  // Phones open panes on demand only (they cover the transcript); desktop shows session details by default.
  const desktop = useState('layout-desktop', () => import.meta.client ? window.matchMedia('(min-width: 1024px)').matches : true)

  const panes = computed<PaneName[]>(() => {
    const raw = route.query.panes ?? route.query.pane
    if (raw === 'none' || raw === '') return []
    if (typeof raw === 'string') return normalize(raw.split(',').filter(isPane))
    return desktop.value ? ['details'] : []
  })

  const menu = computed(() => typeof route.query.menu === 'string' ? route.query.menu : '')

  function setPanes(next: PaneName[]) {
    const query = { ...route.query }
    delete query.menu
    delete query.pane
    const list = normalize(next)
    const fallback = desktop.value ? ['details'] : []
    if (list.join(',') === fallback.join(',')) delete query.panes
    else query.panes = list.length ? list.join(',') : 'none'
    router.replace({ query })
  }

  const isOpen = (p: PaneName) => panes.value.includes(p)
  const openPane = (p: PaneName) => {
    if (!isOpen(p)) setPanes([...panes.value, p])
  }
  const closePane = (p: PaneName) => setPanes(panes.value.filter(x => x !== p))
  const togglePane = (p: PaneName) => isOpen(p) ? closePane(p) : openPane(p)

  return { panes, menu, isOpen, openPane, closePane, togglePane }
}
