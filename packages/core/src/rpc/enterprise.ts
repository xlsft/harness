/**
 * Connect implementations of the services for running the server in an organization: AuditService, BillingService,
 * DirectoryService, MfaService, PolicyService and SsoService (proto/harness/auth/v1). Thin adapters: the rules are in
 * src/auth and src/services, what each role may do in auth/roles.ts.
 */
import { create } from "@bufbuild/protobuf";
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { auditCsv, listAudit, type AuditRow } from "../audit/log.ts";
import { confirmEnrollment, disableMfa, getMfaPolicy, mfaStatus, regenerateRecoveryCodes, resetUserMfa, setMfaPolicy, startEnrollment } from "../auth/mfa.ts";
import { capabilitiesOf, can, need, normalizeRole, type Capability } from "../auth/roles.ts";
import { createScimToken, getScimSettings, listScimTokens, revokeScimToken, scimBaseUrl, setScimSettings } from "../scim/tokens.ts";
import { deleteSsoProvider, getSsoSettings, listSsoProviders, resolveSso, saveSsoProvider, setSsoSettings, ssoUrls, type SsoInput, type SsoProviderView } from "../auth/sso-config.ts";
import { testSsoConfig } from "../auth/sso-test.ts";
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { AuditService } from "../gen/harness/auth/v1/audit_pb.ts";
import { BillingService, type QuotaLimits, QuotaLimitsSchema, UserQuotaStatusSchema } from "../gen/harness/auth/v1/billing_pb.ts";
import { DirectoryService } from "../gen/harness/auth/v1/directory_pb.ts";
import { MfaService } from "../gen/harness/auth/v1/mfa_pb.ts";
import { PolicyService, type ManagedPolicy as ManagedPolicyPb } from "../gen/harness/auth/v1/managed_policy_pb.ts";
import { SsoProtocol, SsoService, type SsoProvider } from "../gen/harness/auth/v1/sso_pb.ts";
import { listAllMcpNames } from "../mcp/registry.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "../services/devices.ts";
import { toPublicUser } from "../services/devices.ts";
import { createGroup, deleteGroup, groupsOfUser, listGroups, renameGroup, setGroupMembers, type GroupView } from "../services/groups.ts";
import { getManagedPolicy, setManagedPolicy, type ManagedPolicy } from "../services/managed-policy.ts";
import { listProviders } from "../services/providers.ts";
import { effectiveLimits, getQuotaConfig, limitSource, patchQuotaConfig, spendStatus, type SpendLimits } from "../services/quotas.ts";
import { usageCsv } from "../services/usage-export.ts";
import { roleFromProto, roleToProto } from "./admin.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

const encoder = new TextEncoder();

async function allowed(ctx: HandlerContext, cap: Capability): Promise<Principal> {
  const p = await requirePrincipal(ctx);
  need(p.user, cap);
  return p;
}

const auditToProto = (e: AuditRow) => ({
  id: String(e.id),
  at: e.at,
  action: e.action,
  outcome: e.outcome,
  actorId: e.actor_id,
  actor: e.actor,
  actorIp: e.actor_ip,
  targetType: e.target_type,
  targetId: e.target_id,
  target: e.target,
  detailsJson: JSON.stringify(e.details),
});

function limitsToProto(l: SpendLimits | undefined): QuotaLimits {
  return create(QuotaLimitsSchema, {
    dailyUsd: l?.daily_usd,
    monthlyUsd: l?.monthly_usd,
    objectsBytes: l?.objects_bytes === undefined ? undefined : BigInt(l.objects_bytes),
  });
}

/** Empty limits come out as null: patchQuotaConfig reads that as "remove". */
function limitsFromProto(l: QuotaLimits | undefined): SpendLimits | null {
  const out: SpendLimits = {};
  if (l?.dailyUsd !== undefined) out.daily_usd = l.dailyUsd;
  if (l?.monthlyUsd !== undefined) out.monthly_usd = l.monthlyUsd;
  if (l?.objectsBytes !== undefined) out.objects_bytes = Number(l.objectsBytes);
  return Object.keys(out).length ? out : null;
}

async function quotaStatus(user: { id: string; role: string; email: string; name: string }) {
  const u = toPublicUser(user);
  const [cfg, groups, spend] = await Promise.all([getQuotaConfig(), groupsOfUser(u.id), spendStatus(u)]);
  return create(UserQuotaStatusSchema, {
    own: limitsToProto(cfg.users[u.id]),
    effective: limitsToProto(effectiveLimits(cfg, u, groups.map((g) => g.id))),
    dailySource: limitSource(cfg, u, groups, "daily_usd"),
    monthlySource: limitSource(cfg, u, groups, "monthly_usd"),
    dailySpentUsd: spend.daily_usd,
    monthlySpentUsd: spend.monthly_usd,
  });
}

const groupToProto = (g: GroupView) => ({ id: g.id, name: g.name, source: g.source, memberIds: g.member_ids });

const policyToProto = (p: ManagedPolicy) => ({
  allowRules: p.allow_rules,
  askRules: p.ask_rules,
  denyRules: p.deny_rules,
  managedRulesOnly: p.managed_rules_only,
  sandboxRequired: p.sandbox_required,
  sandboxOutsideRefused: p.sandbox_outside_refused,
  restrictProviders: p.restrict_providers,
  allowedProviders: p.allowed_providers,
  personalProvidersDisabled: p.personal_providers_disabled,
  restrictMcpServers: p.restrict_mcp_servers,
  allowedMcpServers: p.allowed_mcp_servers,
});

function policyFromProto(p: ManagedPolicyPb | undefined): ManagedPolicy {
  if (!p) throw new ApiError("invalid_request", "policy is required");
  return {
    allow_rules: p.allowRules,
    ask_rules: p.askRules,
    deny_rules: p.denyRules,
    managed_rules_only: p.managedRulesOnly,
    sandbox_required: p.sandboxRequired,
    sandbox_outside_refused: p.sandboxOutsideRefused,
    restrict_providers: p.restrictProviders,
    allowed_providers: p.allowedProviders,
    personal_providers_disabled: p.personalProvidersDisabled,
    restrict_mcp_servers: p.restrictMcpServers,
    allowed_mcp_servers: p.allowedMcpServers,
  };
}

const ssoToProto = (p: SsoProviderView) => ({
  id: p.id,
  name: p.name,
  protocol: p.protocol === "saml" ? SsoProtocol.SAML : SsoProtocol.OIDC,
  enabled: p.enabled,
  issuer: p.issuer,
  clientId: p.client_id,
  clientSecret: "",
  clientSecretSet: p.client_secret_set,
  clearClientSecret: false,
  scopes: p.scopes,
  idpEntityId: p.idp_entity_id,
  idpSsoUrl: p.idp_sso_url,
  idpCertificate: p.idp_certificate,
  emailAttribute: p.email_attribute,
  nameAttribute: p.name_attribute,
  usernameAttribute: p.username_attribute,
  groupsAttribute: p.groups_attribute,
  autoCreateUsers: p.auto_create_users,
  allowedDomains: p.allowed_domains,
  allowedGroups: p.allowed_groups,
  roleMappings: p.role_mappings.map((m) => ({ group: m.group, role: roleToProto(m.role) })),
  redirectUri: p.redirect_uri,
  samlAcsUrl: p.saml_acs_url,
  samlEntityId: p.saml_entity_id,
  samlMetadataUrl: p.saml_metadata_url,
});

function ssoFromProto(p: SsoProvider | undefined): SsoInput {
  if (!p) throw new ApiError("invalid_request", "provider is required");
  return {
    id: p.id,
    name: p.name,
    protocol: p.protocol === SsoProtocol.SAML ? "saml" : "oidc",
    enabled: p.enabled,
    issuer: p.issuer,
    client_id: p.clientId,
    client_secret: p.clientSecret || undefined,
    clear_client_secret: p.clearClientSecret,
    scopes: p.scopes,
    idp_entity_id: p.idpEntityId,
    idp_sso_url: p.idpSsoUrl,
    idp_certificate: p.idpCertificate,
    email_attribute: p.emailAttribute,
    name_attribute: p.nameAttribute,
    username_attribute: p.usernameAttribute,
    groups_attribute: p.groupsAttribute,
    auto_create_users: p.autoCreateUsers,
    allowed_domains: p.allowedDomains,
    allowed_groups: p.allowedGroups,
    role_mappings: p.roleMappings.map((m) => ({ group: m.group, role: roleFromProto(m.role) })),
  };
}

export function registerEnterpriseServices(router: ConnectRouter): void {
  router.service(AuditService, {
    listAuditEvents: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "audit.read");
      const r = await listAudit(req.filter ?? {}, req.limit, req.pageToken);
      return { events: r.events.map(auditToProto), nextPageToken: r.next_page_token };
    }),
    exportAuditCsv: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "audit.read");
      const r = await auditCsv(req.filter ?? {});
      return { csv: encoder.encode(r.csv), rows: r.rows, truncated: r.truncated };
    }),
  });

  router.service(BillingService, {
    getQuotas: guard(async (_req, ctx: HandlerContext) => {
      await allowed(ctx, "quotas.read");
      const q = await getQuotaConfig();
      return {
        users: Object.entries(q.users).map(([userId, l]) => ({ userId, limits: limitsToProto(l) })),
        groups: Object.entries(q.groups ?? {}).map(([groupId, l]) => ({ groupId, limits: limitsToProto(l) })),
        roles: Object.entries(q.roles).map(([role, l]) => ({ role: roleToProto(role), limits: limitsToProto(l) })),
        sessionUsd: q.session_usd,
      };
    }),
    setUserQuota: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "quotas.write");
      if (!req.userId) throw new ApiError("invalid_request", "user_id is required");
      await patchQuotaConfig({ users: { [req.userId]: limitsFromProto(req.limits) } });
      return {};
    }),
    setGroupQuota: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "quotas.write");
      if (!req.groupId) throw new ApiError("invalid_request", "group_id is required");
      await patchQuotaConfig({ groups: { [req.groupId]: limitsFromProto(req.limits) } });
      return {};
    }),
    setRoleQuota: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "quotas.write");
      await patchQuotaConfig({ roles: { [roleFromProto(req.role)]: limitsFromProto(req.limits) } });
      return {};
    }),
    setSessionBudget: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "quotas.write");
      await patchQuotaConfig({ session_usd: req.sessionUsd ?? null });
      return {};
    }),
    getUserQuota: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "quotas.read");
      await ensureDb();
      const u = await getDb().query.user.findFirst({ where: eq(schema.user.id, req.userId) });
      if (!u) throw new ApiError("not_found", "User not found");
      return { status: await quotaStatus({ ...u, role: normalizeRole(u.role) }) };
    }),
    getMyQuota: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { status: await quotaStatus(p.user) };
    }),
    exportUsageCsv: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const wide = can(p.user, "usage.read");
      if (!wide && ((req.userId && req.userId !== p.user.id) || req.groupId)) throw new ApiError("permission", "You can export only your own usage");
      const r = await usageCsv({ from: req.from, to: req.to, user_id: wide ? req.userId : p.user.id, group_id: req.groupId }, p.user.id);
      return { csv: encoder.encode(r.csv), rows: r.rows, truncated: r.truncated };
    }),
  });

  router.service(DirectoryService, {
    getMyAccess: guard(async (_req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { role: roleToProto(user.role), capabilities: [...capabilitiesOf(user.role)] };
    }),
    listGroups: guard(async (_req, ctx: HandlerContext) => {
      await allowed(ctx, "directory.read");
      return { groups: (await listGroups()).map(groupToProto) };
    }),
    createGroup: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "directory.write");
      return { group: groupToProto(await createGroup(req.name)) };
    }),
    renameGroup: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "directory.write");
      return { group: groupToProto(await renameGroup(req.id, req.name)) };
    }),
    deleteGroup: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "directory.write");
      await deleteGroup(req.id);
      return {};
    }),
    setGroupMembers: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "directory.write");
      return { group: groupToProto(await setGroupMembers(req.id, req.userIds)) };
    }),
    getScimSettings: guard(async (_req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.read");
      const [settings, tokens] = await Promise.all([getScimSettings(), listScimTokens()]);
      return {
        settings: { roleMappings: settings.role_mappings.map((m) => ({ group: m.group, role: roleToProto(m.role) })) },
        tokens: tokens.map((t) => ({ id: t.id, name: t.name, createdAt: t.created_at, lastUsedAt: t.last_used_at })),
        baseUrl: scimBaseUrl(),
      };
    }),
    setScimSettings: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      const s = await setScimSettings({ role_mappings: (req.settings?.roleMappings ?? []).map((m) => ({ group: m.group, role: roleFromProto(m.role) })) });
      return { settings: { roleMappings: s.role_mappings.map((m) => ({ group: m.group, role: roleToProto(m.role) })) } };
    }),
    createScimToken: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      const r = await createScimToken(req.name);
      return { token: { id: r.token.id, name: r.token.name, createdAt: r.token.created_at, lastUsedAt: "" }, value: r.value };
    }),
    revokeScimToken: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      await revokeScimToken(req.id);
      return {};
    }),
  });

  router.service(MfaService, {
    getMfaStatus: guard(async (_req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      const s = await mfaStatus(user);
      return { enabled: s.enabled, enabledAt: s.enabled_at, recoveryCodesLeft: s.recovery_codes_left, required: s.required };
    }),
    startMfaEnrollment: guard(async (_req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      const e = await startEnrollment(user);
      return { secret: e.secret, uri: e.uri, qrSvg: e.qr_svg };
    }),
    confirmMfaEnrollment: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { recoveryCodes: await confirmEnrollment(user.id, req.code) };
    }),
    disableMfa: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      await disableMfa(user, req.code);
      return {};
    }),
    regenerateRecoveryCodes: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { recoveryCodes: await regenerateRecoveryCodes(user.id, req.code) };
    }),
    getMfaPolicy: guard(async (_req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.read");
      const p = await getMfaPolicy();
      return { policy: { required: p.required, roles: p.roles.map(roleToProto), exemptSso: p.exempt_sso } };
    }),
    setMfaPolicy: guard(async (req, ctx: HandlerContext) => {
      const { user } = await allowed(ctx, "sign_in.write");
      const input = req.policy;
      if (!input) throw new ApiError("invalid_request", "policy is required");
      const p = await setMfaPolicy({ required: input.required, roles: input.roles.map(roleFromProto), exempt_sso: input.exemptSso }, user);
      return { policy: { required: p.required, roles: p.roles.map(roleToProto), exemptSso: p.exempt_sso } };
    }),
    resetUserMfa: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "users.write");
      await resetUserMfa(req.userId);
      return {};
    }),
  });

  router.service(PolicyService, {
    getManagedPolicy: guard(async (_req, ctx: HandlerContext) => {
      const p = await allowed(ctx, "policy.read");
      const r = await getManagedPolicy(p);
      const providers = (await listProviders()).map((x) => ({ id: x.id, label: x.display_name || x.id }));
      const mcpServers = (await listAllMcpNames()).map((name) => ({ id: name, label: name }));
      return { policy: policyToProto(r.policy), updatedAt: r.updated_at, providers, mcpServers };
    }),
    setManagedPolicy: guard(async (req, ctx: HandlerContext) => {
      const p = await allowed(ctx, "policy.write");
      const r = await setManagedPolicy(p, policyFromProto(req.policy));
      return { policy: policyToProto(r.policy), updatedAt: r.updated_at };
    }),
  });

  router.service(SsoService, {
    listSsoProviders: guard(async (_req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.read");
      return { providers: (await listSsoProviders()).map(ssoToProto) };
    }),
    saveSsoProvider: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      return { provider: ssoToProto(await saveSsoProvider(ssoFromProto(req.provider))) };
    }),
    deleteSsoProvider: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      await deleteSsoProvider(req.id);
      return {};
    }),
    testSsoProvider: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      const input = ssoFromProto(req.provider);
      const cfg = await resolveSso(input);
      if (cfg.id) Object.assign(cfg, ssoUrls(cfg.id));
      const r = await testSsoConfig(cfg);
      return { ok: r.ok, checks: r.checks, signInTestUrl: r.sign_in_test_url };
    }),
    getSsoSettings: guard(async (_req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.read");
      const s = await getSsoSettings();
      return { settings: { requireSso: s.require_sso, breakGlassLogins: s.break_glass_logins } };
    }),
    setSsoSettings: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx, "sign_in.write");
      const s = await setSsoSettings({ require_sso: !!req.settings?.requireSso, break_glass_logins: req.settings?.breakGlassLogins ?? [] });
      return { settings: { requireSso: s.require_sso, breakGlassLogins: s.break_glass_logins } };
    }),
  });
}
