// UsersApi: what the admin Users section needs from harness.auth.v1.AdminService (accounts, roles, sessions,
// a user's personal providers, LDAP sign-in settings). The client forwards these calls to the server with the
// user's credentials; the server refuses everyone but admins. Two implementations: connect.ts (the real
// service through the client) and mock.ts (in memory, for the mock transport).

import type { PermissionModeName } from '~/lib/session/types'

export type UserRole = 'admin' | 'user' | 'auditor' | 'billing'

export interface UserView {
  id: string
  name: string
  username: string
  email: string
  role: UserRole
  source: 'local' | 'ldap' | 'sso' | 'scim'
  mfaEnabled: boolean
  groups: string[]
  disabled: boolean
  disabledReason: string
  // RFC 3339; '' when unknown or never.
  createdAt: string
  lastSignInAt: string
  lastSeenAt: string
  isSelf: boolean
}

export interface NewUser {
  name: string
  username: string
  email: string
  // Ignored when generatePassword or ldap is set.
  password: string
  generatePassword: boolean
  role: UserRole
  // A directory user: no local password, signs in through LDAP with `username`.
  ldap: boolean
}

export interface UserDevice {
  id: string
  name: string
  os: string
  arch: string
  createdAt: string
  lastSeenAt: string
}

export interface UserSessions {
  devices: UserDevice[]
  webSessions: number
}

export interface UserProviderView {
  id: string
  type: string
  name: string
  baseUrl: string
  keySet: boolean
  enabled: boolean
  modelCount: number
}

export type LdapLocalLoginName = 'fallback' | 'break_glass'

export interface LdapSettings {
  enabled: boolean
  urls: string[]
  startTls: boolean
  caCert: string
  skipVerify: boolean
  bindDn: string
  // Write-only: a new password; '' keeps the stored one.
  bindPassword: string
  bindPasswordSet: boolean
  clearBindPassword: boolean
  userBaseDn: string
  userFilter: string
  usernameAttribute: string
  emailAttribute: string
  nameAttribute: string
  allowedGroupDn: string
  adminGroupDn: string
  roleMappings: { group: string, role: UserRole }[]
  autoCreateUsers: boolean
  localLogin: LdapLocalLoginName
  breakGlassLogins: string[]
  timeoutMs: number
}

export interface LdapTestResult {
  ok: boolean
  message: string
}

export interface LdapUserLookup {
  found: boolean
  message: string
  dn: string
  username: string
  email: string
  name: string
  groups: string[]
  allowed: boolean
  admin: boolean
  // The role the admin group and the rules give them.
  role: UserRole
  userId: string
}

export interface UsersApi {
  // Whether the caller may administer users (the server answers PERMISSION_DENIED otherwise).
  isAdmin(): Promise<boolean>
  list(query?: string): Promise<UserView[]>
  // Returns the generated password ('' when one was given or for directory users); shown once.
  create(u: NewUser): Promise<{ user: UserView, generatedPassword: string }>
  resetPassword(userId: string, opts: { password?: string, generate?: boolean, revokeSessions?: boolean }): Promise<string>
  setRole(userId: string, role: UserRole): Promise<UserView>
  setDisabled(userId: string, disabled: boolean, reason?: string): Promise<UserView>
  remove(userId: string): Promise<void>
  sessions(userId: string): Promise<UserSessions>
  // deviceId omitted: every device and web session.
  revokeSessions(userId: string, deviceId?: string): Promise<void>
  providers(userId: string): Promise<UserProviderView[]>
  removeProvider(userId: string, providerId: string): Promise<void>
  getLdap(): Promise<LdapSettings>
  setLdap(s: LdapSettings): Promise<LdapSettings>
  testLdap(s: LdapSettings): Promise<LdapTestResult>
  testLdapUser(s: LdapSettings, login: string): Promise<LdapUserLookup>
  // The permission modes nobody on the server may use ('default' is never among them). Every client enforces it.
  getModePolicy(): Promise<PermissionModeName[]>
  setModePolicy(disabled: PermissionModeName[]): Promise<PermissionModeName[]>
}

export const DEFAULT_LDAP_FILTER = '(|(uid={{username}})(mail={{username}}))'

export function emptyLdapSettings(): LdapSettings {
  return {
    enabled: false,
    urls: [],
    startTls: false,
    caCert: '',
    skipVerify: false,
    bindDn: '',
    bindPassword: '',
    bindPasswordSet: false,
    clearBindPassword: false,
    userBaseDn: '',
    userFilter: DEFAULT_LDAP_FILTER,
    usernameAttribute: 'uid',
    emailAttribute: 'mail',
    nameAttribute: 'cn',
    allowedGroupDn: '',
    adminGroupDn: '', roleMappings: [],
    autoCreateUsers: true,
    localLogin: 'fallback',
    breakGlassLogins: [],
    timeoutMs: 5000
  }
}
