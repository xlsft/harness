import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AdminUserSchema, LdapConfigSchema, LdapLocalLogin } from '~/gen/harness/auth/v1/admin_pb'
import { User_Role, UserSource } from '~/gen/harness/auth/v1/auth_pb'
import { ldapConfig, ldapView, userView } from '~/lib/users/connect'
import { MockUsersApi } from '~/lib/users/mock'
import { emptyLdapSettings } from '~/lib/users/types'

describe('userView', () => {
  it('maps role, source and state', () => {
    const v = userView(create(AdminUserSchema, {
      id: 'usr_1',
      name: 'Ada',
      username: 'ada',
      email: 'ada@example.com',
      role: User_Role.ADMIN,
      source: UserSource.LDAP,
      disabled: true,
      disabledReason: 'gone',
      isSelf: false
    }))
    expect(v).toMatchObject({ id: 'usr_1', role: 'admin', source: 'ldap', disabled: true, disabledReason: 'gone', lastSignInAt: '' })
    expect(userView(create(AdminUserSchema, { id: 'u2' }))).toMatchObject({ role: 'user', source: 'local' })
  })
})

describe('LDAP settings', () => {
  it('round-trips the form and never echoes the bind password', () => {
    const v = ldapView(create(LdapConfigSchema, { enabled: true, urls: ['ldaps://dir'], bindPasswordSet: true, localLogin: LdapLocalLogin.BREAK_GLASS, breakGlassLogins: ['root'] }))
    expect(v).toMatchObject({ enabled: true, urls: ['ldaps://dir'], bindPassword: '', bindPasswordSet: true, localLogin: 'break_glass', breakGlassLogins: ['root'] })
    const c = ldapConfig({ ...v, urls: [' ldaps://a ', ''], breakGlassLogins: [' root ', ''], bindPassword: 'new' })
    expect(c).toMatchObject({ urls: ['ldaps://a'], breakGlassLogins: ['root'], localLogin: LdapLocalLogin.BREAK_GLASS, bindPassword: 'new' })
    expect(ldapView(undefined).autoCreateUsers).toBe(true)
  })
})

describe('MockUsersApi', () => {
  it('keeps the last admin and refuses to disable or delete yourself', async () => {
    const api = new MockUsersApi()
    await expect(api.setRole('usr_me', 'user')).rejects.toThrow(/last admin/)
    await expect(api.setDisabled('usr_me', true)).rejects.toThrow(/yourself/)
    await expect(api.remove('usr_me')).rejects.toThrow(/yourself/)
    await api.setRole('usr_ada', 'admin')
    expect((await api.setRole('usr_me', 'user')).role).toBe('user')
  })

  it('shows a generated password once and never for directory users', async () => {
    const api = new MockUsersApi()
    const r = await api.create({ name: 'Carol', username: '', email: 'carol@example.com', password: '', generatePassword: true, role: 'user', ldap: false })
    expect(r.generatedPassword).not.toBe('')
    expect(JSON.stringify(await api.list())).not.toContain(r.generatedPassword)
    const d = await api.create({ name: 'Dir', username: 'dir', email: 'dir@example.com', password: '', generatePassword: true, role: 'user', ldap: true })
    expect(d).toMatchObject({ generatedPassword: '', user: { source: 'ldap' } })
    await expect(api.resetPassword(d.user.id, { generate: true })).rejects.toThrow(/LDAP/)
  })

  it('refuses everything to non-admins', async () => {
    const api = new MockUsersApi(false)
    expect(await api.isAdmin()).toBe(false)
    await expect(api.list()).rejects.toThrow()
    await expect(api.getLdap()).rejects.toThrow()
  })

  it('keeps the LDAP bind password write-only', async () => {
    const api = new MockUsersApi()
    const saved = await api.setLdap({ ...emptyLdapSettings(), urls: ['ldap://dir'], userBaseDn: 'dc=x', bindPassword: 'secret-value' })
    expect(saved).toMatchObject({ bindPassword: '', bindPasswordSet: true })
    expect(JSON.stringify(await api.getLdap())).not.toContain('secret-value')
  })
})
