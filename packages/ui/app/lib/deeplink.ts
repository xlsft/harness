// harness:// links in the desktop app. The shell checks every part of a link before the UI hears of it
// (src-tauri/src/deeplink.rs) and hands over only what passed; this maps a link to where the UI goes. A link never runs
// anything: a draft's prompt waits in the new-session composer until the user sends it.

export type DeepLink
  = | { kind: 'session', id: string }
    | { kind: 'new', prompt: string, folder: string, folder_dropped: boolean }
    | { kind: 'settings', section: string }

/** What a `new` link leaves for the new-session screen. */
export interface LinkDraft {
  prompt: string
  folder: string
  folderDropped: boolean
}

export interface LinkRoute {
  path: string
  query: Record<string, string>
}

/** Where a link goes. The new-session screen reads ?cwd= for its folder; the draft travels separately (linkDraft). */
export function deepLinkRoute(link: DeepLink): LinkRoute {
  switch (link.kind) {
    case 'session':
      return { path: '/', query: { session: link.id } }
    case 'settings':
      return { path: '/', query: { settings: link.section } }
    case 'new':
      return { path: '/', query: link.folder ? { new: '1', cwd: link.folder } : { new: '1' } }
  }
}

export function linkDraft(link: DeepLink): LinkDraft | null {
  return link.kind === 'new' ? { prompt: link.prompt, folder: link.folder, folderDropped: link.folder_dropped } : null
}
