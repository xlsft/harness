/**
 * skills.sh behind SkillSearchService. Its API takes a Vercel OIDC token and answers 401 without one, so search exists only on a
 * server given a token (SKILLS_SH_TOKEN, or VERCEL_OIDC_TOKEN on a Vercel deployment); otherwise the source is hidden. Answers are
 * cached here, so many clients cost one request, and a 429 is honoured for everyone.
 *
 * Requests follow the API's reference (https://skills.sh/docs/api, November 2026).
 */
export interface ExternalAudit {
  provider: string;
  status: string;
  summary: string;
  auditedAt: string;
  riskLevel: string;
}

export interface ExternalSkillView {
  id: string;
  name: string;
  slug: string;
  source: string;
  installs: bigint;
  url: string;
  audits: ExternalAudit[];
}

export interface SkillSnapshot {
  skill: ExternalSkillView;
  hash: string;
  files: { path: string; contents: string }[];
}

const DEFAULT_URL = "https://skills.sh";
const SEARCH_TTL_MS = 10 * 60_000;
const DETAIL_TTL_MS = 60 * 60_000;
const REQUEST_TIMEOUT_MS = 8_000;
const MAX_CACHE = 500;
// How many results get their audits fetched: enough for what a screen shows, without one search costing a request per result.
const AUDITED_RESULTS = 12;
const AUDIT_CONCURRENCY = 4;
const ID = /^[\w.@-]+(\/[\w.@-]+){1,2}$/;

export class SkillSearchError extends Error {
  constructor(
    message: string,
    readonly kind: "not_configured" | "unavailable" | "not_found" | "invalid",
  ) {
    super(message);
  }
}

function token(): string {
  return (process.env.SKILLS_SH_TOKEN || process.env.VERCEL_OIDC_TOKEN || "").trim();
}

function baseUrl(): string {
  return (process.env.SKILLS_SH_URL?.trim() || DEFAULT_URL).replace(/\/+$/, "");
}

export function skillSearchConfigured(): boolean {
  return token() !== "";
}

const cache = new Map<string, { at: number; value: unknown }>();
let waitUntil = 0;

/** Tests. */
export function resetSkillSearch(): void {
  cache.clear();
  waitUntil = 0;
}

async function cached<T>(key: string, ttl: number, load: () => Promise<T>): Promise<T> {
  const hit = cache.get(key);
  if (hit && Date.now() - hit.at < ttl) return hit.value as T;
  const value = await load();
  if (cache.size >= MAX_CACHE) cache.delete(cache.keys().next().value as string);
  cache.set(key, { at: Date.now(), value });
  return value;
}

async function get(path: string, signal?: AbortSignal): Promise<unknown> {
  const bearer = token();
  if (!bearer) throw new SkillSearchError("skill search isn't set up on this server", "not_configured");
  if (Date.now() < waitUntil) throw new SkillSearchError("skills.sh asked us to wait", "unavailable");
  const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
  let res: Response;
  try {
    res = await fetch(`${baseUrl()}/api/v1${path}`, {
      headers: { Authorization: `Bearer ${bearer}`, Accept: "application/json" },
      signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
    });
  } catch {
    throw new SkillSearchError("skills.sh can't be reached", "unavailable");
  }
  if (res.status === 404) throw new SkillSearchError("skills.sh has no such skill", "not_found");
  if (res.status === 429) {
    const after = Number(res.headers.get("retry-after"));
    waitUntil = Date.now() + (Number.isFinite(after) && after > 0 ? after : 60) * 1000;
    throw new SkillSearchError("skills.sh asked us to wait", "unavailable");
  }
  if (res.status === 401) throw new SkillSearchError("skills.sh refused this server's token", "unavailable");
  if (!res.ok) throw new SkillSearchError(`skills.sh answered ${res.status}`, "unavailable");
  try {
    return await res.json();
  } catch {
    throw new SkillSearchError("skills.sh's answer can't be read", "unavailable");
  }
}

type Json = Record<string, unknown>;
const text = (v: unknown): string => (typeof v === "string" ? v : "");

function skillOf(v: Json): ExternalSkillView {
  const installs = typeof v.installs === "number" && Number.isFinite(v.installs) && v.installs > 0 ? BigInt(Math.floor(v.installs)) : 0n;
  return { id: text(v.id), name: text(v.name) || text(v.slug), slug: text(v.slug), source: text(v.source), installs, url: text(v.url), audits: [] };
}

function checkId(id: string): void {
  if (!ID.test(id) || id.includes("..")) throw new SkillSearchError("that isn't a skill's id", "invalid");
}

async function auditsOf(id: string, signal?: AbortSignal): Promise<ExternalAudit[]> {
  checkId(id);
  return cached(`audit:${id}`, DETAIL_TTL_MS, async () => {
    const res = (await get(`/skills/audit/${id}`, signal)) as Json;
    const list = Array.isArray(res.audits) ? (res.audits as Json[]) : [];
    return list.map((a) => ({ provider: text(a.provider), status: text(a.status), summary: text(a.summary), auditedAt: text(a.auditedAt), riskLevel: text(a.riskLevel) }));
  });
}

/** Adds audits to the first results. An audit that can't be read leaves that skill without one: unknown, never a pass. */
async function withAudits(skills: ExternalSkillView[], signal?: AbortSignal): Promise<ExternalSkillView[]> {
  const out = skills.map((s) => ({ ...s }));
  let next = 0;
  const worker = async () => {
    while (next < Math.min(out.length, AUDITED_RESULTS)) {
      const s = out[next++]!;
      s.audits = await auditsOf(s.id, signal).catch(() => []);
    }
  };
  await Promise.all(Array.from({ length: AUDIT_CONCURRENCY }, worker));
  return out;
}

function clampLimit(limit: number): number {
  return limit > 0 ? Math.min(limit, 200) : 50;
}

export async function searchSkills(query: string, limit: number, signal?: AbortSignal): Promise<ExternalSkillView[]> {
  const q = query.trim();
  if (q.length < 2) throw new SkillSearchError("a search needs at least two characters", "invalid");
  const n = clampLimit(limit);
  const found = await cached(`search:${n}:${q.toLowerCase()}`, SEARCH_TTL_MS, async () => {
    const res = (await get(`/skills/search?q=${encodeURIComponent(q)}&limit=${n}`, signal)) as Json;
    return (Array.isArray(res.data) ? (res.data as Json[]) : []).map(skillOf).filter((s) => s.id);
  });
  return withAudits(found, signal);
}

export async function curatedSkills(limit: number, signal?: AbortSignal): Promise<ExternalSkillView[]> {
  const n = clampLimit(limit);
  const all = await cached("curated", DETAIL_TTL_MS, async () => {
    const res = (await get("/skills/curated", signal)) as Json;
    const owners = Array.isArray(res.data) ? (res.data as Json[]) : [];
    return owners.flatMap((o) => (Array.isArray(o.skills) ? (o.skills as Json[]) : []).map(skillOf)).filter((s) => s.id);
  });
  return withAudits([...all].sort((a, b) => (a.installs === b.installs ? a.id.localeCompare(b.id) : a.installs > b.installs ? -1 : 1)).slice(0, n), signal);
}

export async function skillSnapshot(id: string, signal?: AbortSignal): Promise<SkillSnapshot> {
  checkId(id);
  const snap = await cached(`skill:${id}`, DETAIL_TTL_MS, async () => {
    const res = (await get(`/skills/${id}`, signal)) as Json;
    const files = Array.isArray(res.files) ? (res.files as Json[]) : [];
    return { skill: skillOf(res), hash: text(res.hash), files: files.map((f) => ({ path: text(f.path), contents: text(f.contents) })) };
  });
  // Audits are worth having again: a verdict can change sooner than the files do.
  const audits = await auditsOf(id, signal).catch(() => []);
  return { ...snap, skill: { ...snap.skill, audits } };
}
