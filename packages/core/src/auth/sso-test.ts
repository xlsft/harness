/** What the Test button of an identity provider checks, on the form as it is, before anything is saved. */
import { ApiError } from "../lib/errors.ts";
import { parseCertificate, validateSso, type SsoRuntime } from "./sso-config.ts";
import { discover } from "./sso-oidc.ts";
import { createTestLink } from "./sso-sign-in.ts";

export interface SsoCheck {
  name: string;
  ok: boolean;
  message: string;
}

const TIMEOUT_MS = 10_000;
const text = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function reachable(url: string): Promise<SsoCheck> {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(TIMEOUT_MS), redirect: "manual" });
    await res.body?.cancel();
    // A sign-in URL without a request often answers 400 or a redirect: the point is that something answers.
    return res.status >= 500
      ? { name: "Sign-in URL", ok: false, message: `${url} answered ${res.status}` }
      : { name: "Sign-in URL", ok: true, message: `${url} answers` };
  } catch (e) {
    return { name: "Sign-in URL", ok: false, message: `${url}: ${text(e)}` };
  }
}

async function oidcChecks(cfg: SsoRuntime): Promise<SsoCheck[]> {
  const checks: SsoCheck[] = [];
  let doc;
  try {
    doc = await discover(cfg.issuer);
    checks.push({ name: "Discovery", ok: true, message: `Read the discovery document of ${doc.issuer}` });
  } catch (e) {
    return [{ name: "Discovery", ok: false, message: text(e) }];
  }
  const types = doc.response_types_supported;
  checks.push(
    !types || types.includes("code")
      ? { name: "Code flow", ok: true, message: "The provider supports the authorization code flow" }
      : { name: "Code flow", ok: false, message: `The provider only offers ${types.join(", ")}` },
  );
  try {
    const res = await fetch(doc.jwks_uri, { signal: AbortSignal.timeout(TIMEOUT_MS), redirect: "error" });
    const keys = ((await res.json()) as { keys?: unknown[] }).keys ?? [];
    checks.push(res.ok && keys.length ? { name: "Signing keys", ok: true, message: `${keys.length} published` } : { name: "Signing keys", ok: false, message: `${doc.jwks_uri} has no keys` });
  } catch (e) {
    checks.push({ name: "Signing keys", ok: false, message: text(e) });
  }
  return checks;
}

async function samlChecks(cfg: SsoRuntime): Promise<SsoCheck[]> {
  const checks: SsoCheck[] = [await reachable(cfg.idp_sso_url)];
  try {
    const cert = parseCertificate(cfg.idp_certificate);
    const until = new Date(cert.validTo);
    checks.push(until.getTime() > Date.now() ? { name: "Certificate", ok: true, message: `Valid until ${until.toISOString().slice(0, 10)}` } : { name: "Certificate", ok: false, message: `Expired on ${until.toISOString().slice(0, 10)}` });
  } catch (e) {
    checks.push({ name: "Certificate", ok: false, message: text(e) });
  }
  return checks;
}

export async function testSsoConfig(cfg: SsoRuntime): Promise<{ ok: boolean; checks: SsoCheck[]; sign_in_test_url: string }> {
  try {
    validateSso(cfg);
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    return { ok: false, checks: [{ name: "Settings", ok: false, message: e.message }], sign_in_test_url: "" };
  }
  const checks = cfg.protocol === "oidc" ? await oidcChecks(cfg) : await samlChecks(cfg);
  const ok = checks.every((c) => c.ok);
  // Only a saved provider has callback URLs the identity provider can know about.
  return { ok, checks, sign_in_test_url: ok && cfg.id ? await createTestLink(cfg.id) : "" };
}
