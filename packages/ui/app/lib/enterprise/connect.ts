// EnterpriseApi over the server's services, on the same connect transport as everything else: through the client, which
// forwards them with the user's credentials (packages/client/server/coreproxy.go), or directly in the web console.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AuditService, type AuditEvent } from '~/gen/harness/auth/v1/audit_pb'
import type { User_Role } from '~/gen/harness/auth/v1/auth_pb'
import { BillingService, type QuotaLimits as QuotaLimitsPb, type UserQuotaStatus as UserQuotaStatusPb } from '~/gen/harness/auth/v1/billing_pb'
import { DirectoryService, type Group, type ScimToken } from '~/gen/harness/auth/v1/directory_pb'
import { PolicyService, type ManagedPolicy as ManagedPolicyPb } from '~/gen/harness/auth/v1/managed_policy_pb'
import { MfaService } from '~/gen/harness/auth/v1/mfa_pb'
import { SsoProtocol, SsoService, type SsoProvider } from '~/gen/harness/auth/v1/sso_pb'
import type { GroupRoleMapping } from '~/gen/harness/auth/v1/admin_pb'
import { roleName, rolePb } from '~/lib/users/connect'
import type { UserRole } from '~/lib/users/types'
import type { AuditFilter, EnterpriseApi, GroupView, ManagedPolicy, ManagedPolicyState, MfaPolicy, QuotaLimits, RoleMapping, ScimTokenView, SsoProviderView, SsoSettings, UserQuotaStatus } from './types'

export const mappingsView = (m: GroupRoleMapping[] | undefined): RoleMapping[] => (m ?? []).map(x => ({ group: x.group, role: roleName(x.role) }))
export const mappingsPb = (m: RoleMapping[]) => m.filter(x => x.group.trim()).map(x => ({ group: x.group.trim(), role: rolePb(x.role) }))

export function ssoView(p: SsoProvider): SsoProviderView {
  return {
    id: p.id,
    name: p.name,
    protocol: p.protocol === SsoProtocol.SAML ? 'saml' : 'oidc',
    enabled: p.enabled,
    issuer: p.issuer,
    clientId: p.clientId,
    clientSecret: '',
    clientSecretSet: p.clientSecretSet,
    clearClientSecret: false,
    scopes: p.scopes,
    idpEntityId: p.idpEntityId,
    idpSsoUrl: p.idpSsoUrl,
    idpCertificate: p.idpCertificate,
    emailAttribute: p.emailAttribute,
    nameAttribute: p.nameAttribute,
    usernameAttribute: p.usernameAttribute,
    groupsAttribute: p.groupsAttribute,
    autoCreateUsers: p.autoCreateUsers,
    allowedDomains: p.allowedDomains,
    allowedGroups: p.allowedGroups,
    roleMappings: mappingsView(p.roleMappings),
    redirectUri: p.redirectUri,
    samlAcsUrl: p.samlAcsUrl,
    samlEntityId: p.samlEntityId,
    samlMetadataUrl: p.samlMetadataUrl
  }
}

export function ssoPb(p: SsoProviderView) {
  const { redirectUri: _a, samlAcsUrl: _b, samlEntityId: _c, samlMetadataUrl: _d, clientSecretSet: _e, roleMappings, protocol, ...rest } = p
  return { ...rest, protocol: protocol === 'saml' ? SsoProtocol.SAML : SsoProtocol.OIDC, roleMappings: mappingsPb(roleMappings) }
}

const groupView = (g: Group): GroupView => ({ id: g.id, name: g.name, source: g.source, memberIds: g.memberIds })
const tokenView = (t: ScimToken): ScimTokenView => ({ id: t.id, name: t.name, createdAt: t.createdAt, lastUsedAt: t.lastUsedAt })

export const limitsView = (l: QuotaLimitsPb | undefined): QuotaLimits => {
  const out: QuotaLimits = {}
  if (l?.dailyUsd !== undefined) out.dailyUsd = l.dailyUsd
  if (l?.monthlyUsd !== undefined) out.monthlyUsd = l.monthlyUsd
  return out
}

const quotaStatusView = (s: UserQuotaStatusPb | undefined): UserQuotaStatus => ({
  own: limitsView(s?.own),
  effective: limitsView(s?.effective),
  dailySource: s?.dailySource ?? '',
  monthlySource: s?.monthlySource ?? '',
  dailySpentUsd: s?.dailySpentUsd ?? 0,
  monthlySpentUsd: s?.monthlySpentUsd ?? 0
})

export const auditEventView = (e: AuditEvent) => ({ id: e.id, at: e.at, action: e.action, outcome: e.outcome, actorId: e.actorId, actor: e.actor, actorIp: e.actorIp, targetType: e.targetType, targetId: e.targetId, target: e.target, details: e.detailsJson })

const auditFilterPb = (f: AuditFilter) => ({ ...f, from: f.from ? new Date(f.from).toISOString() : '', to: f.to ? new Date(f.to).toISOString() : '' })

export function policyView(p: ManagedPolicyPb | undefined): ManagedPolicy {
  return {
    allowRules: p?.allowRules ?? [],
    askRules: p?.askRules ?? [],
    denyRules: p?.denyRules ?? [],
    managedRulesOnly: p?.managedRulesOnly ?? false,
    sandboxRequired: p?.sandboxRequired ?? false,
    sandboxOutsideRefused: p?.sandboxOutsideRefused ?? false,
    restrictProviders: p?.restrictProviders ?? false,
    allowedProviders: p?.allowedProviders ?? [],
    personalProvidersDisabled: p?.personalProvidersDisabled ?? false,
    restrictMcpServers: p?.restrictMcpServers ?? false,
    allowedMcpServers: p?.allowedMcpServers ?? [],
    restrictPluginSources: p?.restrictPluginSources ?? false,
    allowedPluginSources: p?.allowedPluginSources ?? [],
    blockedPluginSources: p?.blockedPluginSources ?? [],
    distributedMarketplaces: p?.distributedMarketplaces ?? [],
    distributedPlugins: p?.distributedPlugins ?? [],
    blockedPlugins: p?.blockedPlugins ?? [],
    restrictSkills: p?.restrictSkills ?? false,
    allowedSkills: p?.allowedSkills ?? [],
    blockedSkills: p?.blockedSkills ?? [],
    externalSkillSearchDisabled: p?.externalSkillSearchDisabled ?? false
  }
}

export class ConnectEnterpriseApi implements EnterpriseApi {
  private audit: Client<typeof AuditService>
  private billing: Client<typeof BillingService>
  private directory: Client<typeof DirectoryService>
  private mfa: Client<typeof MfaService>
  private policy: Client<typeof PolicyService>
  private sso: Client<typeof SsoService>

  constructor(transport: Transport) {
    this.audit = createClient(AuditService, transport)
    this.billing = createClient(BillingService, transport)
    this.directory = createClient(DirectoryService, transport)
    this.mfa = createClient(MfaService, transport)
    this.policy = createClient(PolicyService, transport)
    this.sso = createClient(SsoService, transport)
  }

  async access() {
    const r = await this.directory.getMyAccess({})
    return { role: roleName(r.role), capabilities: r.capabilities }
  }

  async listSso() {
    return (await this.sso.listSsoProviders({})).providers.map(ssoView)
  }

  async saveSso(p: SsoProviderView) {
    return ssoView((await this.sso.saveSsoProvider({ provider: ssoPb(p) })).provider!)
  }

  async deleteSso(id: string) {
    await this.sso.deleteSsoProvider({ id })
  }

  async testSso(p: SsoProviderView) {
    const r = await this.sso.testSsoProvider({ provider: ssoPb(p) })
    return { ok: r.ok, checks: r.checks.map(c => ({ name: c.name, ok: c.ok, message: c.message })), signInTestUrl: r.signInTestUrl }
  }

  async getSsoSettings() {
    const s = (await this.sso.getSsoSettings({})).settings
    return { requireSso: s?.requireSso ?? false, breakGlassLogins: s?.breakGlassLogins ?? [] }
  }

  async setSsoSettings(s: SsoSettings) {
    const r = (await this.sso.setSsoSettings({ settings: s })).settings
    return { requireSso: r?.requireSso ?? false, breakGlassLogins: r?.breakGlassLogins ?? [] }
  }

  async mfaStatus() {
    const r = await this.mfa.getMfaStatus({})
    return { enabled: r.enabled, enabledAt: r.enabledAt, recoveryCodesLeft: r.recoveryCodesLeft, required: r.required }
  }

  async startMfa() {
    const r = await this.mfa.startMfaEnrollment({})
    return { secret: r.secret, uri: r.uri, qrSvg: r.qrSvg }
  }

  async confirmMfa(code: string) {
    return (await this.mfa.confirmMfaEnrollment({ code })).recoveryCodes
  }

  async disableMfa(code: string) {
    await this.mfa.disableMfa({ code })
  }

  async regenerateRecoveryCodes(code: string) {
    return (await this.mfa.regenerateRecoveryCodes({ code })).recoveryCodes
  }

  async getMfaPolicy() {
    const p = (await this.mfa.getMfaPolicy({})).policy
    return { required: p?.required ?? false, roles: (p?.roles ?? []).map(roleName), exemptSso: p?.exemptSso ?? false }
  }

  async setMfaPolicy(p: MfaPolicy) {
    const r = (await this.mfa.setMfaPolicy({ policy: { required: p.required, roles: p.roles.map(rolePb), exemptSso: p.exemptSso } })).policy
    return { required: r?.required ?? false, roles: (r?.roles ?? []).map(roleName), exemptSso: r?.exemptSso ?? false }
  }

  async resetUserMfa(userId: string) {
    await this.mfa.resetUserMfa({ userId })
  }

  async listGroups() {
    return (await this.directory.listGroups({})).groups.map(groupView)
  }

  async createGroup(name: string) {
    return groupView((await this.directory.createGroup({ name })).group!)
  }

  async renameGroup(id: string, name: string) {
    return groupView((await this.directory.renameGroup({ id, name })).group!)
  }

  async deleteGroup(id: string) {
    await this.directory.deleteGroup({ id })
  }

  async setGroupMembers(id: string, userIds: string[]) {
    return groupView((await this.directory.setGroupMembers({ id, userIds })).group!)
  }

  async getScim() {
    const r = await this.directory.getScimSettings({})
    return { roleMappings: mappingsView(r.settings?.roleMappings), tokens: r.tokens.map(tokenView), baseUrl: r.baseUrl }
  }

  async setScimMappings(m: RoleMapping[]) {
    return mappingsView((await this.directory.setScimSettings({ settings: { roleMappings: mappingsPb(m) } })).settings?.roleMappings)
  }

  async createScimToken(name: string) {
    const r = await this.directory.createScimToken({ name })
    return { token: tokenView(r.token!), value: r.value }
  }

  async revokeScimToken(id: string) {
    await this.directory.revokeScimToken({ id })
  }

  async getQuotas() {
    const r = await this.billing.getQuotas({})
    const roles: Partial<Record<UserRole, QuotaLimits>> = {}
    for (const x of r.roles) roles[roleName(x.role)] = limitsView(x.limits)
    return {
      users: Object.fromEntries(r.users.map(x => [x.userId, limitsView(x.limits)])),
      groups: Object.fromEntries(r.groups.map(x => [x.groupId, limitsView(x.limits)])),
      roles,
      sessionUsd: r.sessionUsd
    }
  }

  async setUserQuota(userId: string, l: QuotaLimits) {
    await this.billing.setUserQuota({ userId, limits: l })
  }

  async setGroupQuota(groupId: string, l: QuotaLimits) {
    await this.billing.setGroupQuota({ groupId, limits: l })
  }

  async setRoleQuota(role: UserRole, l: QuotaLimits) {
    await this.billing.setRoleQuota({ role: rolePb(role) as User_Role, limits: l })
  }

  async setSessionBudget(usd: number | undefined) {
    await this.billing.setSessionBudget({ sessionUsd: usd })
  }

  async getUserQuota(userId: string) {
    return quotaStatusView((await this.billing.getUserQuota({ userId })).status)
  }

  async exportUsage(o: { from?: string, to?: string, userId?: string, groupId?: string } = {}) {
    const r = await this.billing.exportUsageCsv({ from: o.from ?? '', to: o.to ?? '', userId: o.userId ?? '', groupId: o.groupId ?? '' })
    return { csv: r.csv, rows: r.rows, truncated: r.truncated }
  }

  async listAudit(f: AuditFilter, pageToken = '') {
    const r = await this.audit.listAuditEvents({ filter: auditFilterPb(f), pageToken, limit: 100 })
    return { events: r.events.map(auditEventView), nextPageToken: r.nextPageToken }
  }

  async exportAudit(f: AuditFilter) {
    const r = await this.audit.exportAuditCsv({ filter: auditFilterPb(f) })
    return { csv: r.csv, rows: r.rows, truncated: r.truncated }
  }

  async getManagedPolicy(): Promise<ManagedPolicyState> {
    const r = await this.policy.getManagedPolicy({})
    return { policy: policyView(r.policy), updatedAt: r.updatedAt, providers: r.providers.map(c => ({ id: c.id, label: c.label })), mcpServers: r.mcpServers.map(c => ({ id: c.id, label: c.label })) }
  }

  async setManagedPolicy(p: ManagedPolicy): Promise<ManagedPolicyState> {
    await this.policy.setManagedPolicy({ policy: p })
    return this.getManagedPolicy()
  }
}
