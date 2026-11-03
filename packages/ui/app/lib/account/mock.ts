// In-memory AccountApi for the mock transport. Follows the server's rules closely enough to exercise the Account
// page: directory users keep their name and have no local password, avatars must be small PNG/JPEG/WebP data
// URLs, the current device cannot be revoked. The mock's password is 'password-123'.

import type { AccountApi, AccountDevice, AccountPatch, AccountProfile } from './types'
import { AVATAR_MAX_BYTES } from './types'

export class MockAccountApi implements AccountApi {
  private profile: AccountProfile
  private list: AccountDevice[]
  private password = 'password-123'

  constructor(source: 'local' | 'ldap' = 'local') {
    const day = 86_400_000
    const at = (d: number) => new Date(Date.now() - d * day).toISOString()
    this.profile = {
      id: 'usr_me',
      username: 'you',
      email: 'you@example.com',
      name: 'You',
      image: '',
      role: 'admin',
      source,
      createdAt: at(90),
      lastSignInAt: at(0.2)
    }
    this.list = [
      { id: 'dev_this', name: 'my-macbook', os: 'darwin', arch: 'arm64', clientName: 'harness-client', createdAt: at(30), lastSeenAt: at(0), current: true },
      { id: 'dev_ws', name: 'build-box', os: 'linux', arch: 'amd64', clientName: 'harness-client', createdAt: at(12), lastSeenAt: at(1.5), current: false },
      { id: 'dev_old', name: 'old-laptop', os: 'windows', arch: 'amd64', clientName: 'harness-client', createdAt: at(80), lastSeenAt: at(40), current: false }
    ]
  }

  async me() {
    return { ...this.profile }
  }

  async update(patch: AccountPatch) {
    if (patch.name !== undefined) {
      const name = patch.name.trim()
      if (!name) throw new Error('Name is required')
      if (name.length > 100) throw new Error('Names have at most 100 characters')
      if (name !== this.profile.name && this.profile.source === 'ldap') {
        throw new Error('Your name comes from the directory (LDAP) and is updated at every sign-in')
      }
      this.profile.name = name
    }
    if (patch.image !== undefined) {
      const m = /^data:image\/(png|jpeg|webp);base64,(.+)$/.exec(patch.image)
      if (patch.image && !m) throw new Error('The avatar must be a PNG, JPEG or WebP image (a base64 data URL)')
      if (m && Math.floor(m[2]!.length * 3 / 4) > AVATAR_MAX_BYTES) throw new Error(`The avatar must be at most ${AVATAR_MAX_BYTES / 1024} KB`)
      this.profile.image = patch.image
    }
    return { ...this.profile }
  }

  async changePassword(currentPassword: string, newPassword: string, revokeOtherSessions = false) {
    if (this.profile.source === 'ldap') throw new Error('You sign in through the directory (LDAP); change your password there')
    if (currentPassword !== this.password) throw new Error('The current password is incorrect')
    if (newPassword.length < 8) throw new Error('Passwords have at least 8 characters')
    if (newPassword.length > 128) throw new Error('Passwords have at most 128 characters')
    this.password = newPassword
    if (revokeOtherSessions) this.list = this.list.filter(d => d.current)
  }

  async devices() {
    return this.list.map(d => ({ ...d }))
  }

  async revokeDevice(deviceId: string) {
    const d = this.list.find(x => x.id === deviceId)
    if (!d) throw new Error('Device not found')
    if (d.current) throw new Error('This is the device you are using; sign out on it instead')
    this.list = this.list.filter(x => x.id !== deviceId)
  }
}
