import { createContextKey, createContextValues, type ContextValues, type HandlerContext } from "@connectrpc/connect";
import { getAuth } from "../auth/better-auth.ts";
import { config } from "../env.ts";
import { ApiError } from "../lib/errors.ts";
import { enforceIpRateLimit } from "../lib/rate-limit.ts";
import { authenticateAccessToken, toPublicUser, type Principal } from "../services/devices.ts";

/**
 * Internal header carrying the peer address from the HTTP layer into the Connect handlers on the
 * fetch path. Any client-sent value is stripped before the request reaches the router.
 */
export const PEER_IP_HEADER = "x-harness-peer-ip";

/** Client IP address of the current call ("" when unknown). */
export const peerIpKey = createContextKey<string>("", { description: "peer ip" });

/** Filled by requirePrincipal, read by the audit interceptor (rpc/interceptors.ts). */
export const principalHolderKey = createContextKey<{ principal?: Principal }>({}, { description: "principal holder" });

export function contextValuesWithIp(ip: string | undefined): ContextValues {
  return createContextValues().set(peerIpKey, ip ?? "").set(principalHolderKey, {});
}

/**
 * Picks the client address: the socket peer, or with HARNESS_TRUST_PROXY=true the address the
 * nearest proxy appended to X-Forwarded-For (its rightmost entry; left entries are client-controlled).
 */
export function clientIp(socketIp: string | undefined, forwardedFor: string | null | undefined): string {
  if (process.env.HARNESS_TRUST_PROXY === "true" && forwardedFor) {
    const parts = forwardedFor.split(",").map((s) => s.trim()).filter(Boolean);
    if (parts.length) return parts[parts.length - 1]!;
  }
  return socketIp ?? "";
}

/** Per-IP token bucket for the unauthenticated DeviceAuthService methods. */
export function rateLimitByIp(ctx: HandlerContext, group: string): void {
  enforceIpRateLimit(group, ctx.values.get(peerIpKey));
}

/**
 * Rejects clients older than HARNESS_MIN_CLIENT_VERSION (User-Agent: harness-client/<version> (...)).
 * Non-harness user agents (browsers, curl, tests) are not checked.
 */
export function checkClientVersion(ctx: HandlerContext): void {
  const min = process.env.HARNESS_MIN_CLIENT_VERSION;
  if (!min) return;
  const ua = ctx.requestHeader.get("user-agent") ?? "";
  const m = /harness-client\/(\d+(?:\.\d+){0,2})/.exec(ua);
  if (!m) return;
  if (compareVersions(m[1]!, min) < 0) {
    throw new ApiError("unsupported_client", `harness-client ${m[1]} is older than the minimum supported ${min}; please update`);
  }
}

export function compareVersions(a: string, b: string): number {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d;
  }
  return 0;
}

/**
 * Services the web console (the UI the server serves itself, deploy/README.md) may reach with the browser's session cookie,
 * for what needs no client on the user's machine. Everything else, the gateway above all, takes access tokens only.
 */
const COOKIE_SERVICES = new Set([
  "harness.auth.v1.AdminService",
  "harness.auth.v1.AuditService",
  "harness.auth.v1.BillingService",
  "harness.auth.v1.DirectoryService",
  "harness.auth.v1.MfaService",
  "harness.auth.v1.PolicyService",
  "harness.auth.v1.SsoService",
  "harness.auth.v1.DeviceAuthService",
  "harness.llm.v1.ProviderService",
  "harness.llm.v1.ProviderLimitsService",
  "harness.llm.v1.UsageService",
  "harness.relay.v1.HostsService",
]);

/** The signed-in browser, for a same-origin call to one of COOKIE_SERVICES. */
async function principalFromCookie(ctx: HandlerContext): Promise<Principal | undefined> {
  if (!ctx.requestHeader.get("cookie") || !COOKIE_SERVICES.has(ctx.service.typeName)) return undefined;
  // A cookie is sent by the browser whatever page asked: only our own pages count. The Origin header cannot be forged by one.
  const origin = ctx.requestHeader.get("origin");
  if (!origin || origin !== new URL(config().publicUrl).origin) return undefined;
  const s = await getAuth().api.getSession({ headers: ctx.requestHeader });
  if (!s?.user || (s.user as { banned?: boolean }).banned || (s.session as { mfaPending?: boolean }).mfaPending) return undefined;
  return { user: toPublicUser(s.user as { id: string; email: string; name: string; role: string | null }), via: "session" };
}

/** Authenticates `Authorization: Bearer hat_…`, or the session cookie of the web console. */
export async function requirePrincipal(ctx: HandlerContext): Promise<Principal> {
  checkClientVersion(ctx);
  const authz = ctx.requestHeader.get("authorization");
  const m = authz ? /^Bearer\s+(\S+)$/i.exec(authz.trim()) : null;
  const p = m ? await authenticateAccessToken(m[1]!) : authz ? undefined : await principalFromCookie(ctx);
  if (!p) throw new ApiError("authentication", "Missing or invalid access token");
  // A team host's supervisor starts workers and nothing else; it has no business with models, sessions or the relay.
  if (p.deviceKind === "host_supervisor" && ctx.service.typeName !== "harness.relay.v1.HostAgentService") {
    throw new ApiError("permission", "This host's token can only talk to the host service");
  }
  ctx.values.get(principalHolderKey).principal = p;
  return p;
}
