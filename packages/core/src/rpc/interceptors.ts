/**
 * What every Connect call goes through: a trace span and metrics for all of them, and an audit event for the ones that
 * change the server. Audited methods are listed here, each with the fields it may record: anything not named stays out, so
 * a new secret field in a request cannot end up in the log by accident (audit/log.ts scrubs on top of that).
 */
import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import { context, propagation, SpanKind } from "@opentelemetry/api";
import { record } from "../audit/log.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { recordMetric, withSpan } from "../telemetry/metrics.ts";
import { peerIpKey, principalHolderKey } from "./context.ts";

type Msg = Record<string, unknown>;

interface Audited {
  action: string;
  targetType?: string;
  /** The field holding the target's id. */
  target?: string;
  /** Request fields recorded as details, by name. */
  details?: string[];
  /** Only some calls of this method are worth recording. */
  when?: (req: Msg) => boolean;
  /** Extra details derived from the request. */
  extra?: (req: Msg) => Record<string, unknown>;
}

const obj = (v: unknown): Msg => (v && typeof v === "object" ? (v as Msg) : {});
const has = (v: unknown) => typeof v === "string" && v.length > 0;

const ADMIN = "harness.auth.v1.AdminService";
const SSO = "harness.auth.v1.SsoService";
const DIRECTORY = "harness.auth.v1.DirectoryService";
const BILLING = "harness.auth.v1.BillingService";
const POLICY = "harness.auth.v1.PolicyService";
const MFA = "harness.auth.v1.MfaService";
const PROVIDERS = "harness.llm.v1.ProviderService";
const LIMITS = "harness.llm.v1.ProviderLimitsService";
const SPEECH = "harness.speech.v1.SpeechService";
const MCP = "harness.mcp.v1.McpService";
const VCS = "harness.vcs.v1.VcsService";
const SYNC = "harness.sync.v1.SyncService";
const DEVICE = "harness.auth.v1.DeviceAuthService";

export const AUDITED: Record<string, Audited> = {
  [`${ADMIN}/CreateUser`]: { action: "user.create", targetType: "user", target: "email", details: ["name", "username", "role", "ldap", "generatePassword"] },
  [`${ADMIN}/ResetPassword`]: { action: "user.reset_password", targetType: "user", target: "userId", details: ["generate", "revokeSessions"] },
  [`${ADMIN}/SetRole`]: { action: "user.set_role", targetType: "user", target: "userId", details: ["role"] },
  [`${ADMIN}/SetDisabled`]: { action: "user.set_disabled", targetType: "user", target: "userId", details: ["disabled", "reason"] },
  [`${ADMIN}/DeleteUser`]: { action: "user.delete", targetType: "user", target: "userId" },
  [`${ADMIN}/RevokeUserSessions`]: { action: "user.revoke_sessions", targetType: "user", target: "userId", details: ["deviceId"] },
  [`${ADMIN}/DeleteUserProvider`]: { action: "provider.delete_personal", targetType: "user", target: "userId", details: ["providerId"] },
  [`${ADMIN}/SetLdapConfig`]: {
    action: "sign_in.ldap_update",
    targetType: "ldap",
    extra: (r) => {
      const c = obj(r.config);
      return { enabled: c.enabled, urls: c.urls, bind_dn: c.bindDn, password_changed: has(c.bindPassword) || !!c.clearBindPassword };
    },
  },
  [`${ADMIN}/SetPermissionModePolicy`]: { action: "policy.permission_modes", targetType: "policy", extra: (r) => ({ disabled_modes: obj(r.policy).disabledModes }) },
  [`${ADMIN}/SetLocalModelPolicy`]: { action: "policy.local_models", targetType: "policy", extra: (r) => ({ allowed: obj(r.policy).allowed }) },
  [`${ADMIN}/SetProviderLimitsPolicy`]: { action: "policy.provider_limits", targetType: "provider", extra: (r) => ({ provider_id: obj(r.policy).providerId }) },

  [`${SSO}/SaveSsoProvider`]: {
    action: "sign_in.sso_save",
    targetType: "sso_provider",
    extra: (r) => {
      const p = obj(r.provider);
      return { id: p.id, name: p.name, protocol: p.protocol, enabled: p.enabled, secret_changed: has(p.clientSecret) || !!p.clearClientSecret };
    },
  },
  [`${SSO}/DeleteSsoProvider`]: { action: "sign_in.sso_delete", targetType: "sso_provider", target: "id" },
  [`${SSO}/SetSsoSettings`]: { action: "sign_in.sso_settings", targetType: "sso", extra: (r) => ({ require_sso: obj(r.settings).requireSso, break_glass_logins: obj(r.settings).breakGlassLogins }) },

  [`${DIRECTORY}/CreateGroup`]: { action: "group.create", targetType: "group", target: "name" },
  [`${DIRECTORY}/RenameGroup`]: { action: "group.rename", targetType: "group", target: "id", details: ["name"] },
  [`${DIRECTORY}/DeleteGroup`]: { action: "group.delete", targetType: "group", target: "id" },
  [`${DIRECTORY}/SetGroupMembers`]: { action: "group.set_members", targetType: "group", target: "id", details: ["userIds"] },
  [`${DIRECTORY}/SetScimSettings`]: { action: "scim.settings", targetType: "scim", extra: (r) => ({ role_mappings: obj(r.settings).roleMappings }) },
  [`${DIRECTORY}/CreateScimToken`]: { action: "scim.token_create", targetType: "scim_token", target: "name" },
  [`${DIRECTORY}/RevokeScimToken`]: { action: "scim.token_revoke", targetType: "scim_token", target: "id" },

  [`${BILLING}/SetUserQuota`]: { action: "quota.set_user", targetType: "user", target: "userId", details: ["limits"] },
  [`${BILLING}/SetGroupQuota`]: { action: "quota.set_group", targetType: "group", target: "groupId", details: ["limits"] },
  [`${BILLING}/SetRoleQuota`]: { action: "quota.set_role", targetType: "role", target: "role", details: ["limits"] },
  [`${BILLING}/SetSessionBudget`]: { action: "quota.set_session_budget", targetType: "quota", details: ["sessionUsd"] },

  [`${POLICY}/SetManagedPolicy`]: { action: "policy.managed_update", targetType: "policy", details: ["policy"] },

  [`${MFA}/ConfirmMfaEnrollment`]: { action: "mfa.enroll_confirm" },
  [`${MFA}/DisableMfa`]: { action: "mfa.disable" },
  [`${MFA}/RegenerateRecoveryCodes`]: { action: "mfa.regenerate_recovery_codes" },
  [`${MFA}/SetMfaPolicy`]: { action: "policy.mfa", targetType: "policy", extra: (r) => ({ required: obj(r.policy).required, roles: obj(r.policy).roles, exempt_sso: obj(r.policy).exemptSso }) },
  [`${MFA}/ResetUserMfa`]: { action: "mfa.reset", targetType: "user", target: "userId" },

  [`${PROVIDERS}/CreateProvider`]: { action: "provider.create", targetType: "provider", extra: (r) => providerDetails(r) },
  [`${PROVIDERS}/UpdateProvider`]: { action: "provider.update", targetType: "provider", target: "id", extra: (r) => providerDetails(r) },
  [`${PROVIDERS}/DeleteProvider`]: { action: "provider.delete", targetType: "provider", target: "id" },
  [`${PROVIDERS}/SetSlots`]: { action: "provider.set_slots", targetType: "slots" },
  [`${LIMITS}/SetProviderBudget`]: { action: "provider.set_budget", targetType: "provider", details: ["providerId", "monthlyUsd"] },
  [`${SPEECH}/SetProviderKey`]: { action: "provider.speech_key_set", targetType: "speech_provider", target: "providerId" },
  [`${SPEECH}/ClearProviderKey`]: { action: "provider.speech_key_clear", targetType: "speech_provider", target: "providerId" },
  [`${PROVIDERS}/SetClassifier`]: { action: "provider.set_classifier", targetType: "classifier" },
  [`${MCP}/CreateServer`]: { action: "mcp.server_create", targetType: "mcp_server", extra: (r) => ({ name: obj(r.server).name, scope: obj(r.server).scope }) },
  [`${MCP}/UpdateServer`]: { action: "mcp.server_update", targetType: "mcp_server", extra: (r) => ({ name: obj(r.server).name, id: obj(r.server).id }) },
  [`${MCP}/DeleteServer`]: { action: "mcp.server_delete", targetType: "mcp_server", target: "serverId" },
  [`${VCS}/PutConnection`]: { action: "vcs.connection_put", targetType: "vcs_connection" },
  [`${VCS}/DeleteConnection`]: { action: "vcs.connection_delete", targetType: "vcs_connection", target: "id" },

  // Anyone writing a POLICY document directly instead of through the forms above.
  [`${SYNC}/PutDocument`]: {
    action: "policy.document_put",
    targetType: "policy",
    when: (r) => obj(r.ref).kind === DocumentKind.POLICY,
    extra: (r) => ({ key: obj(r.ref).key, target_roles: r.targetRoles, source: r.source }),
  },
  // (Deleting one is recorded in services/sync.ts: the request only names an id.)

  [`${DEVICE}/RevokeDevice`]: { action: "device.revoke", targetType: "device", target: "deviceId" },
  [`${DEVICE}/ChangePassword`]: { action: "auth.change_password", extra: (r) => ({ revoke_other_sessions: r.revokeOtherSessions }) },
};

function providerDetails(r: Msg): Record<string, unknown> {
  const p = obj(r.provider ?? r);
  // Whether a key was set or cleared, never the key.
  return {
    type: p.type,
    preset: p.presetId,
    display_name: p.displayName,
    base_url: p.baseUrl,
    scope: p.scope,
    key_changed: has(p.apiKey) || p.clearApiKey === true,
  };
}

/** The named fields of a request as plain JSON-able values. */
export function pickDetails(req: Msg, a: Audited): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of a.details ?? []) out[f] = plain(req[f]);
  return { ...out, ...plainObject(a.extra?.(req)) };
}

function plainObject(v: Record<string, unknown> | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, x] of Object.entries(v ?? {})) out[k] = plain(x);
  return out;
}

function plain(v: unknown): unknown {
  if (typeof v === "bigint") return v.toString();
  if (Array.isArray(v)) return v.map(plain);
  if (v && typeof v === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v as Msg)) if (k !== "$typeName" && k !== "$unknown") out[k] = plain(x);
    return out;
  }
  return v;
}

const audit: Interceptor = (next) => async (req) => {
  if (req.stream) return next(req);
  const entry = AUDITED[`${req.service.typeName}/${req.method.name}`];
  const message = req.message as unknown as Msg;
  if (!entry || (entry.when && !entry.when(message))) return next(req);
  const finish = async (outcome: "success" | "failure", code?: string) => {
    const p = req.contextValues.get(principalHolderKey).principal;
    if (!p) return; // Nobody authenticated: nothing was at stake, and scanners would fill the log.
    const targetId = entry.target ? String(plain(message[entry.target]) ?? "") : "";
    await record({
      action: entry.action,
      outcome,
      actor: { id: p.user.id, label: p.user.email, ip: req.contextValues.get(peerIpKey) },
      target: { type: entry.targetType, id: targetId },
      details: { ...pickDetails(message, entry), ...(code ? { error: code } : {}) },
    });
  };
  try {
    const res = await next(req);
    await finish("success");
    return res;
  } catch (e) {
    await finish("failure", e instanceof ConnectError ? Code[e.code] : "internal");
    throw e;
  }
};

const telemetry: Interceptor = (next) => async (req) => {
  const start = performance.now();
  const procedure = `${req.service.typeName}/${req.method.name}`;
  const parent = propagation.extract(context.active(), req.header, {
    get: (h, k) => h.get(k) ?? undefined,
    keys: (h) => [...h.keys()],
  });
  return context.with(parent, () =>
    withSpan(
      procedure,
      { "rpc.system": "connect_rpc", "rpc.service": req.service.typeName, "rpc.method": req.method.name },
      async (span) => {
        let code = "ok";
        try {
          return await next(req);
        } catch (e) {
          code = e instanceof ConnectError ? Code[e.code] : "internal";
          span.setAttribute("rpc.connect_rpc.error_code", code);
          throw e;
        } finally {
          recordMetric("harness.rpc.duration", performance.now() - start, { "rpc.service": req.service.typeName, "rpc.method": req.method.name, code }, "ms");
        }
      },
      SpanKind.SERVER,
    ),
  );
};

export const rpcInterceptors: Interceptor[] = [telemetry, audit];
