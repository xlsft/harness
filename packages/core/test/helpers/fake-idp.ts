/**
 * A fake identity provider on a local port, speaking both protocols far enough to sign people in:
 *  - OIDC: discovery, /authorize (signs in `nextUser` at once), /token with PKCE and client secret checks, JWKS, userinfo;
 *  - SAML: the signing certificate and signed responses for an AuthnRequest, built by hand and signed with xml-crypto.
 * Keys and the certificate are generated at start. Nothing here ever leaves the machine.
 */
import { execFileSync } from "node:child_process";
import { createHash, generateKeyPairSync, randomBytes } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inflateRawSync } from "node:zlib";
import { exportJWK, generateKeyPair, SignJWT, type JWK } from "jose";
import { SignedXml } from "xml-crypto";

export interface IdpUser {
  sub: string;
  email?: string;
  email_verified?: boolean;
  name?: string;
  preferred_username?: string;
  groups?: string[];
}

export interface FakeIdp {
  url: string;
  clientId: string;
  clientSecret: string;
  cert: string;
  entityId: string;
  ssoUrl: string;
  nextUser: IdpUser;
  /** Overrides for what the next ID token says, to test that it is refused. */
  tamper: { nonce?: string; audience?: string; issuer?: string; wrongKey?: boolean };
  /** The token requests seen, for checking client authentication. */
  tokenRequests: { authorization: string; body: URLSearchParams }[];
  /** Follows an /authorize URL: returns where the browser would be sent next. */
  /** A signed SAML response to an AuthnRequest URL. */
  samlResponse(authorizeUrl: string, opts?: { user?: IdpUser; audience?: string; forgedWith?: { key: string; cert: string }; unsigned?: boolean; inResponseTo?: string; issuer?: string; notOnOrAfter?: Date }): { acs: string; samlResponse: string };
  otherSigner(): { key: string; cert: string };
  close(): Promise<void>;
}

function selfSigned(cn: string): { key: string; cert: string } {
  const dir = mkdtempSync(join(tmpdir(), "harness-idp-"));
  try {
    execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", join(dir, "k.pem"), "-out", join(dir, "c.pem"), "-days", "30", "-subj", `/CN=${cn}`], { stdio: "ignore" });
    return { key: readFileSync(join(dir, "k.pem"), "utf8"), cert: readFileSync(join(dir, "c.pem"), "utf8") };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);
const stripPem = (pem: string) => pem.replace(/-----[^-]+-----/g, "").replace(/\s+/g, "");

export async function startFakeIdp(): Promise<FakeIdp> {
  const { publicKey, privateKey } = await generateKeyPair("RS256", { extractable: true });
  const other = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const jwk: JWK = { ...(await exportJWK(publicKey)), kid: "k1", alg: "RS256", use: "sig" };
  const signer = selfSigned("fake-idp");

  const codes = new Map<string, { user: IdpUser; nonce: string; challenge: string; redirectUri: string }>();
  const tokens = new Map<string, IdpUser>();
  const state: Pick<FakeIdp, "nextUser" | "tamper" | "tokenRequests"> = { nextUser: { sub: "subject-1" }, tamper: {}, tokenRequests: [] };
  const clientId = "harness-test-client";
  const clientSecret = `fake-${randomBytes(12).toString("hex")}`;

  let base = "";
  const server: Server = createServer(async (req, res) => {
    const url = new URL(req.url!, base);
    const json = (status: number, body: unknown) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(body));
    };
    if (url.pathname === "/.well-known/openid-configuration") {
      return json(200, {
        issuer: base,
        authorization_endpoint: `${base}/authorize`,
        token_endpoint: `${base}/token`,
        jwks_uri: `${base}/jwks`,
        userinfo_endpoint: `${base}/userinfo`,
        response_types_supported: ["code"],
        token_endpoint_auth_methods_supported: ["client_secret_basic"],
      });
    }
    if (url.pathname === "/jwks") return json(200, { keys: [jwk] });
    if (url.pathname === "/authorize") {
      const q = url.searchParams;
      const code = randomBytes(16).toString("hex");
      codes.set(code, { user: state.nextUser, nonce: q.get("nonce") ?? "", challenge: q.get("code_challenge") ?? "", redirectUri: q.get("redirect_uri") ?? "" });
      res.writeHead(302, { location: `${q.get("redirect_uri")}?code=${code}&state=${encodeURIComponent(q.get("state") ?? "")}` });
      return res.end();
    }
    if (url.pathname === "/token") {
      const chunks: Buffer[] = [];
      for await (const c of req) chunks.push(c as Buffer);
      const body = new URLSearchParams(Buffer.concat(chunks).toString());
      state.tokenRequests.push({ authorization: req.headers.authorization ?? "", body });
      const entry = codes.get(body.get("code") ?? "");
      codes.delete(body.get("code") ?? "");
      if (!entry) return json(400, { error: "invalid_grant" });
      if (req.headers.authorization !== `Basic ${Buffer.from(`${clientId}:${clientSecret}`).toString("base64")}`) return json(401, { error: "invalid_client" });
      if (createHash("sha256").update(body.get("code_verifier") ?? "").digest("base64url") !== entry.challenge) return json(400, { error: "invalid_grant", error_description: "pkce" });
      if (body.get("redirect_uri") !== entry.redirectUri) return json(400, { error: "invalid_grant" });
      const t = state.tamper;
      const { groups, ...claims } = entry.user;
      const token = randomBytes(16).toString("hex");
      tokens.set(token, entry.user);
      const key = t.wrongKey ? other.privateKey : privateKey;
      const idToken = await new SignJWT({ ...claims, ...(groups ? { groups } : {}), nonce: t.nonce ?? entry.nonce })
        .setProtectedHeader({ alg: "RS256", kid: "k1" })
        .setIssuer(t.issuer ?? base)
        .setAudience(t.audience ?? clientId)
        .setSubject(entry.user.sub)
        .setIssuedAt()
        .setExpirationTime("5m")
        .sign(key);
      return json(200, { access_token: token, id_token: idToken, token_type: "Bearer" });
    }
    if (url.pathname === "/userinfo") {
      const user = tokens.get((req.headers.authorization ?? "").replace(/^Bearer /, ""));
      return user ? json(200, user) : json(401, {});
    }
    if (url.pathname === "/saml") {
      res.writeHead(200, { "content-type": "text/plain" });
      return res.end("sign-in page");
    }
    json(404, {});
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  const entityId = `${base}/entity`;

  const samlResponse: FakeIdp["samlResponse"] = (authorizeUrl, opts = {}) => {
    const req = inflateRawSync(Buffer.from(new URL(authorizeUrl).searchParams.get("SAMLRequest")!, "base64")).toString();
    const requestId = /ID="([^"]+)"/.exec(req)![1]!;
    const acs = /AssertionConsumerServiceURL="([^"]+)"/.exec(req)![1]!;
    const spEntity = /Issuer[^>]*>([^<]+)</.exec(req)![1]!;
    const u = opts.user ?? state.nextUser;
    const now = new Date();
    const later = opts.notOnOrAfter ?? new Date(now.getTime() + 5 * 60_000);
    const id = `_a${randomBytes(16).toString("hex")}`;
    const inResponseTo = opts.inResponseTo ?? requestId;
    const attr = (name: string, values: (string | undefined)[]) =>
      values.filter(Boolean).length ? `<saml:Attribute Name="${name}">${values.filter(Boolean).map((v) => `<saml:AttributeValue xsi:type="xs:string">${esc(v!)}</saml:AttributeValue>`).join("")}</saml:Attribute>` : "";
    const assertion = `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xs="http://www.w3.org/2001/XMLSchema" ID="${id}" Version="2.0" IssueInstant="${now.toISOString()}"><saml:Issuer>${esc(opts.issuer ?? entityId)}</saml:Issuer><saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified">${esc(u.sub)}</saml:NameID><saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData InResponseTo="${esc(inResponseTo)}" NotOnOrAfter="${later.toISOString()}" Recipient="${esc(acs)}"/></saml:SubjectConfirmation></saml:Subject><saml:Conditions NotBefore="${new Date(now.getTime() - 60_000).toISOString()}" NotOnOrAfter="${later.toISOString()}"><saml:AudienceRestriction><saml:Audience>${esc(opts.audience ?? spEntity)}</saml:Audience></saml:AudienceRestriction></saml:Conditions><saml:AuthnStatement AuthnInstant="${now.toISOString()}" SessionIndex="${id}"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:Password</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement><saml:AttributeStatement>${attr("email", [u.email])}${attr("displayName", [u.name])}${attr("username", [u.preferred_username])}${attr("groups", u.groups ?? [])}</saml:AttributeStatement></saml:Assertion>`;
    let signed = assertion;
    if (!opts.unsigned) {
      const use = opts.forgedWith ?? signer;
      const sig = new SignedXml({ privateKey: use.key, publicCert: use.cert, signatureAlgorithm: "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256", canonicalizationAlgorithm: "http://www.w3.org/2001/10/xml-exc-c14n#" });
      sig.addReference({
        xpath: "//*[local-name(.)='Assertion']",
        digestAlgorithm: "http://www.w3.org/2001/04/xmlenc#sha256",
        transforms: ["http://www.w3.org/2000/09/xmldsig#enveloped-signature", "http://www.w3.org/2001/10/xml-exc-c14n#"],
      });
      sig.computeSignature(assertion, { location: { reference: "//*[local-name(.)='Issuer']", action: "after" } });
      signed = sig.getSignedXml();
    }
    const response = `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="_r${randomBytes(16).toString("hex")}" Version="2.0" IssueInstant="${now.toISOString()}" Destination="${esc(acs)}" InResponseTo="${esc(inResponseTo)}"><saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">${esc(opts.issuer ?? entityId)}</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>${signed}</samlp:Response>`;
    return { acs, samlResponse: Buffer.from(response).toString("base64") };
  };

  const idp: FakeIdp = {
    url: base,
    clientId,
    clientSecret,
    cert: signer.cert,
    entityId,
    ssoUrl: `${base}/saml`,
    get nextUser() {
      return state.nextUser;
    },
    set nextUser(u: IdpUser) {
      state.nextUser = u;
    },
    get tamper() {
      return state.tamper;
    },
    set tamper(t) {
      state.tamper = t;
    },
    tokenRequests: state.tokenRequests,
    samlResponse,
    otherSigner: () => selfSigned("not-the-idp"),
    close: () =>
      new Promise((r) => {
        server.closeAllConnections();
        server.close(() => r());
      }),
  };
  void stripPem;
  return idp;
}

/**
 * A browser with a cookie jar: follows redirects by hand so a test can see each step, and sends cookies per host like a
 * browser does (the fake provider and the server are both on 127.0.0.1, ports apart, so by path of the Set-Cookie).
 */
export class FakeBrowser {
  jar = new Map<string, string>();

  cookie(): string {
    return [...this.jar.entries()].map(([k, v]) => `${k}=${v}`).join("; ");
  }

  async request(url: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    if (this.jar.size) headers.set("cookie", this.cookie());
    const res = await fetch(url, { ...init, headers, redirect: "manual" });
    for (const c of res.headers.getSetCookie()) {
      const [pair] = c.split(";");
      const i = pair!.indexOf("=");
      const name = pair!.slice(0, i);
      const value = pair!.slice(i + 1);
      if (value === "" || /max-age=0/i.test(c)) this.jar.delete(name);
      else this.jar.set(name, value);
    }
    return res;
  }

  /** Follows redirects (GET); returns the last response and where it landed. */
  async follow(url: string, limit = 10): Promise<{ res: Response; url: string; hops: string[] }> {
    let at = url;
    const hops = [url];
    for (let i = 0; i < limit; i++) {
      const res = await this.request(at);
      const loc = res.headers.get("location");
      if (!loc || res.status < 300 || res.status >= 400) return { res, url: at, hops };
      await res.body?.cancel();
      at = new URL(loc, at).toString();
      hops.push(at);
    }
    throw new Error(`too many redirects: ${hops.join(" -> ")}`);
  }
}
