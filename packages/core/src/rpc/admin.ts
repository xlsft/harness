/**
 * Connect implementation of harness.auth.v1.AdminService: proto <-> src/services/admin-users.ts and the LDAP
 * settings / directory code in src/auth, and the permission mode policy (src/services/mode-policy.ts). Every method
 * needs the admin role (PERMISSION_DENIED otherwise).
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import {
  AdminService,
  LdapLocalLogin,
  type AdminUser,
  type LdapConfig,
} from "../gen/harness/auth/v1/admin_pb.ts";
import { PermissionMode } from "../gen/harness/agent/v1/agent_pb.ts";
import { User_Role, UserSource } from "../gen/harness/auth/v1/auth_pb.ts";
import {
  getLdapSettings,
  resolveLdapConfig,
  setLdapSettings,
  validate,
  type LdapSettingsInput,
  type PublicLdapSettings,
} from "../auth/ldap-config.ts";
import { findLdapUser } from "../auth/ldap-sign-in.ts";
import { LdapUnavailableError, lookupUser, testConnection } from "../auth/ldap.ts";
import { ApiError } from "../lib/errors.ts";
import {
  createUser,
  deleteUser,
  listUsers,
  listUserSessions,
  modelCounts,
  resetPassword,
  revokeUserSessions,
  setDisabled,
  setRole,
  type AdminUserView,
  type Role,
} from "../services/admin-users.ts";
import { getModePolicy, setModePolicy, type ModeName } from "../services/mode-policy.ts";
import { getLocalModelPolicy, setLocalModelPolicy } from "../services/local-models.ts";
import { deleteProvider, listUserProviders } from "../services/providers.ts";
import { listPolicies as listLimitsPolicies, setPolicy as setLimitsPolicy } from "../limits/policy.ts";
import { policyFromProto, policyToProto } from "./limits.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

function userToProto(u: AdminUserView, selfId: string): Omit<AdminUser, "$typeName"> {
  return {
    id: u.id,
    name: u.name,
    username: u.username,
    email: u.email,
    role: u.role === "admin" ? User_Role.ADMIN : User_Role.USER,
    source: u.source === "ldap" ? UserSource.LDAP : UserSource.LOCAL,
    disabled: u.disabled,
    disabledReason: u.disabled_reason,
    createdAt: u.created_at,
    lastSignInAt: u.last_sign_in_at,
    lastSeenAt: u.last_seen_at,
    isSelf: u.id === selfId,
  };
}

function roleFromProto(r: User_Role): Role {
  if (r === User_Role.ADMIN) return "admin";
  if (r === User_Role.USER || r === User_Role.UNSPECIFIED) return "user";
  throw new ApiError("invalid_request", "Unknown role");
}

function ldapToProto(s: PublicLdapSettings): Omit<LdapConfig, "$typeName"> {
  return {
    enabled: s.enabled,
    urls: s.urls,
    startTls: s.start_tls,
    caCert: s.ca_cert,
    skipVerify: s.skip_verify,
    bindDn: s.bind_dn,
    bindPassword: "",
    bindPasswordSet: s.bind_password_set,
    clearBindPassword: false,
    userBaseDn: s.user_base_dn,
    userFilter: s.user_filter,
    usernameAttribute: s.username_attribute,
    emailAttribute: s.email_attribute,
    nameAttribute: s.name_attribute,
    allowedGroupDn: s.allowed_group_dn,
    adminGroupDn: s.admin_group_dn,
    autoCreateUsers: s.auto_create_users,
    localLogin: s.local_login === "break_glass" ? LdapLocalLogin.BREAK_GLASS : LdapLocalLogin.FALLBACK,
    breakGlassLogins: s.break_glass_logins,
    timeoutMs: s.timeout_ms,
  };
}

function ldapFromProto(c: LdapConfig | undefined): LdapSettingsInput {
  if (!c) throw new ApiError("invalid_request", "config is required");
  return {
    enabled: c.enabled,
    urls: c.urls,
    start_tls: c.startTls,
    ca_cert: c.caCert,
    skip_verify: c.skipVerify,
    bind_dn: c.bindDn,
    bind_password: c.bindPassword || undefined,
    clear_bind_password: c.clearBindPassword,
    user_base_dn: c.userBaseDn,
    user_filter: c.userFilter,
    username_attribute: c.usernameAttribute,
    email_attribute: c.emailAttribute,
    name_attribute: c.nameAttribute,
    allowed_group_dn: c.allowedGroupDn,
    admin_group_dn: c.adminGroupDn,
    auto_create_users: c.autoCreateUsers,
    local_login: c.localLogin === LdapLocalLogin.BREAK_GLASS ? "break_glass" : "fallback",
    break_glass_logins: c.breakGlassLogins,
    timeout_ms: c.timeoutMs,
  };
}

/** A config to test: the stored one with the request's changes, complete enough to connect. */
async function testConfig(c: LdapConfig | undefined) {
  const cfg = await resolveLdapConfig(c ? ldapFromProto(c) : {});
  validate(cfg, { force: true });
  return cfg;
}

const MODE_TO_PROTO: Record<ModeName, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  acceptEdits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  auto: PermissionMode.AUTO,
  dontAsk: PermissionMode.DONT_ASK,
  bypassPermissions: PermissionMode.BYPASS_PERMISSIONS,
};

function modeFromProto(m: PermissionMode): string {
  const name = (Object.keys(MODE_TO_PROTO) as ModeName[]).find((k) => MODE_TO_PROTO[k] === m);
  if (!name) throw new ApiError("invalid_request", `Unknown permission mode ${m}`);
  return name;
}

const modePolicyToProto = (modes: ModeName[]) => ({ disabledModes: modes.map((m) => MODE_TO_PROTO[m]) });

function failure(e: unknown): string {
  if (e instanceof LdapUnavailableError || e instanceof ApiError) return e.message;
  return e instanceof Error ? e.message : String(e);
}

export function registerAdminService(router: ConnectRouter): void {
  const principal = async (ctx: HandlerContext) => {
    const p = await requirePrincipal(ctx);
    if (p.user.role !== "admin") throw new ApiError("permission", "Admin role required");
    return p;
  };
  const admin = async (ctx: HandlerContext) => (await principal(ctx)).user;

  router.service(AdminService, {
    listUsers: guard(async (req, ctx: HandlerContext) => {
      const me = await admin(ctx);
      return { users: (await listUsers(req.query, req.limit)).map((u) => userToProto(u, me.id)) };
    }),

    createUser: guard(async (req, ctx: HandlerContext) => {
      const me = await admin(ctx);
      const r = await createUser({
        name: req.name,
        username: req.username,
        email: req.email,
        password: req.password,
        generate_password: req.generatePassword,
        role: roleFromProto(req.role),
        ldap: req.ldap,
      });
      return { user: userToProto(r.user, me.id), generatedPassword: r.generated_password };
    }),

    resetPassword: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      const r = await resetPassword(req.userId, { password: req.password, generate: req.generate, revoke_sessions: req.revokeSessions });
      return { generatedPassword: r.generated_password };
    }),

    setRole: guard(async (req, ctx: HandlerContext) => {
      const me = await admin(ctx);
      return { user: userToProto(await setRole(req.userId, roleFromProto(req.role)), me.id) };
    }),

    setDisabled: guard(async (req, ctx: HandlerContext) => {
      const me = await admin(ctx);
      return { user: userToProto(await setDisabled(req.userId, req.disabled, req.reason, me.id), me.id) };
    }),

    deleteUser: guard(async (req, ctx: HandlerContext) => {
      const me = await admin(ctx);
      await deleteUser(req.userId, me.id);
      return {};
    }),

    listUserSessions: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      const r = await listUserSessions(req.userId);
      return {
        devices: r.devices.map((d) => ({
          id: d.id,
          name: d.name,
          os: d.os ?? "",
          arch: d.arch ?? "",
          createdAt: d.createdAt.toISOString(),
          lastSeenAt: d.lastSeenAt?.toISOString() ?? "",
        })),
        webSessions: r.web_sessions,
      };
    }),

    revokeUserSessions: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      const r = await revokeUserSessions(req.userId, req.deviceId);
      return { revokedDevices: r.devices, revokedWebSessions: r.web_sessions };
    }),

    listUserProviders: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      const providers = await listUserProviders(req.userId);
      const counts = await modelCounts(providers.map((p) => p.id));
      return {
        providers: providers.map((p) => ({
          id: p.id,
          type: p.type,
          displayName: p.display_name ?? "",
          baseUrl: p.base_url,
          apiKeySet: p.api_key_set,
          enabled: p.enabled,
          modelCount: counts.get(p.id) ?? 0,
        })),
      };
    }),

    deleteUserProvider: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      if (!req.userId) throw new ApiError("invalid_request", "user_id is required");
      await deleteProvider(req.providerId, { userId: req.userId });
      return {};
    }),

    getLdapConfig: guard(async (_req, ctx: HandlerContext) => {
      await admin(ctx);
      return { config: ldapToProto(await getLdapSettings()) };
    }),

    setLdapConfig: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      return { config: ldapToProto(await setLdapSettings(ldapFromProto(req.config))) };
    }),

    testLdapConnection: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      try {
        const r = await testConnection(await testConfig(req.config));
        return { ok: true, message: r.message, url: r.url };
      } catch (e) {
        return { ok: false, message: failure(e) };
      }
    }),

    testLdapUser: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      const login = req.login.trim();
      if (!login) throw new ApiError("invalid_request", "login is required");
      try {
        const u = await lookupUser(await testConfig(req.config), login);
        if (!u) return { found: false, message: "No single directory entry matches this login" };
        const local = await findLdapUser(u.username);
        return {
          found: true,
          message: u.allowed ? "Found" : "Found, but not a member of the allowed group",
          dn: u.dn,
          username: u.username,
          email: u.email,
          name: u.name,
          groups: u.groups,
          allowed: u.allowed,
          admin: u.admin,
          userId: local?.id ?? "",
        };
      } catch (e) {
        return { found: false, message: failure(e) };
      }
    }),

    getPermissionModePolicy: guard(async (_req, ctx: HandlerContext) => {
      return { policy: modePolicyToProto(await getModePolicy(await principal(ctx))) };
    }),

    setPermissionModePolicy: guard(async (req, ctx: HandlerContext) => {
      const p = await principal(ctx);
      const modes = (req.policy?.disabledModes ?? []).map(modeFromProto);
      return { policy: modePolicyToProto(await setModePolicy(p, modes)) };
    }),

    getLocalModelPolicy: guard(async (_req, ctx: HandlerContext) => {
      return { policy: { allowed: await getLocalModelPolicy(await principal(ctx)) } };
    }),

    setLocalModelPolicy: guard(async (req, ctx: HandlerContext) => {
      const p = await principal(ctx);
      if (!req.policy) throw new ApiError("invalid_request", "policy is required");
      return { policy: { allowed: await setLocalModelPolicy(p, req.policy.allowed) } };
    }),

    listProviderLimitsPolicies: guard(async (_req, ctx: HandlerContext) => {
      await admin(ctx);
      return { policies: (await listLimitsPolicies()).map(policyToProto) };
    }),

    setProviderLimitsPolicy: guard(async (req, ctx: HandlerContext) => {
      await admin(ctx);
      return { policy: policyToProto(await setLimitsPolicy(policyFromProto(req.policy))) };
    }),
  });
}
