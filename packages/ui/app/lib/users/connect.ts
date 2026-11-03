// UsersApi over harness.auth.v1.AdminService, on the same connect transport (and agent token) as AgentService:
// the client forwards these calls to the server (packages/client/server/coreproxy.go).

import { Code, ConnectError, createClient, type Client, type Transport } from '@connectrpc/connect'
import {
  AdminService,
  LdapLocalLogin,
  UserSource,
  type AdminUser,
  type LdapConfig
} from '~/gen/harness/auth/v1/admin_pb'
import { User_Role } from '~/gen/harness/auth/v1/auth_pb'
import type { LdapSettings, NewUser, UserRole, UsersApi, UserView } from './types'

export function userView(u: AdminUser): UserView {
  return {
    id: u.id,
    name: u.name,
    username: u.username,
    email: u.email,
    role: u.role === User_Role.ADMIN ? 'admin' : 'user',
    source: u.source === UserSource.LDAP ? 'ldap' : 'local',
    disabled: u.disabled,
    disabledReason: u.disabledReason,
    createdAt: u.createdAt,
    lastSignInAt: u.lastSignInAt,
    lastSeenAt: u.lastSeenAt,
    isSelf: u.isSelf
  }
}

const role = (r: UserRole) => r === 'admin' ? User_Role.ADMIN : User_Role.USER

export function ldapView(c: LdapConfig | undefined): LdapSettings {
  return {
    enabled: c?.enabled ?? false,
    urls: c?.urls ?? [],
    startTls: c?.startTls ?? false,
    caCert: c?.caCert ?? '',
    skipVerify: c?.skipVerify ?? false,
    bindDn: c?.bindDn ?? '',
    bindPassword: '',
    bindPasswordSet: c?.bindPasswordSet ?? false,
    clearBindPassword: false,
    userBaseDn: c?.userBaseDn ?? '',
    userFilter: c?.userFilter ?? '',
    usernameAttribute: c?.usernameAttribute ?? '',
    emailAttribute: c?.emailAttribute ?? '',
    nameAttribute: c?.nameAttribute ?? '',
    allowedGroupDn: c?.allowedGroupDn ?? '',
    adminGroupDn: c?.adminGroupDn ?? '',
    autoCreateUsers: c?.autoCreateUsers ?? true,
    localLogin: c?.localLogin === LdapLocalLogin.BREAK_GLASS ? 'break_glass' : 'fallback',
    breakGlassLogins: c?.breakGlassLogins ?? [],
    timeoutMs: c?.timeoutMs ?? 5000
  }
}

export function ldapConfig(s: LdapSettings) {
  return {
    enabled: s.enabled,
    urls: s.urls.map(u => u.trim()).filter(Boolean),
    startTls: s.startTls,
    caCert: s.caCert,
    skipVerify: s.skipVerify,
    bindDn: s.bindDn,
    bindPassword: s.bindPassword,
    clearBindPassword: s.clearBindPassword,
    userBaseDn: s.userBaseDn,
    userFilter: s.userFilter,
    usernameAttribute: s.usernameAttribute,
    emailAttribute: s.emailAttribute,
    nameAttribute: s.nameAttribute,
    allowedGroupDn: s.allowedGroupDn,
    adminGroupDn: s.adminGroupDn,
    autoCreateUsers: s.autoCreateUsers,
    localLogin: s.localLogin === 'break_glass' ? LdapLocalLogin.BREAK_GLASS : LdapLocalLogin.FALLBACK,
    breakGlassLogins: s.breakGlassLogins.map(l => l.trim()).filter(Boolean),
    timeoutMs: s.timeoutMs
  }
}

export class ConnectUsersApi implements UsersApi {
  private client: Client<typeof AdminService>

  constructor(transport: Transport) {
    this.client = createClient(AdminService, transport)
  }

  async isAdmin() {
    try {
      await this.client.listUsers({ limit: 1 })
      return true
    } catch (e) {
      // Not an admin, or a client too old to forward AdminService.
      if (e instanceof ConnectError && e.code !== Code.Unavailable) return false
      throw e
    }
  }

  async list(query = '') {
    return (await this.client.listUsers({ query })).users.map(userView)
  }

  async create(u: NewUser) {
    const r = await this.client.createUser({ ...u, role: role(u.role) })
    return { user: userView(r.user!), generatedPassword: r.generatedPassword }
  }

  async resetPassword(userId: string, o: { password?: string, generate?: boolean, revokeSessions?: boolean }) {
    return (await this.client.resetPassword({ userId, password: o.password ?? '', generate: !!o.generate, revokeSessions: !!o.revokeSessions })).generatedPassword
  }

  async setRole(userId: string, r: UserRole) {
    return userView((await this.client.setRole({ userId, role: role(r) })).user!)
  }

  async setDisabled(userId: string, disabled: boolean, reason = '') {
    return userView((await this.client.setDisabled({ userId, disabled, reason })).user!)
  }

  async remove(userId: string) {
    await this.client.deleteUser({ userId })
  }

  async sessions(userId: string) {
    const r = await this.client.listUserSessions({ userId })
    return {
      devices: r.devices.map(d => ({ id: d.id, name: d.name, os: d.os, arch: d.arch, createdAt: d.createdAt, lastSeenAt: d.lastSeenAt })),
      webSessions: r.webSessions
    }
  }

  async revokeSessions(userId: string, deviceId = '') {
    await this.client.revokeUserSessions({ userId, deviceId })
  }

  async providers(userId: string) {
    return (await this.client.listUserProviders({ userId })).providers.map(p => ({
      id: p.id,
      type: p.type,
      name: p.displayName || p.id,
      baseUrl: p.baseUrl,
      keySet: p.apiKeySet,
      enabled: p.enabled,
      modelCount: p.modelCount
    }))
  }

  async removeProvider(userId: string, providerId: string) {
    await this.client.deleteUserProvider({ userId, providerId })
  }

  async getLdap() {
    return ldapView((await this.client.getLdapConfig({})).config)
  }

  async setLdap(s: LdapSettings) {
    return ldapView((await this.client.setLdapConfig({ config: ldapConfig(s) })).config)
  }

  async testLdap(s: LdapSettings) {
    const r = await this.client.testLdapConnection({ config: ldapConfig(s) })
    return { ok: r.ok, message: r.message }
  }

  async testLdapUser(s: LdapSettings, login: string) {
    const r = await this.client.testLdapUser({ config: ldapConfig(s), login })
    return { found: r.found, message: r.message, dn: r.dn, username: r.username, email: r.email, name: r.name, groups: r.groups, allowed: r.allowed, admin: r.admin, userId: r.userId }
  }
}
