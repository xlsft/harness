/**
 * GitHub App credentials: the app JWT (RS256, signed with the app's private key) and installation access
 * tokens, cached until shortly before they expire. Installations are known from installation webhooks and
 * looked up on demand (GET /repos/{owner}/{repo}/installation).
 */
import { createSign } from "node:crypto";
import { and, eq } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { connSecrets, isGitHubDotCom, onConnectionChanged, type ConnectionRow } from "./connections.ts";
import { httpJson, num, str } from "./util.ts";

/** REST API root: https://api.github.com, or <base>/api/v3 on GitHub Enterprise Server. */
export function githubApiBase(baseUrl: string): string {
  return isGitHubDotCom(baseUrl) ? "https://api.github.com" : `${baseUrl}/api/v3`;
}

export function githubGraphqlUrl(baseUrl: string): string {
  return isGitHubDotCom(baseUrl) ? "https://api.github.com/graphql" : `${baseUrl}/api/graphql`;
}

export const GH_HEADERS = { accept: "application/vnd.github+json", "x-github-api-version": "2022-11-28" };

/** App JWT for the /app endpoints (valid 9 minutes, issued 60 s in the past for clock skew). */
export function appJwt(row: ConnectionRow): string {
  const pem = connSecrets(row).privateKeyPem;
  const iss = row.appId || row.clientId;
  if (!pem || !iss) throw new ApiError("failed_precondition", "The GitHub App connection has no app id / private key");
  const now = Math.floor(Date.now() / 1000);
  const b64 = (v: unknown) => Buffer.from(JSON.stringify(v)).toString("base64url");
  const input = `${b64({ alg: "RS256", typ: "JWT" })}.${b64({ iat: now - 60, exp: now + 540, iss })}`;
  let sig: string;
  try {
    sig = createSign("RSA-SHA256").update(input).sign(pem).toString("base64url");
  } catch (err) {
    throw new ApiError("failed_precondition", `Cannot sign the GitHub App JWT: ${err instanceof Error ? err.message : String(err)}`);
  }
  return `${input}.${sig}`;
}

export function hasAppCredentials(row: ConnectionRow): boolean {
  return !!(row.appId || row.clientId) && !!connSecrets(row).privateKeyPem;
}

// --- installations -------------------------------------------------------------------------------

export async function upsertInstallation(connectionId: string, inst: { id: number; login: string; type: string; suspended: boolean }): Promise<void> {
  const values = { accountLogin: inst.login.toLowerCase(), accountType: inst.type, suspended: inst.suspended, updatedAt: new Date() };
  await getDb()
    .insert(schema.vcsInstallation)
    .values({ connectionId, installationId: inst.id, ...values })
    .onConflictDoUpdate({ target: [schema.vcsInstallation.connectionId, schema.vcsInstallation.installationId], set: values });
}

export async function deleteInstallation(connectionId: string, installationId: number): Promise<void> {
  await getDb()
    .delete(schema.vcsInstallation)
    .where(and(eq(schema.vcsInstallation.connectionId, connectionId), eq(schema.vcsInstallation.installationId, installationId)));
  for (const k of [...tokenCache.keys()]) if (k === `${connectionId}:${installationId}`) tokenCache.delete(k);
}

export async function listInstallations(row: ConnectionRow): Promise<{ id: number; login: string }[]> {
  const out: { id: number; login: string }[] = [];
  for (let page = 1; page <= 10; page++) {
    const { data } = await httpJson<any[]>(row, `${githubApiBase(row.baseUrl)}/app/installations?per_page=100&page=${page}`, "List GitHub App installations", {
      headers: GH_HEADERS,
      authorization: `Bearer ${appJwt(row)}`,
    });
    for (const i of data ?? []) {
      const inst = { id: num(i.id), login: str(i.account?.login), type: str(i.account?.type), suspended: !!i.suspended_at };
      await upsertInstallation(row.id, inst);
      if (!inst.suspended) out.push({ id: inst.id, login: inst.login });
    }
    if (!data || data.length < 100) break;
  }
  return out;
}

/** Installation id for a repository owner: the stored installation, else asked from GitHub. */
export async function installationFor(row: ConnectionRow, owner: string, repo: string): Promise<number> {
  const stored = await getDb().query.vcsInstallation.findFirst({
    where: and(eq(schema.vcsInstallation.connectionId, row.id), eq(schema.vcsInstallation.accountLogin, owner.toLowerCase())),
  });
  if (stored && !stored.suspended) return stored.installationId;
  const { data } = await httpJson<any>(row, `${githubApiBase(row.baseUrl)}/repos/${owner}/${repo}/installation`, "Find the GitHub App installation", {
    headers: GH_HEADERS,
    authorization: `Bearer ${appJwt(row)}`,
  }).catch((err: unknown) => {
    if (err instanceof ApiError && err.type === "not_found") {
      throw new ApiError("failed_precondition", `The GitHub App is not installed on ${owner}/${repo}`);
    }
    throw err;
  });
  const id = num(data?.id);
  await upsertInstallation(row.id, { id, login: str(data?.account?.login) || owner, type: str(data?.account?.type), suspended: !!data?.suspended_at });
  return id;
}

// --- installation tokens -------------------------------------------------------------------------

const tokenCache = new Map<string, { token: string; expiresAt: number }>();
const TOKEN_MARGIN_MS = 5 * 60_000;

onConnectionChanged((id) => {
  for (const k of [...tokenCache.keys()]) if (k.startsWith(`${id}:`)) tokenCache.delete(k);
});

export async function installationToken(row: ConnectionRow, installationId: number): Promise<string> {
  const k = `${row.id}:${installationId}`;
  const hit = tokenCache.get(k);
  if (hit && hit.expiresAt - TOKEN_MARGIN_MS > Date.now()) return hit.token;
  const { data } = await httpJson<{ token: string; expires_at: string }>(
    row,
    `${githubApiBase(row.baseUrl)}/app/installations/${installationId}/access_tokens`,
    "Create a GitHub installation token",
    { method: "POST", headers: GH_HEADERS, authorization: `Bearer ${appJwt(row)}` },
  );
  const expiresAt = Date.parse(data.expires_at) || Date.now() + 3_600_000;
  tokenCache.set(k, { token: data.token, expiresAt });
  return data.token;
}

export async function installationTokenForRepo(row: ConnectionRow, owner: string, repo: string): Promise<string> {
  return installationToken(row, await installationFor(row, owner, repo));
}
