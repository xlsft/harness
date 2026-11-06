/**
 * Native cloud credentials of the gateway's providers (Amazon Bedrock, Google Vertex AI, Microsoft Foundry): how a request
 * to one gets its signature or token, on the server only.
 *
 * Modes:
 *   - stored: a secret the user gave, kept encrypted like any provider key (`keys`, `bearer`, `service_account`, `api_key`,
 *     `entra`);
 *   - `ambient`: what the server's own environment holds (instance role, application default credentials, managed
 *     identity). Spends the server's cloud account, so only a global provider may use it: services/providers.ts refuses it
 *     on a personal one, and `authorize` refuses it again here.
 *
 * Nothing a stored secret names is followed: a service account file's `token_uri` is ignored, or a user could point the
 * server's token request at its metadata service. Metadata and token endpoints are constants or come from the server's own
 * environment. Values that land in a host name are validated by services/presets.ts.
 */
import { createHash, createSign } from "node:crypto";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { signV4, type AwsCredentials } from "./sigv4.ts";

export type CloudKind = "aws" | "gcp" | "azure";

export interface CloudAuth {
  kind: CloudKind;
  mode: string;
  /** Plain options of the provider: region, access_key_id, tenant_id, client_id... */
  options: Record<string, string>;
  /** The stored secret, if the mode has one. */
  secret?: string;
  /** SigV4 service name (aws); the API route (azure: "anthropic" or "openai"). */
  service?: string;
  /** Null for a global provider. */
  ownerUserId: string | null;
}

/** What credential lookups touch: replaced by the tests, so no test ever reaches a real metadata service or file. */
export interface CloudEnv {
  env: Record<string, string | undefined>;
  fetch: typeof fetch;
  readFile(path: string): string;
  now(): number;
  home: string;
  /** The Google and Microsoft token endpoints, and the two metadata services. */
  googleTokenUrl: string;
  azureLoginUrl: string;
  awsImdsUrl: string;
  awsContainerUrl: string;
  azureImdsUrl: string;
}

export function defaultCloudEnv(): CloudEnv {
  return {
    env: process.env,
    fetch: globalThis.fetch,
    readFile: (p) => readFileSync(p, "utf8"),
    now: () => Date.now(),
    home: homedir(),
    googleTokenUrl: "https://oauth2.googleapis.com/token",
    azureLoginUrl: "https://login.microsoftonline.com",
    awsImdsUrl: process.env.AWS_EC2_METADATA_SERVICE_ENDPOINT?.replace(/\/+$/, "") || "http://169.254.169.254",
    awsContainerUrl: "http://169.254.170.2",
    azureImdsUrl: "http://169.254.169.254",
  };
}

/** A failure to obtain credentials: no request was sent. `retryable` when it may be the network, not the configuration. */
export class CloudAuthError extends Error {
  constructor(
    message: string,
    readonly retryable = false,
  ) {
    super(message);
    this.name = "CloudAuthError";
  }
}

const TIMEOUT_MS = 10_000;
/** A token this close to its expiry is fetched anew. */
const REFRESH_MARGIN_MS = 120_000;
const GCP_SCOPE = "https://www.googleapis.com/auth/cloud-platform";
const AZURE_RESOURCE = "https://cognitiveservices.azure.com";

interface Token {
  value: string;
  expiresAt: number;
}

async function request(env: CloudEnv, what: string, url: string, init: RequestInit): Promise<Response> {
  try {
    return await env.fetch(url, { ...init, signal: AbortSignal.timeout(TIMEOUT_MS), redirect: "error" });
  } catch (err) {
    throw new CloudAuthError(`Could not reach ${what}: ${(err as Error).message}`, true);
  }
}

/** Reads a JSON answer. Upstream bodies are never quoted in the error: a token endpoint may echo what it was sent. */
async function json(what: string, res: Response): Promise<Record<string, any>> {
  if (!res.ok) throw new CloudAuthError(`${what} answered HTTP ${res.status}`, res.status >= 500);
  try {
    return (await res.json()) as Record<string, any>;
  } catch {
    throw new CloudAuthError(`${what} did not answer JSON`, true);
  }
}

// --- AWS -------------------------------------------------------------------------------------------------------------

function awsFromEnv(env: CloudEnv): AwsCredentials | undefined {
  const { AWS_ACCESS_KEY_ID: id, AWS_SECRET_ACCESS_KEY: secret, AWS_SESSION_TOKEN: token } = env.env;
  return id && secret ? { accessKeyId: id, secretAccessKey: secret, ...(token && { sessionToken: token }) } : undefined;
}

function awsFromProfile(env: CloudEnv): AwsCredentials | undefined {
  const path = env.env.AWS_SHARED_CREDENTIALS_FILE || join(env.home, ".aws", "credentials");
  let text: string;
  try {
    text = env.readFile(path);
  } catch {
    return undefined;
  }
  const profile = env.env.AWS_PROFILE || "default";
  let inProfile = false;
  const values: Record<string, string> = {};
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#") || line.startsWith(";")) continue;
    const section = /^\[(.+)\]$/.exec(line);
    if (section) {
      inProfile = section[1]!.trim() === profile;
      continue;
    }
    const eq = line.indexOf("=");
    if (inProfile && eq > 0) values[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  const { aws_access_key_id: id, aws_secret_access_key: secret, aws_session_token: token } = values;
  return id && secret ? { accessKeyId: id, secretAccessKey: secret, ...(token && { sessionToken: token }) } : undefined;
}

function awsParse(j: Record<string, any>): AwsCredentials {
  const id = j.AccessKeyId as string | undefined;
  const secret = j.SecretAccessKey as string | undefined;
  if (!id || !secret) throw new CloudAuthError("The AWS credential source gave no keys");
  const exp = j.Expiration;
  return {
    accessKeyId: id,
    secretAccessKey: secret,
    ...((j.Token ?? j.SessionToken) && { sessionToken: (j.Token ?? j.SessionToken) as string }),
    // STS's JSON gives seconds since the epoch, the metadata services an ISO time.
    ...(exp !== undefined && { expiresAt: typeof exp === "number" ? exp * 1000 : Date.parse(String(exp)) }),
  };
}

async function awsWebIdentity(env: CloudEnv, region: string): Promise<AwsCredentials | undefined> {
  const { AWS_WEB_IDENTITY_TOKEN_FILE: file, AWS_ROLE_ARN: role, AWS_ROLE_SESSION_NAME: name } = env.env;
  if (!file || !role) return undefined;
  let token: string;
  try {
    token = env.readFile(file).trim();
  } catch {
    throw new CloudAuthError("Could not read the file in AWS_WEB_IDENTITY_TOKEN_FILE");
  }
  const endpoint = env.env.AWS_ENDPOINT_URL_STS?.replace(/\/+$/, "") || `https://sts.${region}.amazonaws.com`;
  const form = new URLSearchParams({
    Action: "AssumeRoleWithWebIdentity",
    Version: "2011-06-15",
    RoleArn: role,
    RoleSessionName: name || "harness-gateway",
    WebIdentityToken: token,
  });
  const res = await request(env, "AWS STS", `${endpoint}/`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json" },
    body: form,
  });
  const j = await json("AWS STS", res);
  return awsParse(j.AssumeRoleWithWebIdentityResponse?.AssumeRoleWithWebIdentityResult?.Credentials ?? {});
}

async function awsContainer(env: CloudEnv): Promise<AwsCredentials | undefined> {
  const { AWS_CONTAINER_CREDENTIALS_RELATIVE_URI: rel, AWS_CONTAINER_CREDENTIALS_FULL_URI: full, AWS_CONTAINER_AUTHORIZATION_TOKEN: auth } = env.env;
  if (!rel && !full) return undefined;
  const url = rel ? `${env.awsContainerUrl}${rel}` : full!;
  const res = await request(env, "the container credentials service", url, { headers: auth ? { authorization: auth } : {} });
  return awsParse(await json("The container credentials service", res));
}

/** IMDSv2: a session token first, then the role's credentials. */
async function awsInstance(env: CloudEnv): Promise<AwsCredentials | undefined> {
  if (env.env.AWS_EC2_METADATA_DISABLED === "true") return undefined;
  let session: Response;
  try {
    session = await env.fetch(`${env.awsImdsUrl}/latest/api/token`, {
      method: "PUT",
      headers: { "x-aws-ec2-metadata-token-ttl-seconds": "21600" },
      signal: AbortSignal.timeout(TIMEOUT_MS),
      redirect: "error",
    });
  } catch {
    return undefined; // Not on an instance.
  }
  if (!session.ok) return undefined;
  const headers = { "x-aws-ec2-metadata-token": await session.text() };
  const base = `${env.awsImdsUrl}/latest/meta-data/iam/security-credentials/`;
  const roles = await request(env, "the instance metadata service", base, { headers });
  if (!roles.ok) return undefined;
  const role = (await roles.text()).split("\n")[0]?.trim();
  if (!role) return undefined;
  const res = await request(env, "the instance metadata service", base + encodeURIComponent(role), { headers });
  return awsParse(await json("The instance metadata service", res));
}

/** The default chain: environment, web identity, shared profile, container, instance role. */
async function awsAmbient(env: CloudEnv, region: string): Promise<AwsCredentials> {
  const found = awsFromEnv(env) ?? (await awsWebIdentity(env, region)) ?? awsFromProfile(env) ?? (await awsContainer(env)) ?? (await awsInstance(env));
  if (!found) throw new CloudAuthError("No AWS credentials on this server: no environment keys, web identity, profile, container or instance role");
  return found;
}

// --- Google ----------------------------------------------------------------------------------------------------------

const b64url = (b: string | Buffer) => Buffer.from(b).toString("base64url");

function tokenOf(env: CloudEnv, j: Record<string, any>): Token {
  const value = j.access_token as string | undefined;
  if (!value) throw new CloudAuthError("The token service gave no access token");
  const seconds = Number(j.expires_in ?? 3600);
  return { value, expiresAt: env.now() + (Number.isFinite(seconds) ? seconds : 3600) * 1000 };
}

async function gcpServiceAccount(env: CloudEnv, file: Record<string, any>): Promise<Token> {
  const email = file.client_email as string | undefined;
  const pem = file.private_key as string | undefined;
  if (!email || !pem) throw new CloudAuthError("The service account file has no client_email or private_key");
  const iat = Math.floor(env.now() / 1000);
  const head = b64url(JSON.stringify({ alg: "RS256", typ: "JWT", ...(file.private_key_id && { kid: file.private_key_id }) }));
  const claims = b64url(JSON.stringify({ iss: email, scope: GCP_SCOPE, aud: env.googleTokenUrl, iat, exp: iat + 3600 }));
  let signature: Buffer;
  try {
    signature = createSign("RSA-SHA256").update(`${head}.${claims}`).sign(pem);
  } catch {
    throw new CloudAuthError("The service account's private key could not be read");
  }
  const res = await request(env, "Google's token service", env.googleTokenUrl, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ grant_type: "urn:ietf:params:oauth:grant-type:jwt-bearer", assertion: `${head}.${claims}.${b64url(signature)}` }),
  });
  return tokenOf(env, await json("Google's token service", res));
}

async function gcpAuthorizedUser(env: CloudEnv, file: Record<string, any>): Promise<Token> {
  const res = await request(env, "Google's token service", env.googleTokenUrl, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "refresh_token",
      client_id: String(file.client_id ?? ""),
      client_secret: String(file.client_secret ?? ""),
      refresh_token: String(file.refresh_token ?? ""),
    }),
  });
  return tokenOf(env, await json("Google's token service", res));
}

/** Application default credentials: the named file, gcloud's file, then the metadata server. */
async function gcpAmbient(env: CloudEnv): Promise<Token> {
  const named = env.env.GOOGLE_APPLICATION_CREDENTIALS;
  const wellKnown = join(env.env.CLOUDSDK_CONFIG || join(env.home, ".config", "gcloud"), "application_default_credentials.json");
  let text: string | undefined;
  try {
    text = env.readFile(named || wellKnown);
  } catch {
    if (named) throw new CloudAuthError("Could not read the file in GOOGLE_APPLICATION_CREDENTIALS");
  }
  if (text !== undefined) {
    let file: Record<string, any>;
    try {
      file = JSON.parse(text);
    } catch {
      throw new CloudAuthError("The application default credentials file is not JSON");
    }
    if (file.type === "service_account") return gcpServiceAccount(env, file);
    if (file.type === "authorized_user") return gcpAuthorizedUser(env, file);
    throw new CloudAuthError(`Application default credentials of type "${String(file.type)}" are not supported`);
  }
  const host = env.env.GCE_METADATA_HOST || "metadata.google.internal";
  const res = await request(env, "the GCE metadata server", `http://${host}/computeMetadata/v1/instance/service-accounts/default/token?scopes=${encodeURIComponent(GCP_SCOPE)}`, {
    headers: { "metadata-flavor": "Google" },
  });
  return tokenOf(env, await json("The GCE metadata server", res));
}

// --- Azure -----------------------------------------------------------------------------------------------------------

async function azureClientSecret(env: CloudEnv, auth: CloudAuth): Promise<Token> {
  const { tenant_id: tenant, client_id: client } = auth.options;
  if (!tenant || !client || !auth.secret) throw new CloudAuthError("Microsoft Entra sign-in needs a tenant id, a client id and a client secret");
  const res = await request(env, "Microsoft Entra", `${env.azureLoginUrl}/${encodeURIComponent(tenant)}/oauth2/v2.0/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ grant_type: "client_credentials", client_id: client, client_secret: auth.secret, scope: `${AZURE_RESOURCE}/.default` }),
  });
  return tokenOf(env, await json("Microsoft Entra", res));
}

/** Managed identity: App Service's endpoint when the server has one, else the instance metadata service. */
async function azureManagedIdentity(env: CloudEnv, auth: CloudAuth): Promise<Token> {
  const client = auth.options.client_id;
  const params = new URLSearchParams({ resource: AZURE_RESOURCE, ...(client && { client_id: client }) });
  const { IDENTITY_ENDPOINT: endpoint, IDENTITY_HEADER: header } = env.env;
  const res = endpoint
    ? await request(env, "the managed identity endpoint", `${endpoint}?api-version=2019-08-01&${params}`, { headers: { "x-identity-header": header ?? "" } })
    : await request(env, "the instance metadata service", `${env.azureImdsUrl}/metadata/identity/oauth2/token?api-version=2018-02-01&${params}`, { headers: { metadata: "true" } });
  const j = await json("The managed identity endpoint", res);
  const value = j.access_token as string | undefined;
  if (!value) throw new CloudAuthError("The managed identity endpoint gave no access token");
  // An absolute `expires_on` (seconds), where client credentials give a relative `expires_in`.
  const on = Number(j.expires_on);
  return { value, expiresAt: Number.isFinite(on) && on > 0 ? on * 1000 : env.now() + 3_000_000 };
}

// --- The request -----------------------------------------------------------------------------------------------------

/** Tokens and temporary keys by what they came from. */
const cache = new Map<string, Promise<Token | AwsCredentials>>();

function cacheKey(auth: CloudAuth): string {
  return createHash("sha256")
    .update(JSON.stringify([auth.kind, auth.mode, auth.options, auth.secret ?? "", auth.ownerUserId ?? ""]))
    .digest("hex");
}

async function cached<T extends Token | AwsCredentials>(auth: CloudAuth, env: CloudEnv, load: () => Promise<T>): Promise<T> {
  const key = cacheKey(auth);
  const hit = cache.get(key);
  if (hit) {
    try {
      const c = (await hit) as T;
      if (c.expiresAt === undefined || c.expiresAt - env.now() > REFRESH_MARGIN_MS) return c;
    } catch {
      // A failed lookup is not remembered: fall through to a new one.
    }
    if (cache.get(key) === hit) cache.delete(key);
  }
  const running = cache.get(key) as Promise<T> | undefined;
  if (running) return running;
  const p = load();
  cache.set(key, p);
  p.catch(() => {
    if (cache.get(key) === p) cache.delete(key);
  });
  return p;
}

/** Forgets a provider's token after the upstream refused it, so the next request gets a new one. */
export function forgetCloudToken(auth: CloudAuth): void {
  cache.delete(cacheKey(auth));
}

/** For the tests. */
export function clearCloudTokens(): void {
  cache.clear();
}

export interface AuthorizeRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

async function awsHeaders(auth: CloudAuth, env: CloudEnv, req: AuthorizeRequest): Promise<Record<string, string>> {
  const region = auth.options.region;
  if (!region) throw new CloudAuthError("The provider has no AWS region");
  if (auth.mode === "bearer") {
    if (!auth.secret) throw new CloudAuthError("The provider has no Bedrock API key");
    // Mantle reads the key from x-api-key, the runtime endpoint from Authorization.
    return { ...req.headers, ...(auth.service === "bedrock-mantle" ? { "x-api-key": auth.secret } : { authorization: `Bearer ${auth.secret}` }) };
  }
  let credentials: AwsCredentials;
  if (auth.mode === "keys") {
    if (!auth.options.access_key_id || !auth.secret) throw new CloudAuthError("The provider needs an access key id and a secret access key");
    credentials = { accessKeyId: auth.options.access_key_id, secretAccessKey: auth.secret };
  } else {
    credentials = await cached(auth, env, () => awsAmbient(env, region));
  }
  return signV4({ method: req.method, url: req.url, headers: req.headers, body: req.body, region, service: auth.service ?? "bedrock", credentials, now: new Date(env.now()) });
}

async function gcpHeaders(auth: CloudAuth, env: CloudEnv, req: AuthorizeRequest): Promise<Record<string, string>> {
  const token = await cached(auth, env, () => {
    if (auth.mode === "ambient") return gcpAmbient(env);
    let file: Record<string, any>;
    try {
      file = JSON.parse(auth.secret ?? "");
    } catch {
      throw new CloudAuthError("The stored service account file is not JSON");
    }
    return gcpServiceAccount(env, file);
  });
  return { ...req.headers, authorization: `Bearer ${token.value}` };
}

async function azureHeaders(auth: CloudAuth, env: CloudEnv, req: AuthorizeRequest): Promise<Record<string, string>> {
  if (auth.mode === "api_key") {
    if (!auth.secret) throw new CloudAuthError("The provider has no API key");
    // The Anthropic route takes x-api-key, the OpenAI routes api-key.
    return { ...req.headers, [auth.service === "anthropic" ? "x-api-key" : "api-key"]: auth.secret };
  }
  const token = await cached(auth, env, () => (auth.mode === "ambient" ? azureManagedIdentity(env, auth) : azureClientSecret(env, auth)));
  return { ...req.headers, authorization: `Bearer ${token.value}` };
}

/** The headers of a request to a cloud provider, with its signature or token. Throws CloudAuthError; nothing was sent then. */
export async function authorize(auth: CloudAuth, req: AuthorizeRequest, env: CloudEnv = defaultCloudEnv()): Promise<Record<string, string>> {
  if (auth.mode === "ambient" && auth.ownerUserId) throw new CloudAuthError("A personal provider cannot use the server's own cloud identity");
  switch (auth.kind) {
    case "aws":
      return awsHeaders(auth, env, req);
    case "gcp":
      return gcpHeaders(auth, env, req);
    case "azure":
      return azureHeaders(auth, env, req);
  }
}
