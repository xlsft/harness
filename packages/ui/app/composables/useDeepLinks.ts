// harness:// links in the desktop app: the shell checks and queues them (src-tauri/src/deeplink.rs), this takes them once
// the UI is ready and goes where they say. A `new` link leaves its prompt as a draft in the new-session composer, where
// the user reads it and sends it, or not: a link never starts anything by itself.
import { deepLinkRoute, linkDraft, type LinkDraft } from '~/lib/deeplink'
import { isDesktop, onDeepLinks, takeDeepLinks } from '~/lib/desktop'

/** The draft the last `new` link left, until the new-session screen takes it. */
export const useLinkDraft = () => useState<LinkDraft | null>('link-draft', () => null)

let started = false

/** Follows links from now on, and takes those that came before. Once per page load. */
export function startDeepLinks() {
  if (started || !isDesktop()) return
  started = true
  const draft = useLinkDraft()
  async function take() {
    let links
    try {
      links = await takeDeepLinks()
    } catch {
      return
    }
    // Several at once (rare): the last one decides where the UI ends up.
    const link = links.at(-1)
    if (!link) return
    const d = linkDraft(link)
    if (d) draft.value = d
    await navigateTo(deepLinkRoute(link))
  }
  void onDeepLinks(() => void take())
  void take()
}
