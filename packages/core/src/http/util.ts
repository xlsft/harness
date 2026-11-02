/**
 * Helpers for the plain-HTTP routes (admin JSON API, /device page, Better Auth). Client-facing
 * APIs are Connect services in src/rpc. All business logic lives in src/services.
 */
import { defineHandler, type H3Event } from "h3";
import { getAuth } from "../auth/better-auth.ts";
import { ApiError, asApiError } from "../lib/errors.ts";
import { authenticateAccessToken, toPublicUser, type Principal } from "../services/devices.ts";

export function json(data: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", ...headers },
  });
}

export function errorResponse(err: unknown): Response {
  const e = asApiError(err);
  const headers: Record<string, string> = {};
  if (e.status === 401) headers["www-authenticate"] = 'Bearer realm="harness"';
  if (e.retryAfterMs !== undefined) headers["retry-after"] = String(Math.ceil(e.retryAfterMs / 1000));
  return json(e.toJSON(), e.status, headers);
}

/** Wraps a handler: ApiError -> JSON error response. */
export function api(fn: (event: H3Event) => Promise<unknown> | unknown) {
  return defineHandler(async (event) => {
    try {
      const out = await fn(event);
      return out instanceof Response ? out : json(out);
    } catch (err) {
      return errorResponse(err);
    }
  });
}

export async function readJson<T = Record<string, unknown>>(event: H3Event): Promise<T> {
  const text = await event.req.text();
  if (!text) return {} as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new ApiError("invalid_request", "Body must be valid JSON");
  }
}

/** Bearer access token (clients) or Better Auth session cookie (web UI). */
export async function getPrincipal(event: H3Event): Promise<Principal | undefined> {
  const authz = event.req.headers.get("authorization");
  if (authz) {
    const m = /^Bearer\s+(.+)$/i.exec(authz.trim());
    if (!m) return undefined;
    return authenticateAccessToken(m[1]!.trim());
  }
  if (!event.req.headers.get("cookie")) return undefined;
  const session = await getAuth().api.getSession({ headers: event.req.headers });
  if (!session?.user || (session.user as { banned?: boolean }).banned) return undefined;
  return {
    user: toPublicUser(session.user as { id: string; email: string; name: string; role: string | null }),
    via: "session",
  };
}

export async function requirePrincipal(event: H3Event): Promise<Principal> {
  const p = await getPrincipal(event);
  if (!p) throw new ApiError("authentication", "Missing or invalid credentials");
  return p;
}

export async function requireAdmin(event: H3Event): Promise<Principal> {
  const p = await requirePrincipal(event);
  if (p.user.role !== "admin") throw new ApiError("permission", "Admin role required");
  return p;
}

export function param(event: H3Event, name: string): string {
  const v = (event.context.params as Record<string, string> | undefined)?.[name];
  if (!v) throw new ApiError("invalid_request", `Missing path parameter ${name}`);
  return decodeURIComponent(v);
}
