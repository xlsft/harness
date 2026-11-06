/**
 * The browser side of SSO sign-in (auth/sso-sign-in.ts), one handler for:
 *   GET  /api/sso/:id/start[?return=/device?code=...|?test=<code>]
 *   GET  /api/sso/:id/oidc/callback
 *   POST /api/sso/:id/saml/acs
 *   GET  /api/sso/:id/saml/metadata
 * Plain routes, not Better Auth's: the SAML POST comes from the identity provider's origin, which its origin check refuses.
 */
import { defineHandler, type H3Event } from "h3";
import { asApiError } from "../../lib/errors.ts";
import { clientIp } from "../../rpc/context.ts";
import { esc, resultPage } from "../result-page.ts";
import { getSsoRuntime } from "../../auth/sso-config.ts";
import { samlMetadata } from "../../auth/sso-saml.ts";
import { bindingCookieHeader, bindingFromCookie, oidcCallback, samlAcs, startSso, type Answer, type TestResult } from "../../auth/sso-sign-in.ts";

function testPage(r: TestResult): Response {
  const row = (k: string, v: string) => `<dt>${esc(k)}</dt><dd>${esc(v ?? "") || "—"}</dd>`;
  return resultPage(
    r.allowed,
    r.allowed ? "Sign-in works" : "Sign-in would be refused",
    `${r.provider} answered. Nothing was created and nobody was signed in.${r.allowed ? "" : ` ${r.reason}.`}`,
    200,
    `<dl>${row("Subject", r.identity.subject)}${row("Email", r.identity.email)}${row("Name", r.identity.name)}${row("Username", r.identity.username)}${row("Groups", r.identity.groups.join(", "))}${row("Role", r.role)}</dl>
<details><summary>Everything the provider sent</summary><pre>${esc(JSON.stringify(r.claims, null, 2))}</pre></details>`,
  );
}

function errorPage(err: unknown, status?: number): Response {
  const e = asApiError(err);
  return resultPage(false, "Sign-in failed", e.message, status ?? e.status, `<p><a href="/device">Back to sign-in</a></p>`);
}

function respond(a: Answer, extraHeaders: Record<string, string> = {}): Response {
  if (a.testResult) return testPage(a.testResult);
  return new Response(null, { status: 303, headers: { location: a.location!, "cache-control": "no-store", ...extraHeaders } });
}

const peer = (event: H3Event) => clientIp((event.req as { ip?: string }).ip, event.req.headers.get("x-forwarded-for"));

export default defineHandler(async (event) => {
  const id = decodeURIComponent((event.context.params as Record<string, string> | undefined)?.id ?? "");
  const q = event.url.searchParams;
  const action = event.url.pathname.split("/").slice(4).join("/");
  try {
    if (action === "start") {
      const r = await startSso(id, { returnTo: q.get("return") ?? "/device", binding: bindingFromCookie(event.req.headers.get("cookie")), testCode: q.get("test") ?? undefined });
      return new Response(null, { status: 303, headers: { location: r.location, "cache-control": "no-store", "set-cookie": bindingCookieHeader(r.binding) } });
    }
    if (action === "oidc/callback") {
      return respond(await oidcCallback(id, { state: q.get("state") ?? "", code: q.get("code") ?? "", error: q.get("error") ?? "", errorDescription: q.get("error_description") ?? "" }, peer(event)));
    }
    if (action === "saml/acs") {
      const form = new URLSearchParams(await event.req.text());
      return respond(await samlAcs(id, form.get("SAMLResponse") ?? "", peer(event)));
    }
    if (action === "saml/metadata") {
      const cfg = await getSsoRuntime(id);
      if (!cfg || cfg.protocol !== "saml") return errorPage(new Error("Unknown identity provider"), 404);
      return new Response(samlMetadata(cfg), { headers: { "content-type": "application/samlmetadata+xml", "cache-control": "no-store" } });
    }
    return errorPage(new Error("Not found"), 404);
  } catch (err) {
    return errorPage(err);
  }
});
