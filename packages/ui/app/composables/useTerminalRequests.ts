// "Open in terminal" from anywhere (the folder and branch menus): opens the terminal pane, which starts a new
// terminal in that folder.
export function useTerminalRequests() {
  const request = useState<{ cwd: string, n: number } | null>('terminal-open-request', () => null)
  const { openPane } = usePanes()
  function openTerminal(cwd: string) {
    request.value = { cwd, n: (request.value?.n ?? 0) + 1 }
    openPane('terminal')
  }
  return { request, openTerminal }
}
