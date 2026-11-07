// Which sessions have the "Record a demo" picker open above the composer (opened from the tools menu).
export function useDemoRecordingPicker() {
  const open = useState<Record<string, boolean>>('demo-recording-picker', () => ({}))
  return {
    isOpen: (sessionId: string) => !!open.value[sessionId],
    open: (sessionId: string) => {
      open.value = { ...open.value, [sessionId]: true }
    },
    close: (sessionId: string) => {
      const { [sessionId]: _, ...rest } = open.value
      open.value = rest
    }
  }
}
