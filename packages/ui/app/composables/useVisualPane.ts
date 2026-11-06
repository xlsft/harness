// The Visual open in the right pane, per session. The card in the transcript opens it and keeps it current while the
// model draws a new version of the same visual.
export interface OpenVisual {
  id: string
  title: string
  html: string
}

export function useVisualPane() {
  const open = useState<Record<string, OpenVisual | undefined>>('visual-pane', () => ({}))
  const { openPane } = usePanes()
  function show(sessionId: string, visual: OpenVisual) {
    open.value = { ...open.value, [sessionId]: visual }
    openPane('visual')
  }
  function update(sessionId: string, visual: OpenVisual) {
    if (open.value[sessionId]?.id === visual.id) open.value = { ...open.value, [sessionId]: visual }
  }
  return { open, show, update }
}
