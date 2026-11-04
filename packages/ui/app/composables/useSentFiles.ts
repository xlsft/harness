// SendUserFile: opening a file in the side pane and getting its bytes. On the session's machine they come from the file
// (ReadSentFile); on another device, or when the file is gone, from the copy in the call's result when it has one.
import { decodeBase64, type SentFile } from '~/lib/session/userTools'

export interface SentFileBytes {
  bytes: Uint8Array<ArrayBuffer>
  // The file differs from what was sent.
  changed: boolean
}

// The types a frame or image shows, by kind: whatever the file's name claims, it is not rendered as anything else.
const RENDER_MIME: Partial<Record<SentFile['kind'], string>> = { svg: 'image/svg+xml', pdf: 'application/pdf' }

export function sentFileUrl(file: SentFile, bytes: Uint8Array<ArrayBuffer>): string {
  return URL.createObjectURL(new Blob([bytes], { type: RENDER_MIME[file.kind] ?? file.mime }))
}

export function useSentFiles() {
  const store = useSessionStore()
  const { openPane } = usePanes()
  // The file open in the side pane, per session.
  const open = useState<Record<string, SentFile>>('sent-file', () => ({}))

  function view(sessionId: string, file: SentFile) {
    open.value = { ...open.value, [sessionId]: file }
    openPane('sentfile')
  }

  async function load(sessionId: string, file: SentFile): Promise<SentFileBytes> {
    const t = store.transport
    let failure: unknown = new Error('The file is not available here')
    if (t && !store.sessions[sessionId]?.meta.remote) {
      try {
        const r = await store.whenOpen(sessionId, () => t.readSentFile(sessionId, file.path))
        return { bytes: r.bytes, changed: !!file.sha256 && r.sha256 !== file.sha256 }
      } catch (e) {
        failure = e
      }
    }
    if (file.data) return { bytes: decodeBase64(file.data), changed: false }
    throw failure
  }

  async function download(sessionId: string, file: SentFile) {
    const { bytes } = await load(sessionId, file)
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([bytes], { type: 'application/octet-stream' }))
    a.download = file.name
    a.click()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  }

  return { open, view, load, download }
}
