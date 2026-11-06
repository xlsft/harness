// The page's side of an editor extension (lib/ide.ts): context from the editor lands in the composer, the open session is
// reported so the editor's commands act on it, and a file named in the transcript opens in the editor.
import { ideHost, ideMention, onHostMessage, parseFileRef } from '~/lib/ide'

export function useIdeHost() {
  const host = ideHost()
  if (!host) return
  const store = useSessionStore()
  const router = useRouter()
  const attach = useState<string[]>('composer-attach', () => [])
  const appended = useState<string>('composer-append', () => '')
  const { setMode } = useAppearance()
  setMode(host.kind)

  const stop = onHostMessage((m) => {
    switch (m.type) {
      case 'add-context':
        attach.value = [...attach.value, ideMention(m.path, m.lineStart, m.lineEnd, store.current?.meta.cwd ?? host.folder)]
        break
      case 'add-text':
        appended.value = m.text
        break
      case 'new-session':
        router.push({ path: '/', query: { new: String(Date.now()), cwd: m.folder || host.folder } })
        break
      case 'interrupt':
        if (store.currentId) store.interrupt(store.currentId)
        break
      case 'focus-composer':
        document.querySelector<HTMLElement>('textarea, [contenteditable="true"]')?.focus()
        break
      case 'theme':
        setMode(m.kind)
        break
    }
  })

  // Inline code in the transcript that names a file opens it. Links and code blocks keep their own behavior.
  const onClick = (e: MouseEvent) => {
    const code = (e.target as Element | null)?.closest?.('code')
    if (!code || code.closest('pre, a, button') || !window.getSelection()?.isCollapsed) return
    const ref = parseFileRef(code.textContent ?? '', store.current?.meta.cwd ?? host.folder)
    if (ref) host.post({ type: 'open-file', path: ref.path, line: ref.line, endLine: ref.endLine })
  }
  document.addEventListener('click', onClick)

  watch(() => [store.currentId, store.current?.meta.cwd], () => {
    host.post({ type: 'session', id: store.currentId, cwd: store.current?.meta.cwd ?? '' })
  }, { immediate: true })
  watch(() => store.connectionLost, lost => lost && host.post({ type: 'connection-lost' }))
  host.post({ type: 'ready', connected: store.ready, error: store.error ?? '' })

  onBeforeUnmount(() => {
    stop()
    document.removeEventListener('click', onClick)
  })
}
