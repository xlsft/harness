// In-memory UsersApi for the mock transport. Follows the server's rules closely enough to exercise the Users
// section: admin only, the last admin cannot be demoted, disabled or deleted, nobody disables or deletes
// themselves, directory users have no local password, passwords are write-only.

import type { LdapSettings, NewUser, UserProviderView, UserRole, UserSessions, UsersApi, UserView } from './types'
import { emptyLdapSettings } from './types'

const now = () => new Date().toISOString()

export class MockUsersApi implements UsersApi {
  private users: UserView[]
  private ldap: LdapSettings = emptyLdapSettings()
  private devices: Record<string, UserSessions> = {}
  private userProviders: Record<string, UserProviderView[]> = {}
  private seq = 0

  constructor(private readonly admin = true) {
    const day = 86_400_000
    const at = (d: number) => new Date(Date.now() - d * day).toISOString()
    this.users = [
      { id: 'usr_me', name: 'You', username: 'you', email: 'you@example.com', role: 'admin', source: 'local', disabled: false, disabledReason: '', createdAt: at(90), lastSignInAt: at(0), lastSeenAt: at(0), isSelf: true },
      { id: 'usr_ada', name: 'Ada Lovelace', username: 'ada', email: 'ada@example.com', role: 'user', source: 'ldap', disabled: false, disabledReason: '', createdAt: at(30), lastSignInAt: at(2), lastSeenAt: at(1), isSelf: false },
      { id: 'usr_bob', name: 'Bob', username: '', email: 'bob@example.com', role: 'user', source: 'local', disabled: true, disabledReason: 'Left the team', createdAt: at(60), lastSignInAt: '', lastSeenAt: '', isSelf: false }
    ]
    this.devices.usr_ada = { devices: [{ id: 'dev_1', name: 'ada-laptop', os: 'darwin', arch: 'arm64', createdAt: at(10), lastSeenAt: at(1) }], webSessions: 1 }
    this.userProviders.usr_ada = [{ id: 'u-ada1', type: 'openai-chat', name: 'OpenRouter', baseUrl: 'https://openrouter.ai/api/v1', keySet: true, enabled: true, modelCount: 2 }]
  }

  private check() {
    if (!this.admin) throw new Error('Admin role required')
  }

  private find(id: string) {
    this.check()
    const u = this.users.find(x => x.id === id)
    if (!u) throw new Error('User not found')
    return u
  }

  private lastAdmin(u: UserView) {
    return u.role === 'admin' && !u.disabled && !this.users.some(x => x.id !== u.id && x.role === 'admin' && !x.disabled)
  }

  async isAdmin() {
    return this.admin
  }

  async list(query = '') {
    this.check()
    const q = query.trim().toLowerCase()
    return this.users.filter(u => !q || [u.name, u.username, u.email].some(v => v.toLowerCase().includes(q))).map(u => ({ ...u }))
  }

  async create(n: NewUser) {
    this.check()
    const email = n.email.trim().toLowerCase()
    if (!n.name.trim() || !email.includes('@')) throw new Error('Name and a valid email are required')
    if (this.users.some(u => u.email === email)) throw new Error('A user with this email already exists')
    if (n.ldap && !n.username.trim()) throw new Error('Directory users need their directory username')
    if (!n.ldap && !n.generatePassword && n.password.length < 8) throw new Error('Passwords have at least 8 characters')
    const user: UserView = {
      id: `usr_${++this.seq}`,
      name: n.name.trim(),
      username: n.username.trim().toLowerCase(),
      email,
      role: n.role,
      source: n.ldap ? 'ldap' : 'local',
      disabled: false,
      disabledReason: '',
      createdAt: now(),
      lastSignInAt: '',
      lastSeenAt: '',
      isSelf: false
    }
    this.users.push(user)
    return { user: { ...user }, generatedPassword: !n.ldap && n.generatePassword ? `mock-${Math.random().toString(36).slice(2, 14)}` : '' }
  }

  async resetPassword(userId: string, o: { password?: string, generate?: boolean }) {
    const u = this.find(userId)
    if (u.source === 'ldap') throw new Error('This user signs in through LDAP')
    if (!o.generate && (o.password ?? '').length < 8) throw new Error('Passwords have at least 8 characters')
    return o.generate ? `mock-${Math.random().toString(36).slice(2, 14)}` : ''
  }

  async setRole(userId: string, role: UserRole) {
    const u = this.find(userId)
    if (role !== 'admin' && this.lastAdmin(u)) throw new Error('This is the last admin')
    u.role = role
    return { ...u }
  }

  async setDisabled(userId: string, disabled: boolean, reason = '') {
    const u = this.find(userId)
    if (disabled && u.isSelf) throw new Error('You cannot disable yourself')
    if (disabled && this.lastAdmin(u)) throw new Error('This is the last admin')
    u.disabled = disabled
    u.disabledReason = disabled ? reason || 'Disabled by an administrator' : ''
    if (disabled) this.devices[u.id] = { devices: [], webSessions: 0 }
    return { ...u }
  }

  async remove(userId: string) {
    const u = this.find(userId)
    if (u.isSelf) throw new Error('You cannot delete yourself')
    if (this.lastAdmin(u)) throw new Error('This is the last admin')
    this.users = this.users.filter(x => x.id !== userId)
  }

  async sessions(userId: string) {
    this.find(userId)
    const s = this.devices[userId] ?? { devices: [], webSessions: 0 }
    return { devices: [...s.devices], webSessions: s.webSessions }
  }

  async revokeSessions(userId: string, deviceId = '') {
    this.find(userId)
    const s = this.devices[userId]
    if (!s) return
    if (deviceId) s.devices = s.devices.filter(d => d.id !== deviceId)
    else this.devices[userId] = { devices: [], webSessions: 0 }
  }

  async providers(userId: string) {
    this.find(userId)
    return [...(this.userProviders[userId] ?? [])]
  }

  async removeProvider(userId: string, providerId: string) {
    this.find(userId)
    this.userProviders[userId] = (this.userProviders[userId] ?? []).filter(p => p.id !== providerId)
  }

  async getLdap() {
    this.check()
    return { ...this.ldap, bindPassword: '' }
  }

  async setLdap(s: LdapSettings) {
    this.check()
    if (s.enabled && (!s.urls.length || !s.userBaseDn)) throw new Error('LDAP needs a server URL and a user search base DN')
    const set = s.clearBindPassword ? false : !!s.bindPassword || this.ldap.bindPasswordSet
    this.ldap = { ...s, bindPassword: '', clearBindPassword: false, bindPasswordSet: set }
    return { ...this.ldap }
  }

  async testLdap(s: LdapSettings) {
    this.check()
    return s.urls.length ? { ok: true, message: `Connected to ${s.urls[0]} (mock)` } : { ok: false, message: 'No LDAP server URL configured' }
  }

  async testLdapUser(_s: LdapSettings, login: string) {
    this.check()
    const u = this.users.find(x => x.source === 'ldap' && (x.username === login || x.email === login))
    if (!u) return { found: false, message: 'No single directory entry matches this login', dn: '', username: '', email: '', name: '', groups: [], allowed: false, admin: false, userId: '' }
    return { found: true, message: 'Found', dn: `uid=${u.username},ou=people,dc=example,dc=com`, username: u.username, email: u.email, name: u.name, groups: ['cn=staff,ou=groups,dc=example,dc=com'], allowed: true, admin: false, userId: u.id }
  }
}
