// Show targets (the Show tool): `show` opens the matching pane in the open session and hands it the target
// ('show-target', taken by the Files, Diff or Browser pane once it is mounted). The pane joins the layout like
// any pane opened by hand, so the layout rules apply (details steps aside, a new pane stacks when tight).
//
// `useShowRequests` (mounted once, by the session page) follows the agent's ShowRequested events: a request is
// applied once, when its session is the open one, so a request for a session in the background waits until the
// user opens it. Requests already applied in this browser are remembered, so a reload does not reopen them.
import type { ShowRequest, ShowTarget } from '~/lib/session/types'
import { showPane } from '~/utils/show'

const STORAGE_KEY = 'harness-shown'
// Applied request ids kept (oldest dropped first).
const KEEP = 200

function loadShown(): string[] {
  if (!import.meta.client) return []
  try {
    const v = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

function saveShown(ids: string[]) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(ids))
  } catch {
    // Storage unavailable: a reload may open the last request again.
  }
}

/** Whether `req` still has to be applied, given the ids applied so far; returns the updated list when it does. */
export function takeShowRequest(req: ShowRequest | null | undefined, shown: string[]): string[] | null {
  if (!req || shown.includes(req.id)) return null
  return [...shown, req.id].slice(-KEEP)
}

export function useShow() {
  const { openPane } = usePanes()
  const target = useState<ShowTarget | null>('show-target', () => null)

  function show(t: ShowTarget) {
    target.value = { ...t }
    openPane(showPane(t))
  }

  return { show, target }
}

export function useShowRequests() {
  const store = useSessionStore()
  const { show } = useShow()
  const shown = useState<string[]>('show-applied', loadShown)

  watch(() => [store.currentId, store.current?.showRequest?.id], () => {
    const req = store.current?.showRequest
    const next = takeShowRequest(req, shown.value)
    if (!req || !next) return
    shown.value = next
    saveShown(next)
    show(req.target)
  }, { immediate: true })
}
