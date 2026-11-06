// AccountApi: what the Settings > Account page needs from harness.auth.v1.DeviceAuthService for the signed-in
// user (profile, display name, avatar, password, devices). The client forwards these calls to the server with
// its own credentials, so "the current device" is the client the UI talks to. Two implementations: connect.ts
// (the real service through the client) and mock.ts (in memory, for the mock transport).

import { objectUrl, type ObjectRef } from '~/lib/session/objects'

export type AccountRole = 'admin' | 'user' | 'auditor' | 'billing'

export interface AccountProfile {
  id: string
  // Sign-in handle; '' when the account has none.
  username: string
  email: string
  name: string
  // Avatar as a data URL (usable as an <img> src); '' when none is set.
  image: string
  // The avatar in the object store, once it has moved there. Show avatarUrl(profile).
  imageObject?: ObjectRef
  role: AccountRole
  // 'ldap': signs in through the directory; the name and password are managed there.
  source: 'local' | 'ldap' | 'sso' | 'scim'
  // RFC 3339; '' when unknown or never.
  createdAt: string
  lastSignInAt: string
}

export interface AccountDevice {
  id: string
  // The device name the client signed in with (usually the hostname).
  name: string
  // GOOS / GOARCH, e.g. 'darwin' / 'arm64'; '' when unknown.
  os: string
  arch: string
  // The client program, e.g. 'harness-client'; '' when unknown.
  clientName: string
  // RFC 3339; lastSeenAt '' when never.
  createdAt: string
  lastSeenAt: string
  // The client this UI talks through. It cannot be revoked from here (the server refuses).
  current: boolean
}

export interface AccountPatch {
  // Omitted: unchanged.
  name?: string
  // Omitted: unchanged; '' removes the avatar. A data URL of a PNG, JPEG or WebP image of at most
  // AVATAR_MAX_SIDE x AVATAR_MAX_SIDE pixels and AVATAR_MAX_BYTES decoded; resize before uploading.
  image?: string
}

export const AVATAR_MAX_SIDE = 256
export const AVATAR_MAX_BYTES = 200 * 1024
export const AVATAR_TYPES = ['image/png', 'image/jpeg', 'image/webp'] as const

export interface AccountApi {
  me(): Promise<AccountProfile>
  // Directory (LDAP) users cannot change their name; invalid avatars are refused.
  update(patch: AccountPatch): Promise<AccountProfile>
  // Local accounts only. Rejects a wrong current password, a new one outside 8-128 characters, and directory
  // users. revokeOtherSessions also signs out web sessions and every other device (this one stays).
  changePassword(currentPassword: string, newPassword: string, revokeOtherSessions?: boolean): Promise<void>
  // Most recently seen first.
  devices(): Promise<AccountDevice[]>
  // Rejects the current device.
  revokeDevice(deviceId: string): Promise<void>
}

/** What an <img> shows for a profile: the object store's copy, else the one that came with the profile. */
export function avatarUrl(p: Pick<AccountProfile, 'image' | 'imageObject'> | null | undefined): string {
  if (!p) return ''
  return objectUrl(p.imageObject) || p.image
}
