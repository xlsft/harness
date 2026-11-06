import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AdminUserSchema, GroupRoleMappingSchema } from '~/gen/harness/auth/v1/admin_pb'
import { AuditEventSchema } from '~/gen/harness/auth/v1/audit_pb'
import { User_Role, UserSource } from '~/gen/harness/auth/v1/auth_pb'
import { QuotaLimitsSchema } from '~/gen/harness/auth/v1/billing_pb'
import { ManagedPolicySchema } from '~/gen/harness/auth/v1/managed_policy_pb'
import { SsoProtocol, SsoProviderSchema } from '~/gen/harness/auth/v1/sso_pb'
import { auditEventView, limitsView, mappingsPb, mappingsView, policyView, ssoPb, ssoView } from '~/lib/enterprise/connect'
import { MockEnterpriseApi } from '~/lib/enterprise/mock'
import { emptySsoProvider, lines, limitText, parseLimit } from '~/lib/enterprise/types'
import { roleName, rolePb, userView } from '~/lib/users/connect'
import { resolveClientEndpoint, AGENT_URL_STORAGE_KEY } from '~/lib/web/endpoint'
import { AGENT_TOKEN_STORAGE_KEY } from '~/lib/transport/token'

describe('roles and sources', () => {
  it('maps every role both ways', () => {
    for (const r of ['admin', 'user', 'auditor', 'billing'] as const) expect(roleName(rolePb(r))).toBe(r)
    expect(roleName(User_Role.UNSPECIFIED)).toBe('user')
  })

  it('shows sign-in source, factor and groups of a user', () => {
    const v = userView(create(AdminUserSchema, { id: 'u', role: User_Role.AUDITOR, source: UserSource.SSO, mfaEnabled: true, groups: ['eng'] }))
    expect(v).toMatchObject({ role: 'auditor', source: 'sso', mfaEnabled: true, groups: ['eng'] })
    expect(userView(create(AdminUserSchema, { source: UserSource.SCIM })).source).toBe('scim')
  })

  it('sends group rules without blanks, in order', () => {
    const pb = mappingsPb([{ group: ' a ', role: 'billing' }, { group: ' ', role: 'admin' }, { group: 'b', role: 'auditor' }])
    expect(pb).toEqual([{ group: 'a', role: User_Role.BILLING }, { group: 'b', role: User_Role.AUDITOR }])
    expect(mappingsView([create(GroupRoleMappingSchema, { group: 'x', role: User_Role.ADMIN })])).toEqual([{ group: 'x', role: 'admin' }])
  })
})

describe('identity providers', () => {
  it('never echoes the client secret and sends read-only fields nowhere', () => {
    const v = ssoView(create(SsoProviderSchema, { id: 'corp', name: 'Corp', protocol: SsoProtocol.SAML, clientSecretSet: true, redirectUri: 'https://x/cb', roleMappings: [create(GroupRoleMappingSchema, { group: 'g', role: User_Role.BILLING })] }))
    expect(v).toMatchObject({ protocol: 'saml', clientSecret: '', clientSecretSet: true, redirectUri: 'https://x/cb', roleMappings: [{ group: 'g', role: 'billing' }] })
    const pb = ssoPb({ ...v, clientSecret: 'new' })
    expect(pb).toMatchObject({ protocol: SsoProtocol.SAML, clientSecret: 'new' })
    expect(pb).not.toHaveProperty('redirectUri')
    expect(pb).not.toHaveProperty('clientSecretSet')
  })
})

describe('quota fields', () => {
  it('reads empty as no limit, zero as zero, and refuses words', () => {
    expect(parseLimit('')).toBeUndefined()
    expect(parseLimit('  ')).toBeUndefined()
    expect(parseLimit('0')).toBe(0)
    expect(parseLimit('12.5')).toBe(12.5)
    expect(() => parseLimit('lots')).toThrow(/Not an amount/)
    expect(limitText(undefined)).toBe('')
    expect(limitText(0)).toBe('0')
  })

  it('keeps unset apart from zero when reading', () => {
    expect(limitsView(create(QuotaLimitsSchema, { dailyUsd: 0 }))).toEqual({ dailyUsd: 0 })
    expect(limitsView(undefined)).toEqual({})
  })

  it('splits lists into clean lines', () => {
    expect(lines(' a\n\nb \na\n')).toEqual(['a', 'b'])
  })
})

describe('MockEnterpriseApi', () => {
  it('applies user over strictest group', async () => {
    const api = new MockEnterpriseApi()
    await api.setGroupQuota('grp_eng', { dailyUsd: 5 })
    expect(await api.getUserQuota('usr_ada')).toMatchObject({ effective: { dailyUsd: 5 }, dailySource: 'group:Engineering' })
    await api.setUserQuota('usr_ada', { dailyUsd: 9 })
    expect(await api.getUserQuota('usr_ada')).toMatchObject({ effective: { dailyUsd: 9 }, dailySource: 'user' })
    await expect(api.setUserQuota('usr_ada', { dailyUsd: -1 })).rejects.toThrow()
  })

  it('refuses to require what would lock the caller out, and SSO without a provider', async () => {
    const api = new MockEnterpriseApi()
    await expect(api.setMfaPolicy({ required: true, roles: [], exemptSso: false })).rejects.toThrow(/your own account/)
    await api.startMfa()
    await api.confirmMfa('123456')
    await api.setMfaPolicy({ required: true, roles: [], exemptSso: false })
    await expect(api.setSsoSettings({ requireSso: true, breakGlassLogins: ['a'] })).rejects.toThrow(/identity provider/)
    await api.saveSso({ ...emptySsoProvider(), name: 'Corp', enabled: true, issuer: 'https://i' })
    await expect(api.setSsoSettings({ requireSso: true, breakGlassLogins: [] })).rejects.toThrow(/break-glass/)
  })

  it('does not edit groups a directory feeds', async () => {
    const api = new MockEnterpriseApi()
    await expect(api.deleteGroup('grp_fin')).rejects.toThrow(/SCIM/)
    const g = await api.createGroup('New')
    await api.setGroupMembers(g.id, ['usr_ada'])
    expect((await api.listGroups()).find(x => x.id === g.id)?.memberIds).toEqual(['usr_ada'])
  })

  it('shows a provisioning token once, and filters the audit log', async () => {
    const api = new MockEnterpriseApi()
    const { value, token } = await api.createScimToken('okta')
    expect(value).toMatch(/^hsc_/)
    expect(JSON.stringify(await api.getScim())).not.toContain(value)
    await api.revokeScimToken(token.id)
    const base = { action: '', actor: '', target: '', outcome: '' as const, from: '', to: '' }
    const all = await api.listAudit(base)
    expect(all.events.length).toBeGreaterThan(1)
    expect((await api.listAudit({ ...base, outcome: 'failure' })).events.every(e => e.outcome === 'failure')).toBe(true)
    expect((await api.listAudit({ ...base, action: 'auth' })).events.every(e => e.action.startsWith('auth.'))).toBe(true)
  })
})

describe('views', () => {
  it('reads an audit event and a policy', () => {
    expect(auditEventView(create(AuditEventSchema, { id: '1', action: 'user.create', detailsJson: '{"a":1}' }))).toMatchObject({ id: '1', action: 'user.create', details: '{"a":1}' })
    expect(policyView(create(ManagedPolicySchema, { denyRules: ['Bash'], sandboxRequired: true }))).toMatchObject({ denyRules: ['Bash'], sandboxRequired: true, restrictProviders: false })
    expect(policyView(undefined).allowRules).toEqual([])
  })
})

describe('the web UI choosing a client', () => {
  function env(href: string, seed: Record<string, string> = {}) {
    const data = new Map(Object.entries(seed))
    const urls: string[] = []
    return {
      urls,
      data,
      href,
      storage: { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v), removeItem: (k: string) => void data.delete(k) },
      replaceUrl: (u: string) => void urls.push(u)
    }
  }

  it('has none until one is given', () => {
    expect(resolveClientEndpoint(env('https://h.example/'))).toBeNull()
  })

  it('takes one from a link once, and removes it from the address bar', () => {
    const e = env('https://h.example/?agentUrl=http://127.0.0.1:7801/&agentToken=tok&x=1')
    expect(resolveClientEndpoint(e)).toEqual({ url: 'http://127.0.0.1:7801', token: 'tok' })
    expect(e.urls).toEqual(['https://h.example/?x=1'])
    expect(e.data.get(AGENT_URL_STORAGE_KEY)).toBe('http://127.0.0.1:7801')
  })

  it('remembers per tab, and ignores what a browser cannot call', () => {
    expect(resolveClientEndpoint(env('https://h.example/', { [AGENT_URL_STORAGE_KEY]: 'http://c:1', [AGENT_TOKEN_STORAGE_KEY]: 't' }))).toEqual({ url: 'http://c:1', token: 't' })
    expect(resolveClientEndpoint(env('https://h.example/?agentUrl=javascript:alert(1)'))).toBeNull()
    expect(resolveClientEndpoint(env('https://h.example/', { [AGENT_URL_STORAGE_KEY]: 'not a url' }))).toBeNull()
  })
})
