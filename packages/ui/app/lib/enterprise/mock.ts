// In-memory EnterpriseApi for the mock transport: enough of the server's rules that the screens can be exercised without one.

import type { UserRole } from '~/lib/users/types'
import type { Access, AuditEventView, AuditFilter, EnterpriseApi, GroupView, ManagedPolicy, ManagedPolicyState, MfaPolicy, QuotaLimits, Quotas, RoleMapping, ScimState, SsoProviderView, SsoSettings, UserQuotaStatus } from './types'
import { emptyManagedPolicy } from './types'

const now = () => new Date().toISOString()
const encoder = new TextEncoder()

export class MockEnterpriseApi implements EnterpriseApi {
  private seq = 0
  private providers: SsoProviderView[] = []
  private ssoSettings: SsoSettings = { requireSso: false, breakGlassLogins: [] }
  private mfa = { enabled: false, enabledAt: '', recoveryCodesLeft: 0, required: false, secret: '' }
  private mfaPolicyValue: MfaPolicy = { required: false, roles: [], exemptSso: false }
  private groups: GroupView[] = [{ id: 'grp_eng', name: 'Engineering', source: 'local', memberIds: ['usr_ada'] }, { id: 'grp_fin', name: 'Finance', source: 'scim', memberIds: [] }]
  private scim: ScimState = { roleMappings: [], tokens: [], baseUrl: 'https://harness.example.com/scim/v2' }
  private quotas: Quotas = { users: {}, groups: {}, roles: {} }
  private events: AuditEventView[] = []
  private policy: ManagedPolicy = emptyManagedPolicy()
  private policyAt = ''
  // What the mock says the caller may do; a test can narrow it.
  capabilities = ['users.read', 'users.write', 'audit.read', 'usage.read', 'quotas.read', 'quotas.write', 'policy.read', 'policy.write', 'sign_in.read', 'sign_in.write', 'directory.read', 'directory.write', 'limits.read', 'limits.write']

  private log(action: string, target = '', outcome = 'success') {
    this.events.unshift({ id: String(++this.seq), at: now(), action, outcome, actorId: 'usr_me', actor: 'you@example.com', actorIp: '127.0.0.1', targetType: '', targetId: target, target, details: '{}' })
  }

  async access(): Promise<Access> {
    return { role: 'admin', capabilities: this.capabilities }
  }

  async listSso() {
    return this.providers
  }

  async saveSso(p: SsoProviderView) {
    if (!p.name.trim()) throw new Error('A provider needs a name')
    const id = p.id || `${p.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-${++this.seq}`
    const saved: SsoProviderView = {
      ...p,
      id,
      clientSecret: '',
      clientSecretSet: p.clearClientSecret ? false : !!p.clientSecret || p.clientSecretSet,
      clearClientSecret: false,
      redirectUri: `https://harness.example.com/api/sso/${id}/oidc/callback`,
      samlAcsUrl: `https://harness.example.com/api/sso/${id}/saml/acs`,
      samlEntityId: `https://harness.example.com/api/sso/${id}/saml/metadata`,
      samlMetadataUrl: `https://harness.example.com/api/sso/${id}/saml/metadata`
    }
    this.providers = [...this.providers.filter(x => x.id !== id), saved]
    this.log('sign_in.sso_save', id)
    return saved
  }

  async deleteSso(id: string) {
    this.providers = this.providers.filter(p => p.id !== id)
    this.log('sign_in.sso_delete', id)
  }

  async testSso(p: SsoProviderView) {
    const ok = p.protocol === 'oidc' ? !!p.issuer : !!p.idpSsoUrl && !!p.idpCertificate
    return { ok, checks: [{ name: 'Settings', ok, message: ok ? 'Looks complete (mock)' : 'Something is missing' }], signInTestUrl: '' }
  }

  async getSsoSettings() {
    return this.ssoSettings
  }

  async setSsoSettings(s: SsoSettings) {
    if (s.requireSso && !this.providers.some(p => p.enabled)) throw new Error('Enable an identity provider before requiring SSO')
    if (s.requireSso && !s.breakGlassLogins.length) throw new Error('List at least one break-glass admin login')
    this.ssoSettings = s
    return s
  }

  async mfaStatus() {
    const { secret: _, ...rest } = this.mfa
    return rest
  }

  async startMfa() {
    if (this.mfa.enabled) throw new Error('Two-factor sign-in is already on')
    this.mfa.secret = 'JBSWY3DPEHPK3PXP'
    return { secret: this.mfa.secret, uri: `otpauth://totp/mock?secret=${this.mfa.secret}`, qrSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>' }
  }

  async confirmMfa(code: string) {
    if (!/^\d{6}$/.test(code)) throw new Error('That code is not right')
    this.mfa = { ...this.mfa, enabled: true, enabledAt: now(), recoveryCodesLeft: 10 }
    return Array.from({ length: 10 }, (_, i) => `mock${i}aaaa-bbbbcccc`)
  }

  async disableMfa(code: string) {
    if (!code.trim()) throw new Error('That code is not right')
    if (this.mfa.required) throw new Error('Your organization requires two-factor sign-in')
    this.mfa = { enabled: false, enabledAt: '', recoveryCodesLeft: 0, required: false, secret: '' }
  }

  async regenerateRecoveryCodes(code: string) {
    return this.confirmMfa(code)
  }

  async getMfaPolicy() {
    return this.mfaPolicyValue
  }

  async setMfaPolicy(p: MfaPolicy) {
    if (p.required && (p.roles.length === 0 || p.roles.includes('admin')) && !this.mfa.enabled) throw new Error('Turn on two-factor sign-in for your own account first (Settings, Account)')
    this.mfaPolicyValue = p
    return p
  }

  async resetUserMfa(userId: string) {
    this.log('mfa.reset', userId)
  }

  async listGroups() {
    return this.groups
  }

  private group(id: string) {
    const g = this.groups.find(x => x.id === id)
    if (!g) throw new Error('Group not found')
    if (g.source !== 'local') throw new Error(`This group comes from ${g.source.toUpperCase()}; change it there`)
    return g
  }

  async createGroup(name: string) {
    if (!name.trim()) throw new Error('A group needs a name')
    if (this.groups.some(g => g.source === 'local' && g.name === name.trim())) throw new Error(`A group named ${name} already exists`)
    const g: GroupView = { id: `grp_${++this.seq}`, name: name.trim(), source: 'local', memberIds: [] }
    this.groups = [...this.groups, g]
    return g
  }

  async renameGroup(id: string, name: string) {
    const g = this.group(id)
    g.name = name.trim()
    return g
  }

  async deleteGroup(id: string) {
    this.group(id)
    this.groups = this.groups.filter(g => g.id !== id)
    this.quotas.groups = Object.fromEntries(Object.entries(this.quotas.groups).filter(([k]) => k !== id))
  }

  async setGroupMembers(id: string, userIds: string[]) {
    const g = this.group(id)
    g.memberIds = userIds
    return g
  }

  async getScim() {
    return this.scim
  }

  async setScimMappings(m: RoleMapping[]) {
    this.scim.roleMappings = m
    return m
  }

  async createScimToken(name: string) {
    if (!name.trim()) throw new Error('A token needs a name')
    const token = { id: `sct_${++this.seq}`, name: name.trim(), createdAt: now(), lastUsedAt: '' }
    this.scim.tokens = [...this.scim.tokens, token]
    return { token, value: 'hsc_mock_value_shown_once' }
  }

  async revokeScimToken(id: string) {
    this.scim.tokens = this.scim.tokens.filter(t => t.id !== id)
  }

  async getQuotas() {
    return this.quotas
  }

  private static checked(l: QuotaLimits) {
    for (const v of [l.dailyUsd, l.monthlyUsd]) {
      if (v !== undefined && v < 0) throw new Error('Limits are not negative')
    }
    return l
  }

  async setUserQuota(userId: string, l: QuotaLimits) {
    MockEnterpriseApi.checked(l)
    const { [userId]: _, ...others } = this.quotas.users
    this.quotas.users = l.dailyUsd === undefined && l.monthlyUsd === undefined ? others : { ...others, [userId]: l }
    this.log('quota.set_user', userId)
  }

  async setGroupQuota(groupId: string, l: QuotaLimits) {
    MockEnterpriseApi.checked(l)
    const { [groupId]: _, ...others } = this.quotas.groups
    this.quotas.groups = l.dailyUsd === undefined && l.monthlyUsd === undefined ? others : { ...others, [groupId]: l }
    this.log('quota.set_group', groupId)
  }

  async setRoleQuota(role: UserRole, l: QuotaLimits) {
    MockEnterpriseApi.checked(l)
    this.quotas.roles[role] = l
    this.log('quota.set_role', role)
  }

  async setSessionBudget(usd: number | undefined) {
    this.quotas.sessionUsd = usd
    this.log('quota.set_session_budget')
  }

  async getUserQuota(userId: string): Promise<UserQuotaStatus> {
    const own = this.quotas.users[userId] ?? {}
    const groups = this.groups.filter(g => g.memberIds.includes(userId) && this.quotas.groups[g.id])
    const strictest = (field: 'dailyUsd' | 'monthlyUsd') => {
      const hit = groups.map(g => ({ g, v: this.quotas.groups[g.id]![field] })).filter(x => x.v !== undefined).sort((a, b) => a.v! - b.v!)[0]
      return hit ? { value: hit.v, source: `group:${hit.g.name}` } : undefined
    }
    const pick = (field: 'dailyUsd' | 'monthlyUsd') => {
      if (own[field] !== undefined) return { value: own[field], source: 'user' }
      return strictest(field) ?? { value: undefined, source: '' }
    }
    const d = pick('dailyUsd')
    const m = pick('monthlyUsd')
    return { own, effective: { dailyUsd: d.value, monthlyUsd: m.value }, dailySource: d.source, monthlySource: m.source, dailySpentUsd: 1.25, monthlySpentUsd: 12.5 }
  }

  async exportUsage() {
    const csv = 'time,user_id,email,name,session_id,provider,model,upstream_model,request_class,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_usd,status,duration_ms\r\n'
    return { csv: encoder.encode(csv), rows: 0, truncated: false }
  }

  private matches(e: AuditEventView, f: AuditFilter) {
    const action = f.action.trim()
    if (action && !(action.includes('.') ? e.action === action : e.action.startsWith(`${action}.`))) return false
    if (f.actor && !`${e.actorId} ${e.actor}`.toLowerCase().includes(f.actor.toLowerCase())) return false
    if (f.target && !`${e.targetId} ${e.target}`.toLowerCase().includes(f.target.toLowerCase())) return false
    if (f.outcome && e.outcome !== f.outcome) return false
    if (f.from && e.at < new Date(f.from).toISOString()) return false
    if (f.to && e.at >= new Date(f.to).toISOString()) return false
    return true
  }

  async listAudit(f: AuditFilter) {
    if (!this.events.length) {
      this.log('auth.sign_in', 'you@example.com')
      this.log('user.set_role', 'usr_ada')
      this.log('auth.sign_in_failed', 'mallory@example.com', 'failure')
    }
    return { events: this.events.filter(e => this.matches(e, f)), nextPageToken: '' }
  }

  async exportAudit(f: AuditFilter) {
    const rows = this.events.filter(e => this.matches(e, f))
    const csv = ['time,action,outcome,actor', ...rows.map(e => `${e.at},${e.action},${e.outcome},${e.actor}`)].join('\r\n')
    return { csv: encoder.encode(csv), rows: rows.length, truncated: false }
  }

  private policyState(): ManagedPolicyState {
    return { policy: this.policy, updatedAt: this.policyAt, providers: [{ id: 'anthropic', label: 'Anthropic' }, { id: 'openai', label: 'OpenAI' }], mcpServers: [{ id: 'github', label: 'github' }] }
  }

  async getManagedPolicy() {
    return this.policyState()
  }

  async setManagedPolicy(p: ManagedPolicy) {
    this.policy = p
    this.policyAt = now()
    this.log('policy.managed_update')
    return this.policyState()
  }
}
