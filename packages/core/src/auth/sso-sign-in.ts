/**
 * The sign-in flow through an identity provider, the same for OIDC and SAML:
 *
 *   GET  /api/sso/:id/start             a flow row and a browser-binding cookie, then off to the provider
 *   GET  /api/sso/:id/oidc/callback     or
 *   POST /api/sso/:id/saml/acs          verifies the answer and maps it to an account
 *   GET  /api/auth/sso/complete?code=   checks the browser, creates the normal session, returns to /device (or /)
 *
 * The last step is separate because a SAML answer arrives as a cross-site POST, which does not carry our SameSite=Lax
 * cookies: only the redirect to /sso/complete can see the browser-binding cookie. Without that check a response obtained
 * by an attacker could sign a victim's browser into the attacker's account. It is a Better Auth endpoint so that the session
 * goes through the usual hooks: disabled users are refused and a second factor is asked for as for any sign-in.
 */
import { randomBytes } from "node:crypto";
import type { BetterAuthPlugin } from "better-auth";
import { APIError, createAuthEndpoint } from "better-auth/api";
import { setSessionCookie } from "better-auth/cookies";
import { and, eq, lt } from "drizzle-orm";
import * as z from "zod";
import { record } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { sha256 } from "../lib/ids.ts";
import { syncSourceGroups } from "../services/groups.ts";
import { activeAdminCount } from "./admins.ts";
import { AUTH_PEER_IP_HEADER, type AccountCreator } from "./ldap-sign-in.ts";
import { normalizeRole, roleFromGroups, type Role } from "./roles.ts";
import { getSsoRuntime, type SsoRuntime } from "./sso-config.ts";
import { authorizationUrl, discover, exchangeCode, newOidcSecrets, type OidcSecrets } from "./sso-oidc.ts";
import { FlowCache, identityFromProfile, samlClient } from "./sso-saml.ts";

export const SSO_BINDING_COOKIE = "harness_sso_binding";
const FLOW_TTL_MS = 10 * 60_000;
const COMPLETE_TTL_MS = 2 * 60_000;
const USERNAME = /^[a-zA-Z0-9_.]{3,30}$/;

export interface SsoIdentity {
  subject: string;
  email: string;
  email_verified: boolean;
  name: string;
  username: string;
  groups: string[];
  claims: Record<string, unknown>;
}

export const ssoProviderId = (id: string) => `sso:${id}`;

const newCode = () => randomBytes(32).toString("base64url");
const secretsKey = () => deriveKey("setting-secrets");
const secretsAad = (idHash: string) => `sso_flow:${idHash}`;

/** Local paths a sign-in may return to; anything else goes to /device. */
export function safeReturnTo(raw: string | null | undefined): string {
  const v = raw ?? "";
  return /^\/(device(\?code=[A-Za-z0-9-]{1,32})?)?$/.test(v) ? v : "/device";
}

/** Reused from the cookie when there is one, so two tabs signing in at once do not undo each other. */
export function bindingFromCookie(cookieHeader: string | null): string {
  const m = (cookieHeader ?? "").match(new RegExp(`(?:^|;\\s*)${SSO_BINDING_COOKIE}=([A-Za-z0-9_-]{32,64})`));
  return m?.[1] ?? "";
}

export function bindingCookieHeader(value: string): string {
  const secure = config().publicUrl.startsWith("https:") ? "; Secure" : "";
  return `${SSO_BINDING_COOKIE}=${value}; Path=/api; HttpOnly; SameSite=Lax; Max-Age=${FLOW_TTL_MS / 1000}${secure}`;
}

async function purgeExpired(): Promise<void> {
  await getDb().delete(schema.ssoFlow).where(lt(schema.ssoFlow.expiresAt, new Date()));
}

// --- try-outs from Settings -------------------------------------------------------------------------------

/** A one-time link that runs a real sign-in against `providerId` and shows what came back, with no session. */
export async function createTestLink(providerId: string): Promise<string> {
  await ensureDb();
  await purgeExpired();
  const code = newCode();
  const now = Date.now();
  await getDb().insert(schema.ssoFlow).values({
    idHash: sha256(`test:${code}`),
    providerId,
    browserHash: "",
    test: true,
    expiresAt: new Date(now + FLOW_TTL_MS),
    createdAt: new Date(now),
  });
  return `${config().publicUrl}/api/sso/${providerId}/start?test=${code}`;
}

// --- start ------------------------------------------------------------------------------------------------

export interface StartResult {
  location: string;
  binding: string;
}

/**
 * Begins a sign-in. `testCode` is from createTestLink and works on a disabled provider too; a normal sign-in needs an
 * enabled one.
 */
export async function startSso(providerId: string, opts: { returnTo: string; binding: string; testCode?: string }): Promise<StartResult> {
  await ensureDb();
  const cfg = await getSsoRuntime(providerId);
  if (!cfg) throw new ApiError("not_found", "Unknown identity provider");
  const db = getDb();
  let test = false;
  if (opts.testCode) {
    const [row] = await db.delete(schema.ssoFlow).where(and(eq(schema.ssoFlow.idHash, sha256(`test:${opts.testCode}`)), eq(schema.ssoFlow.providerId, providerId))).returning();
    if (!row || row.expiresAt.getTime() <= Date.now()) throw new ApiError("authentication", "This test link has expired; run the test again");
    test = true;
  } else if (!cfg.enabled) {
    throw new ApiError("not_found", "This identity provider is not enabled");
  }
  await purgeExpired();
  const binding = opts.binding || newCode();
  const now = Date.now();
  const base = { providerId, browserHash: sha256(binding), returnTo: safeReturnTo(opts.returnTo), test, expiresAt: new Date(now + FLOW_TTL_MS), createdAt: new Date(now) };

  if (cfg.protocol === "oidc") {
    const doc = await discover(cfg.issuer);
    const state = newCode();
    const secrets = newOidcSecrets();
    const idHash = sha256(state);
    await db.insert(schema.ssoFlow).values({ ...base, idHash, secretsEnc: encryptSecret(JSON.stringify(secrets), secretsAad(idHash), secretsKey()) });
    return { location: authorizationUrl(doc, cfg, state, secrets), binding };
  }

  const requestId = `_${randomBytes(20).toString("hex")}`;
  await db.insert(schema.ssoFlow).values({ ...base, idHash: sha256(requestId) });
  return { location: await samlClient(cfg, new FlowCache(), requestId).getAuthorizeUrlAsync("", {}), binding };
}

// --- answers ----------------------------------------------------------------------------------------------

export interface Answer {
  /** Where to send the browser, or an HTML page to show (a try-out). */
  location?: string;
  testResult?: TestResult;
}

export interface TestResult {
  provider: string;
  identity: { subject: string; email: string; name: string; username: string; groups: string[] };
  claims: Record<string, unknown>;
  role: string;
  allowed: boolean;
  reason: string;
}

async function finish(cfg: SsoRuntime, flow: { browserHash: string; returnTo: string; test: boolean; providerId: string }, identity: SsoIdentity): Promise<Answer> {
  if (flow.test) {
    const refusal = refusalOf(cfg, identity);
    await record({ action: "sso.test_sign_in", target: { type: "sso_provider", id: cfg.id, label: cfg.name }, details: { subject: identity.subject, allowed: !refusal } });
    return {
      testResult: {
        provider: cfg.name,
        identity: { subject: identity.subject, email: identity.email, name: identity.name, username: identity.username, groups: identity.groups },
        claims: identity.claims,
        role: cfg.role_mappings.length ? roleFromGroups(cfg.role_mappings, identity.groups) : "managed in Users",
        allowed: !refusal,
        reason: refusal ?? "",
      },
    };
  }
  const user = await provisionSsoUser(cfg, identity);
  const code = newCode();
  const now = Date.now();
  await getDb().insert(schema.ssoFlow).values({
    idHash: sha256(`complete:${code}`),
    providerId: cfg.id,
    browserHash: flow.browserHash,
    returnTo: flow.returnTo,
    userId: user.id,
    expiresAt: new Date(now + COMPLETE_TTL_MS),
    createdAt: new Date(now),
  });
  return { location: `${config().publicUrl}/api/auth/sso/complete?code=${code}` };
}

export async function oidcCallback(providerId: string, params: { state: string; code: string; error: string; errorDescription: string }, peerIp: string): Promise<Answer> {
  await ensureDb();
  const cfg = await getSsoRuntime(providerId);
  if (!cfg || cfg.protocol !== "oidc") throw new ApiError("not_found", "Unknown identity provider");
  const idHash = sha256(params.state);
  const [flow] = params.state ? await getDb().delete(schema.ssoFlow).where(and(eq(schema.ssoFlow.idHash, idHash), eq(schema.ssoFlow.providerId, providerId))).returning() : [];
  if (!flow || flow.expiresAt.getTime() <= Date.now() || !flow.secretsEnc) throw new ApiError("authentication", "This sign-in has expired; start again");
  return guarded(cfg, peerIp, async () => {
    if (params.error) throw new ApiError("authentication", `The identity provider refused: ${params.errorDescription || params.error}`);
    if (!params.code) throw new ApiError("authentication", "The identity provider sent no code");
    const secrets = JSON.parse(decryptSecret(flow.secretsEnc!, secretsAad(idHash), secretsKey())) as OidcSecrets;
    const doc = await discover(cfg.issuer);
    return finish(cfg, flow, await exchangeCode(doc, cfg, params.code, secrets));
  });
}

export async function samlAcs(providerId: string, samlResponse: string, peerIp: string): Promise<Answer> {
  await ensureDb();
  const cfg = await getSsoRuntime(providerId);
  if (!cfg || cfg.protocol !== "saml") throw new ApiError("not_found", "Unknown identity provider");
  return guarded(cfg, peerIp, async () => {
    if (!samlResponse) throw new ApiError("authentication", "The identity provider sent no response");
    const cache = new FlowCache();
    const { profile } = await samlClient(cfg, cache).validatePostResponseAsync({ SAMLResponse: samlResponse });
    const flow = cache.taken;
    if (!profile || !flow || flow.providerId !== providerId) throw new ApiError("authentication", "This response does not answer a sign-in started here");
    return finish(cfg, flow, identityFromProfile(cfg, profile));
  });
}

/** Failures are recorded, and anything that is not one of ours leaves as a generic message: it may quote the response. */
async function guarded(cfg: SsoRuntime, peerIp: string, fn: () => Promise<Answer>): Promise<Answer> {
  try {
    return await fn();
  } catch (e) {
    const known = e instanceof ApiError;
    await record({
      action: "auth.sign_in_failed",
      outcome: "failure",
      actor: { ip: peerIp },
      target: { type: "sso_provider", id: cfg.id, label: cfg.name },
      details: { method: cfg.protocol, reason: known ? e.message : "verification failed" },
    });
    if (known) throw e;
    console.warn(`[harness] SSO sign-in through ${cfg.id} failed: ${e instanceof Error ? e.message : String(e)}`);
    throw new ApiError("authentication", "The identity provider's answer could not be verified. An administrator can see why in the server log.");
  }
}

// --- accounts ---------------------------------------------------------------------------------------------

type UserRow = typeof schema.user.$inferSelect;

/** Why this identity may not sign in; undefined when it may. */
function refusalOf(cfg: SsoRuntime, id: SsoIdentity): string | undefined {
  if (!id.subject) return "The identity provider sent no subject";
  if (!id.email) return "The identity provider sent no email address";
  if (!id.email_verified) return "The identity provider says this email address is not verified";
  if (cfg.allowed_domains.length && !cfg.allowed_domains.includes(id.email.split("@")[1] ?? "")) return "This email domain may not sign in here";
  if (cfg.allowed_groups.length && !id.groups.some((g) => cfg.allowed_groups.includes(g))) return "You are not in a group that may sign in here";
  return undefined;
}

async function findLinked(providerId: string, subject: string): Promise<UserRow | undefined> {
  const [row] = await getDb()
    .select({ user: schema.user })
    .from(schema.account)
    .innerJoin(schema.user, eq(schema.user.id, schema.account.userId))
    .where(and(eq(schema.account.providerId, ssoProviderId(providerId)), eq(schema.account.accountId, subject)))
    .limit(1);
  return row?.user;
}

/** Applies a mapped role; the last active admin is never demoted by a directory. */
async function roleUpdate(existing: UserRow, mapped: Role): Promise<Partial<typeof schema.user.$inferInsert>> {
  if (mapped === normalizeRole(existing.role)) return {};
  if (existing.role === "admin" && !existing.banned && (await activeAdminCount(existing.id)) === 0) return {};
  return { role: mapped };
}

/**
 * Creates or updates the account of somebody the provider vouched for. An existing account with the same email is only
 * linked when it has no local password (provisioned by an admin or SCIM for exactly this); an account with a password stays
 * its owner's, because linking it would let whoever controls the provider's email claim take it over.
 */
export async function provisionSsoUser(cfg: SsoRuntime, id: SsoIdentity): Promise<UserRow> {
  const refusal = refusalOf(cfg, id);
  if (refusal) throw new ApiError("permission", refusal);
  const { getAuth } = await import("./better-auth.ts");
  const adapter = (await getAuth().$context).internalAdapter as unknown as AccountCreator;
  const db = getDb();
  const mapped = cfg.role_mappings.length ? roleFromGroups(cfg.role_mappings, id.groups) : undefined;
  const name = id.name || id.username || id.email;

  let user = await findLinked(cfg.id, id.subject);
  if (!user) {
    const sameEmail = await db.query.user.findFirst({ where: eq(schema.user.email, id.email) });
    if (sameEmail) {
      const password = await db.query.account.findFirst({ where: and(eq(schema.account.userId, sameEmail.id), eq(schema.account.providerId, "credential")) });
      if (password?.password) throw new ApiError("permission", "A local account with this email already exists. Ask an administrator to remove its password or the account.");
      await adapter.createAccount({ userId: sameEmail.id, providerId: ssoProviderId(cfg.id), accountId: id.subject });
      user = sameEmail;
    }
  }
  if (user) {
    const set: Partial<typeof schema.user.$inferInsert> = mapped ? await roleUpdate(user, mapped) : {};
    if (name !== user.name) set.name = name;
    if (id.email !== user.email && !(await db.query.user.findFirst({ where: eq(schema.user.email, id.email) }))) set.email = id.email;
    if (Object.keys(set).length) [user] = await db.update(schema.user).set({ ...set, updatedAt: new Date() }).where(eq(schema.user.id, user.id)).returning();
  } else {
    if (!cfg.auto_create_users) throw new ApiError("permission", "There is no account for you here yet. Ask an administrator to add you.");
    const handle = id.username.toLowerCase();
    const usable = USERNAME.test(handle) && !(await db.query.user.findFirst({ where: eq(schema.user.username, handle) }));
    const created = await adapter.createUser({
      email: id.email,
      name,
      emailVerified: true,
      role: mapped ?? undefined,
      ...(usable ? { username: handle, displayUsername: id.username } : {}),
    }, { method: "sso" });
    await adapter.createAccount({ userId: created.id, providerId: ssoProviderId(cfg.id), accountId: id.subject });
    user = await db.query.user.findFirst({ where: eq(schema.user.id, created.id) });
  }
  await syncSourceGroups(user!.id, "sso", id.groups);
  return user!;
}

// --- completion -------------------------------------------------------------------------------------------

export function ssoComplete(): BetterAuthPlugin {
  return {
    id: "harness-sso",
    endpoints: {
      ssoComplete: createAuthEndpoint("/sso/complete", { method: "GET", query: z.object({ code: z.string().max(128) }) }, async (ctx) => {
        await ensureDb();
        const [flow] = await getDb().delete(schema.ssoFlow).where(eq(schema.ssoFlow.idHash, sha256(`complete:${ctx.query.code}`))).returning();
        const binding = bindingFromCookie(ctx.request?.headers.get("cookie") ?? null);
        if (!flow?.userId || flow.expiresAt.getTime() <= Date.now() || !binding || sha256(binding) !== flow.browserHash) {
          await record({ action: "auth.sign_in_failed", outcome: "failure", actor: { ip: ctx.request?.headers.get(AUTH_PEER_IP_HEADER) ?? "" }, details: { method: "sso", reason: "completion not valid for this browser" } });
          throw new APIError("UNAUTHORIZED", { message: "This sign-in was started in another browser or has expired. Start again." });
        }
        const session = await ctx.context.internalAdapter.createSession(flow.userId);
        if (!session) throw new APIError("INTERNAL_SERVER_ERROR", { message: "Failed to create a session" });
        const user = await ctx.context.internalAdapter.findUserById(flow.userId);
        await setSessionCookie(ctx, { session, user: user! });
        throw ctx.redirect(`${config().publicUrl}${safeReturnTo(flow.returnTo)}`);
      }),
    },
  };
}
