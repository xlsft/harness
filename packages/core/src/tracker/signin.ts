/**
 * What tracker and knowledge connections check the same way: the address, the sign-in method and the options. Mirrors
 * packages/client/tracker/signin.go.
 */
import { ApiError } from "../lib/errors.ts";
import { assertUrlAllowed } from "../lib/ssrf.ts";
import type { AuthMethodName, OptionField } from "./provider.ts";

const OPTION_KEY = /^[a-z][a-z0-9_]{0,63}$/;

/** Trims, adds https:// to a bare host, drops trailing slashes and a trailing /api. */
export function normalizeUrl(raw: string): string {
  let v = raw.trim().replace(/\/+$/, "");
  if (v && !v.includes("://")) v = `https://${v}`;
  return v.replace(/\/api$/, "");
}

/** A plain http(s) address the SSRF guard allows, the default for an empty one. */
export function checkAddress(raw: string, opts: { defaultBaseUrl?: string; allowPrivateNetwork: boolean; normalize?: (raw: string) => string }): URL {
  const value = raw.trim() || opts.defaultBaseUrl || "";
  const baseUrl = (opts.normalize ?? normalizeUrl)(value);
  if (!baseUrl) throw new ApiError("invalid_request", "base_url is required");
  let u: URL;
  try {
    u = new URL(baseUrl);
  } catch {
    throw new ApiError("invalid_request", "base_url must be an http(s) URL");
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") throw new ApiError("invalid_request", "base_url must be an http(s) URL");
  if (u.username || u.password || u.search || u.hash) throw new ApiError("invalid_request", "base_url must not carry credentials, a query or a fragment");
  assertUrlAllowed(baseUrl, opts.allowPrivateNetwork);
  return new URL(baseUrl.replace(/\/+$/, ""));
}

/** The href of a checked address without a trailing slash. */
export const hrefOf = (u: URL) => u.href.replace(/\/+$/, "");

/** Keeps declared options only, fills defaults, checks required and allowed values. */
export function checkOptions(fields: OptionField[] | undefined, input: Record<string, string> | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of fields ?? []) {
    if (!OPTION_KEY.test(f.key)) throw new ApiError("invalid_request", `Option key ${f.key} is not valid`);
    const v = (input?.[f.key] ?? "").trim() || f.defaultValue || "";
    if (!v) {
      if (f.required) throw new ApiError("invalid_request", `${f.label} is required`);
      continue;
    }
    if (f.allowedValues?.length && !f.allowedValues.includes(v)) throw new ApiError("invalid_request", `${f.label} must be one of: ${f.allowedValues.join(", ")}`);
    if (v.length > 500) throw new ApiError("invalid_request", `${f.label} is too long`);
    out[f.key] = v;
  }
  return out;
}

/** The method is one the provider offers and has what it needs. */
export function checkAuth(methods: AuthMethodName[], auth: string, login: string | undefined, oauthClientId: string | undefined): AuthMethodName {
  if (!methods.includes(auth as AuthMethodName)) throw new ApiError("invalid_request", "Choose how this connection signs in");
  if (auth === "password" && !login?.trim()) throw new ApiError("invalid_request", "login is required for login and password");
  if (auth === "email_token" && !login?.includes("@")) throw new ApiError("invalid_request", "The account email is required with an API token");
  if (auth === "oauth" && !oauthClientId?.trim()) throw new ApiError("invalid_request", "The OAuth client id is required: an admin of the instance creates the client");
  return auth as AuthMethodName;
}

/** Where a method keeps the secret the form sends. */
export function secretKey(auth: string): "token" | "password" | undefined {
  if (auth === "token" || auth === "email_token") return "token";
  if (auth === "password") return "password";
  return undefined;
}
