/**
 * OpenID Connect authorization code flow with PKCE: discovery, the authorization redirect, the code exchange, and
 * verification of the ID token against the provider's published keys (jose). Only asymmetric signatures are accepted.
 */
import { createHash, randomBytes } from "node:crypto";
import { createRemoteJWKSet, jwtVerify, type JWTPayload } from "jose";
import type { SsoIdentity } from "./sso-sign-in.ts";
import { checkIdpUrl, type SsoRuntime } from "./sso-config.ts";

export interface OidcDiscovery {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  jwks_uri: string;
  userinfo_endpoint?: string;
  token_endpoint_auth_methods_supported?: string[];
  response_types_supported?: string[];
}

const TIMEOUT_MS = 10_000;
const ALGORITHMS = ["RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"];
const b64url = (b: Buffer) => b.toString("base64url");

export class OidcError extends Error {}

export async function discover(issuer: string): Promise<OidcDiscovery> {
  checkIdpUrl(issuer, "The issuer");
  const url = `${issuer.replace(/\/+$/, "")}/.well-known/openid-configuration`;
  let res: Response;
  try {
    res = await fetch(url, { signal: AbortSignal.timeout(TIMEOUT_MS), redirect: "error" });
  } catch (e) {
    throw new OidcError(`Could not read ${url}: ${e instanceof Error ? e.message : String(e)}`);
  }
  if (!res.ok) throw new OidcError(`${url} answered ${res.status}`);
  let doc: OidcDiscovery;
  try {
    doc = (await res.json()) as OidcDiscovery;
  } catch {
    throw new OidcError(`${url} did not return JSON`);
  }
  if (doc.issuer?.replace(/\/+$/, "") !== issuer.replace(/\/+$/, "")) throw new OidcError(`The document names another issuer: ${doc.issuer}`);
  for (const k of ["authorization_endpoint", "token_endpoint", "jwks_uri"] as const) {
    if (!doc[k]) throw new OidcError(`The document has no ${k}`);
    checkIdpUrl(doc[k], k);
  }
  return doc;
}

export interface OidcSecrets {
  nonce: string;
  code_verifier: string;
}

export function newOidcSecrets(): OidcSecrets {
  return { nonce: b64url(randomBytes(24)), code_verifier: b64url(randomBytes(48)) };
}

export function authorizationUrl(doc: OidcDiscovery, cfg: SsoRuntime, state: string, s: OidcSecrets): string {
  const url = new URL(doc.authorization_endpoint);
  const scopes = [...new Set(["openid", "email", "profile", ...cfg.scopes])];
  url.search = new URLSearchParams({
    response_type: "code",
    client_id: cfg.client_id,
    redirect_uri: cfg.redirect_uri,
    scope: scopes.join(" "),
    state,
    nonce: s.nonce,
    code_challenge: b64url(createHash("sha256").update(s.code_verifier).digest()),
    code_challenge_method: "S256",
  }).toString();
  return url.toString();
}

const jwks = new Map<string, ReturnType<typeof createRemoteJWKSet>>();

function keySet(uri: string) {
  let set = jwks.get(uri);
  if (!set) jwks.set(uri, (set = createRemoteJWKSet(new URL(uri), { timeoutDuration: TIMEOUT_MS })));
  return set;
}

/** Reads a claim by dotted path ("realm_access.roles"). */
function claim(payload: Record<string, unknown>, path: string): unknown {
  if (path in payload) return payload[path];
  return path.split(".").reduce<unknown>((v, k) => (v && typeof v === "object" ? (v as Record<string, unknown>)[k] : undefined), payload);
}

function list(v: unknown): string[] {
  if (typeof v === "string") return v ? [v] : [];
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

export function identityFromClaims(cfg: SsoRuntime, p: JWTPayload & Record<string, unknown>): SsoIdentity {
  const text = (name: string) => {
    const v = claim(p, name);
    return typeof v === "string" ? v : "";
  };
  return {
    subject: String(p.sub ?? ""),
    email: text(cfg.email_attribute || "email").toLowerCase(),
    // Absent counts as verified: many providers only ever hand out addresses they own and do not send the claim.
    email_verified: p.email_verified !== false && p.email_verified !== "false",
    name: text(cfg.name_attribute || "name"),
    username: text(cfg.username_attribute || "preferred_username"),
    groups: list(claim(p, cfg.groups_attribute || "groups")),
    claims: p,
  };
}

/** Exchanges the code and verifies what comes back; adds the userinfo claims when the token lacks the mapped ones. */
export async function exchangeCode(doc: OidcDiscovery, cfg: SsoRuntime, code: string, s: OidcSecrets): Promise<SsoIdentity> {
  const form = new URLSearchParams({ grant_type: "authorization_code", code, redirect_uri: cfg.redirect_uri, code_verifier: s.code_verifier, client_id: cfg.client_id });
  const headers: Record<string, string> = { "content-type": "application/x-www-form-urlencoded", accept: "application/json" };
  if (cfg.client_secret) {
    const methods = doc.token_endpoint_auth_methods_supported ?? ["client_secret_basic"];
    if (methods.includes("client_secret_basic")) {
      const enc = (v: string) => encodeURIComponent(v);
      headers.authorization = `Basic ${Buffer.from(`${enc(cfg.client_id)}:${enc(cfg.client_secret)}`).toString("base64")}`;
    } else {
      form.set("client_secret", cfg.client_secret);
    }
  }
  const res = await fetch(doc.token_endpoint, { method: "POST", headers, body: form, signal: AbortSignal.timeout(TIMEOUT_MS), redirect: "error" });
  const body = (await res.json().catch(() => ({}))) as { id_token?: string; access_token?: string; error?: string; error_description?: string };
  if (!res.ok) throw new OidcError(`The token endpoint answered ${res.status}${body.error ? `: ${body.error}` : ""}`);
  if (!body.id_token) throw new OidcError("The token endpoint returned no ID token");

  const { payload } = await jwtVerify(body.id_token, keySet(doc.jwks_uri), { issuer: doc.issuer, audience: cfg.client_id, algorithms: ALGORITHMS, clockTolerance: 60 }).catch((e: unknown) => {
    throw new OidcError(`The ID token did not verify: ${e instanceof Error ? e.message : String(e)}`);
  });
  if (payload.nonce !== s.nonce) throw new OidcError("The ID token has the wrong nonce");
  if (Array.isArray(payload.aud) && payload.aud.length > 1 && payload.azp !== cfg.client_id) throw new OidcError("The ID token was issued to another client");
  if (!payload.sub) throw new OidcError("The ID token has no subject");

  let claims = payload as JWTPayload & Record<string, unknown>;
  let identity = identityFromClaims(cfg, claims);
  if ((!identity.email || !identity.groups.length) && doc.userinfo_endpoint && body.access_token) {
    const info = await fetch(doc.userinfo_endpoint, { headers: { authorization: `Bearer ${body.access_token}` }, signal: AbortSignal.timeout(TIMEOUT_MS), redirect: "error" }).catch(() => undefined);
    const extra = info?.ok ? ((await info.json().catch(() => undefined)) as Record<string, unknown> | undefined) : undefined;
    // OIDC Core 5.3.2: userinfo of another subject must not be used.
    if (extra && extra.sub === payload.sub) {
      claims = { ...extra, ...claims };
      identity = identityFromClaims(cfg, claims);
    }
  }
  return identity;
}
