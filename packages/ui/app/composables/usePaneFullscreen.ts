import type { PaneName } from '~/composables/usePanes'

// The pane shown full screen, per session. Any pane except session details can be; it stays mounted, so a terminal or a
// browser keeps its state when it grows and shrinks.
export function usePaneFullscreen() {
  const full = useState<Record<string, PaneName | undefined>>('pane-fullscreen', () => ({}))
  const isFull = (sessionId: string, pane: PaneName) => full.value[sessionId] === pane
  const enter = (sessionId: string, pane: PaneName) => {
    full.value = { ...full.value, [sessionId]: pane }
  }
  const leave = (sessionId: string) => {
    full.value = { ...full.value, [sessionId]: undefined }
  }
  return { isFull, enter, leave }
}
