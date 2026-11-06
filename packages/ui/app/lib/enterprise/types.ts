// EnterpriseApi: what the settings screens for running a server in an organization need from the server's AuditService,
// BillingService, DirectoryService, MfaService, PolicyService and SsoService. The rules live on the server; these screens show
// what it answers. Two implementations: connect.ts (through the client, or directly in the web console) and mock.ts.

import type { UserRole } from '~/lib/users/types'

export interface Access {
  role: UserRole
  capabilities: string[]
}

export interface RoleMapping {
  group: string
  role: UserRole
}

// --- single sign-on ------------------------------------------------------------------------------------------

export type SsoProtocol = 'oidc' | 'saml'

export interface SsoProviderView {
  id: string
  name: string
  protocol: SsoProtocol
  enabled: boolean
  issuer: string
  clientId: string
  // Write-only: set to change; empty keeps the stored one.
  clientSecret: string
  clientSecretSet: boolean
  clearClientSecret: boolean
  scopes: string[]
  idpEntityId: string
  idpSsoUrl: string
  idpCertificate: string
  emailAttribute: string
  nameAttribute: string
  usernameAttribute: string
  groupsAttribute: string
  autoCreateUsers: boolean
  allowedDomains: string[]
  allowedGroups: string[]
  roleMappings: RoleMapping[]
  // Read only: what to register at the identity provider.
  redirectUri: string
  samlAcsUrl: string
  samlEntityId: string
  samlMetadataUrl: string
}

export function emptySsoProvider(protocol: SsoProtocol = 'oidc'): SsoProviderView {
  return { id: '', name: '', protocol, enabled: false, issuer: '', clientId: '', clientSecret: '', clientSecretSet: false, clearClientSecret: false, scopes: [], idpEntityId: '', idpSsoUrl: '', idpCertificate: '', emailAttribute: '', nameAttribute: '', usernameAttribute: '', groupsAttribute: '', autoCreateUsers: true, allowedDomains: [], allowedGroups: [], roleMappings: [], redirectUri: '', samlAcsUrl: '', samlEntityId: '', samlMetadataUrl: '' }
}

export interface SsoCheck {
  name: string
  ok: boolean
  message: string
}

export interface SsoTestResult {
  ok: boolean
  checks: SsoCheck[]
  signInTestUrl: string
}

export interface SsoSettings {
  requireSso: boolean
  breakGlassLogins: string[]
}

// --- second factor -------------------------------------------------------------------------------------------

export interface MfaStatus {
  enabled: boolean
  enabledAt: string
  recoveryCodesLeft: number
  required: boolean
}

export interface MfaEnrollment {
  secret: string
  uri: string
  qrSvg: string
}

export interface MfaPolicy {
  required: boolean
  roles: UserRole[]
  exemptSso: boolean
}

// --- groups, provisioning ------------------------------------------------------------------------------------

export interface GroupView {
  id: string
  name: string
  // 'local', 'scim', 'sso' or 'ldap'; only local ones are edited here.
  source: string
  memberIds: string[]
}

export interface ScimTokenView {
  id: string
  name: string
  createdAt: string
  lastUsedAt: string
}

export interface ScimState {
  roleMappings: RoleMapping[]
  tokens: ScimTokenView[]
  baseUrl: string
}

// --- quotas --------------------------------------------------------------------------------------------------

// Undefined: no limit at this level. 0 blocks all spending.
export interface QuotaLimits {
  dailyUsd?: number
  monthlyUsd?: number
}

export interface Quotas {
  users: Record<string, QuotaLimits>
  groups: Record<string, QuotaLimits>
  roles: Partial<Record<UserRole, QuotaLimits>>
  sessionUsd?: number
}

export interface UserQuotaStatus {
  own: QuotaLimits
  effective: QuotaLimits
  // 'user', 'group:<name>', 'role' or ''.
  dailySource: string
  monthlySource: string
  dailySpentUsd: number
  monthlySpentUsd: number
}

// --- audit ---------------------------------------------------------------------------------------------------

export interface AuditFilter {
  action: string
  actor: string
  target: string
  outcome: '' | 'success' | 'failure'
  from: string
  to: string
}

export interface AuditEventView {
  id: string
  at: string
  action: string
  outcome: string
  actorId: string
  actor: string
  actorIp: string
  targetType: string
  targetId: string
  target: string
  details: string
}

export interface CsvExport {
  csv: Uint8Array
  rows: number
  truncated: boolean
}

// --- managed policy ------------------------------------------------------------------------------------------

export interface ManagedPolicy {
  allowRules: string[]
  askRules: string[]
  denyRules: string[]
  managedRulesOnly: boolean
  sandboxRequired: boolean
  sandboxOutsideRefused: boolean
  restrictProviders: boolean
  allowedProviders: string[]
  personalProvidersDisabled: boolean
  restrictMcpServers: boolean
  allowedMcpServers: string[]
}

export interface PolicyChoice {
  id: string
  label: string
}

export interface ManagedPolicyState {
  policy: ManagedPolicy
  updatedAt: string
  providers: PolicyChoice[]
  mcpServers: PolicyChoice[]
}

export function emptyManagedPolicy(): ManagedPolicy {
  return { allowRules: [], askRules: [], denyRules: [], managedRulesOnly: false, sandboxRequired: false, sandboxOutsideRefused: false, restrictProviders: false, allowedProviders: [], personalProvidersDisabled: false, restrictMcpServers: false, allowedMcpServers: [] }
}

export interface EnterpriseApi {
  access(): Promise<Access>

  listSso(): Promise<SsoProviderView[]>
  saveSso(p: SsoProviderView): Promise<SsoProviderView>
  deleteSso(id: string): Promise<void>
  testSso(p: SsoProviderView): Promise<SsoTestResult>
  getSsoSettings(): Promise<SsoSettings>
  setSsoSettings(s: SsoSettings): Promise<SsoSettings>

  mfaStatus(): Promise<MfaStatus>
  startMfa(): Promise<MfaEnrollment>
  confirmMfa(code: string): Promise<string[]>
  disableMfa(code: string): Promise<void>
  regenerateRecoveryCodes(code: string): Promise<string[]>
  getMfaPolicy(): Promise<MfaPolicy>
  setMfaPolicy(p: MfaPolicy): Promise<MfaPolicy>
  resetUserMfa(userId: string): Promise<void>

  listGroups(): Promise<GroupView[]>
  createGroup(name: string): Promise<GroupView>
  renameGroup(id: string, name: string): Promise<GroupView>
  deleteGroup(id: string): Promise<void>
  setGroupMembers(id: string, userIds: string[]): Promise<GroupView>

  getScim(): Promise<ScimState>
  setScimMappings(m: RoleMapping[]): Promise<RoleMapping[]>
  createScimToken(name: string): Promise<{ token: ScimTokenView, value: string }>
  revokeScimToken(id: string): Promise<void>

  getQuotas(): Promise<Quotas>
  setUserQuota(userId: string, l: QuotaLimits): Promise<void>
  setGroupQuota(groupId: string, l: QuotaLimits): Promise<void>
  setRoleQuota(role: UserRole, l: QuotaLimits): Promise<void>
  setSessionBudget(usd: number | undefined): Promise<void>
  getUserQuota(userId: string): Promise<UserQuotaStatus>
  exportUsage(o?: { from?: string, to?: string, userId?: string, groupId?: string }): Promise<CsvExport>

  listAudit(f: AuditFilter, pageToken?: string): Promise<{ events: AuditEventView[], nextPageToken: string }>
  exportAudit(f: AuditFilter): Promise<CsvExport>

  getManagedPolicy(): Promise<ManagedPolicyState>
  setManagedPolicy(p: ManagedPolicy): Promise<ManagedPolicyState>
}

export const ROLE_LABEL: Record<UserRole, string> = { admin: 'Admin', user: 'User', auditor: 'Auditor', billing: 'Billing' }
export const ROLE_HINT: Record<UserRole, string> = {
  admin: 'Everything.',
  user: 'No administration.',
  auditor: 'Reads users, the audit log, usage, quotas and policy; changes nothing.',
  billing: 'Reads usage and sets quotas.'
}

/** Text from a limit field: empty means no limit, and the server refuses anything negative. */
export function parseLimit(text: string): number | undefined {
  const t = text.trim()
  if (!t) return undefined
  const n = Number(t)
  if (!Number.isFinite(n)) throw new Error(`Not an amount: ${text}`)
  return n
}

export function limitText(v: number | undefined): string {
  return v === undefined ? '' : String(v)
}

/** One entry per line, without blanks or repeats. */
export function lines(text: string): string[] {
  return [...new Set(text.split('\n').map(l => l.trim()).filter(Boolean))]
}

/** Saves bytes as a file in the browser. */
export function downloadCsv(name: string, csv: Uint8Array) {
  const url = URL.createObjectURL(new Blob([csv as BlobPart], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
