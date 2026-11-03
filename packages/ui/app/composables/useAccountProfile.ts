// The signed-in user's profile (name, avatar, role…), loaded once and shared by the sidebar and Settings > Account.
import type { AccountPatch, AccountProfile } from '~/lib/account/types'

export function useAccountProfile() {
  const store = useSessionStore()
  const profile = useState<AccountProfile | null>('account-profile', () => null)
  const error = useState('account-profile-error', () => '')

  async function load(force = false) {
    if (!store.transport || (profile.value && !force)) return
    try {
      profile.value = await store.transport.account().me()
      error.value = ''
    } catch (e) {
      error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
    }
  }

  async function update(patch: AccountPatch) {
    if (!store.transport) return
    profile.value = await store.transport.account().update(patch)
  }

  return { profile, error, load, update }
}

// Resizes a picked image to a square avatar of at most `side` pixels (cover crop) and encodes it as WebP
// (JPEG where WebP is not supported), lowering the quality until it fits in maxBytes.
export async function avatarDataUrl(file: File, side: number, maxBytes: number): Promise<string> {
  const bitmap = await createImageBitmap(file)
  const s = Math.min(side, bitmap.width, bitmap.height)
  const canvas = document.createElement('canvas')
  canvas.width = canvas.height = s
  const crop = Math.min(bitmap.width, bitmap.height)
  canvas.getContext('2d')!.drawImage(bitmap, (bitmap.width - crop) / 2, (bitmap.height - crop) / 2, crop, crop, 0, 0, s, s)
  bitmap.close()
  for (const q of [0.9, 0.8, 0.65, 0.5]) {
    let url = canvas.toDataURL('image/webp', q)
    if (!url.startsWith('data:image/webp')) url = canvas.toDataURL('image/jpeg', q)
    if ((url.length - url.indexOf(',') - 1) * 0.75 <= maxBytes) return url
  }
  throw new Error('The picture is too large even after resizing.')
}
