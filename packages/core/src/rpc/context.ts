import { createContextKey, createContextValues, type ContextValues, type HandlerContext } from "@connectrpc/connect";
import { ApiError } from "../lib/errors.ts";
import { enforceIpRateLimit } from "../lib/rate-limit.ts";
import { authenticateAccessToken, type Principal } from "../services/devices.ts";

/**
 * Internal header carrying the peer address from the HTTP layer into the Connect handlers on the
 * fetch path. Any client-sent value is stripped before the request reaches the router.
 */
export const PEER_IP_HEADER = "x-harness-peer-ip";

/** Client IP address of the current call ("" when unknown). */
export const peerIpKey = createContextKey<string>("", { description: "peer ip" });

export function contextValuesWithIp(ip: string | undefined): ContextValues {
  return createContextValues().set(peerIpKey, ip ?? "");
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

/** Authenticates `Authorization: Bearer hat_…`. */
export async function requirePrincipal(ctx: HandlerContext): Promise<Principal> {
  checkClientVersion(ctx);
  const authz = ctx.requestHeader.get("authorization");
  const m = authz ? /^Bearer\s+(\S+)$/i.exec(authz.trim()) : null;
  const p = m ? await authenticateAccessToken(m[1]!) : undefined;
  if (!p) throw new ApiError("authentication", "Missing or invalid access token");
  return p;
}
