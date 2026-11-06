/**
 * SAML 2.0 service provider, on @node-saml/node-saml for everything about XML and signatures: HTTP-Redirect binding out,
 * HTTP-POST binding back. Assertions must be signed by the configured certificate and answer a request we made, so
 * identity-provider-initiated sign-in is refused and a captured response cannot be replayed: the request id is consumed
 * from sso_flow in one statement.
 */
import { SAML, ValidateInResponseTo, type CacheProvider, type Profile } from "@node-saml/node-saml";
import { eq } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { sha256 } from "../lib/ids.ts";
import type { SsoRuntime } from "./sso-config.ts";
import type { SsoIdentity } from "./sso-sign-in.ts";

const CLOCK_SKEW_MS = 120_000;
const REQUEST_TTL_MS = 10 * 60_000;

export type FlowRow = typeof schema.ssoFlow.$inferSelect;

/** Request ids live in sso_flow; consumeAsync hands the row to the caller through `taken`. */
export class FlowCache implements CacheProvider {
  taken: FlowRow | undefined;

  async saveAsync(): Promise<null> {
    // The flow row is written before redirecting (sso-sign-in.ts), with what node-saml does not know about.
    return null;
  }

  async getAsync(key: string): Promise<string | null> {
    const r = await getDb().query.ssoFlow.findFirst({ where: eq(schema.ssoFlow.idHash, sha256(key)) });
    // node-saml reads the value as the time the request was made.
    return r && r.expiresAt.getTime() > Date.now() ? r.createdAt.toISOString() : null;
  }

  async removeAsync(): Promise<string | null> {
    // node-saml calls this on expiry paths only; the row expires by itself.
    return null;
  }

  async consumeAsync(key: string): Promise<string | null> {
    const [r] = await getDb().delete(schema.ssoFlow).where(eq(schema.ssoFlow.idHash, sha256(key))).returning();
    if (!r || r.expiresAt.getTime() <= Date.now()) return null;
    this.taken = r;
    return r.createdAt.toISOString();
  }
}

export function samlClient(cfg: SsoRuntime, cache: CacheProvider = new FlowCache(), requestId?: string): SAML {
  return new SAML({
    callbackUrl: cfg.saml_acs_url,
    entryPoint: cfg.idp_sso_url,
    issuer: cfg.saml_entity_id,
    audience: cfg.saml_entity_id,
    idpCert: cfg.idp_certificate,
    idpIssuer: cfg.idp_entity_id,
    wantAssertionsSigned: true,
    wantAuthnResponseSigned: false,
    signatureAlgorithm: "sha256",
    digestAlgorithm: "sha256",
    acceptedClockSkewMs: CLOCK_SKEW_MS,
    requestIdExpirationPeriodMs: REQUEST_TTL_MS,
    validateInResponseTo: ValidateInResponseTo.always,
    cacheProvider: cache,
    disableRequestedAuthnContext: true,
    identifierFormat: null,
    ...(requestId ? { generateUniqueId: () => requestId } : {}),
  });
}

export function samlMetadata(cfg: SsoRuntime): string {
  return samlClient(cfg).generateServiceProviderMetadata(null, null);
}

function values(profile: Profile, name: string): string[] {
  const v = (profile as unknown as Record<string, unknown>)[name];
  if (typeof v === "string") return v ? [v] : [];
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

// The attribute names the usual providers send, tried in order when the admin named none.
const EMAIL = ["email", "mail", "emailAddress", "urn:oid:0.9.2342.19200300.100.1.3", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"];
const NAME = ["displayName", "name", "cn", "urn:oid:2.16.840.1.113730.3.1.241", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name"];
const USERNAME = ["username", "uid", "urn:oid:0.9.2342.19200300.100.1.1"];
const GROUPS = ["groups", "memberOf", "group", "http://schemas.microsoft.com/ws/2008/06/identity/claims/groups", "urn:oid:1.3.6.1.4.1.5923.1.5.1.1"];

function first(profile: Profile, configured: string, usual: string[]): string[] {
  for (const n of configured ? [configured] : usual) {
    const v = values(profile, n);
    if (v.length) return v;
  }
  return [];
}

export function identityFromProfile(cfg: SsoRuntime, profile: Profile): SsoIdentity {
  const emailClaim = first(profile, cfg.email_attribute, EMAIL)[0] ?? "";
  const email = (emailClaim || (profile.nameIDFormat?.endsWith(":emailAddress") ? profile.nameID : "")).toLowerCase();
  const { getAssertionXml: _a, getAssertion: _b, getSamlResponseXml: _c, ...claims } = profile;
  return {
    subject: profile.nameID,
    email,
    // Assertions are signed statements of the identity provider; SAML has no separate verification flag.
    email_verified: true,
    name: first(profile, cfg.name_attribute, NAME)[0] ?? "",
    username: first(profile, cfg.username_attribute, USERNAME)[0] ?? "",
    groups: first(profile, cfg.groups_attribute, GROUPS),
    claims,
  };
}
