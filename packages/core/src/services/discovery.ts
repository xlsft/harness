/**
 * Model discovery (admin): lists the models an upstream offers via its `/v1/models` endpoint,
 * through the SSRF guard, and returns them as candidates for the model catalog.
 */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ANTHROPIC_VERSION } from "../gateway/adapters/anthropic.ts";
import { joinUrl } from "../gateway/adapters/types.ts";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { PublicUser } from "./devices.ts";
import { loadProviderRuntime, providerForUser, redactSecrets, type ProviderRuntime } from "./providers.ts";

export interface ModelCandidate {
  /** Upstream model id, to be used as `upstream_model` of a catalog entry. */
  upstream_model: string;
  display_name: string;
  /** Present when the upstream reports it (OpenRouter `context_length`, vLLM `max_model_len`, ...). */
  context_window?: number;
  max_output_tokens?: number;
  created_at?: string;
  owned_by?: string;
  /** Catalog aliases that already point at this provider + upstream model. */
  catalog_ids: string[];
}

const MAX_PAGES = 20;

function num(v: unknown): number | undefined {
  const n = typeof v === "string" ? Number(v) : v;
  return typeof n === "number" && Number.isFinite(n) && n > 0 ? Math.floor(n) : undefined;
}

function headersFor(p: ProviderRuntime): Record<string, string> {
  const h: Record<string, string> = { accept: "application/json" };
  if (p.type === "anthropic") {
    h["anthropic-version"] = ANTHROPIC_VERSION;
    if (p.apiKey) h["x-api-key"] = p.apiKey;
  } else if (p.apiKey) {
    h.authorization = `Bearer ${p.apiKey}`;
  }
  return { ...h, ...p.headers };
}

async function fetchJson(p: ProviderRuntime, url: string): Promise<Record<string, any>> {
  let res;
  try {
    res = await guardedFetch(url, {
      method: "GET",
      headers: headersFor(p),
      allowPrivateNetwork: p.allowPrivateNetwork,
      signal: AbortSignal.timeout(20_000),
    });
  } catch (err) {
    if (err instanceof ApiError) throw err;
    throw new ApiError("upstream", redactSecrets(`Could not reach ${url}: ${(err as Error).message}`, p));
  }
  const text = await res.text();
  if (!res.ok) {
    throw new ApiError("upstream", redactSecrets(`Upstream answered ${res.status} for ${url}: ${text.slice(0, 300)}`, p), {
      upstreamStatus: res.status,
    });
  }
  try {
    return JSON.parse(text) as Record<string, any>;
  } catch {
    throw new ApiError("upstream", `Upstream returned invalid JSON for ${url}`);
  }
}

/** Maps one entry of an Anthropic or OpenAI-style model list to a candidate. */
export function toCandidate(m: Record<string, any>): Omit<ModelCandidate, "catalog_ids"> | undefined {
  const id = typeof m.id === "string" ? m.id : typeof m.name === "string" ? m.name : undefined;
  if (!id) return undefined;
  const c: Omit<ModelCandidate, "catalog_ids"> = {
    upstream_model: id,
    display_name: typeof m.display_name === "string" ? m.display_name : typeof m.name === "string" && m.name !== id ? m.name : id,
  };
  const ctx = num(m.context_window) ?? num(m.context_length) ?? num(m.max_model_len) ?? num(m.max_input_tokens) ?? num(m.top_provider?.context_length);
  if (ctx) c.context_window = ctx;
  const out = num(m.max_output_tokens) ?? num(m.max_tokens) ?? num(m.top_provider?.max_completion_tokens);
  if (out) c.max_output_tokens = out;
  if (typeof m.created_at === "string") c.created_at = m.created_at;
  else if (typeof m.created === "number") c.created_at = new Date(m.created * 1000).toISOString();
  if (typeof m.owned_by === "string") c.owned_by = m.owned_by;
  return c;
}

/** Discovery for a provider the user may change (admins: global ones; everyone: their own). */
export async function discoverModelsForUser(providerId: string, user: PublicUser) {
  const { writable } = await providerForUser(providerId, user);
  if (!writable) throw new ApiError("permission", "Only admins can change global providers");
  return discoverModels(providerId);
}

/** Discovery for a global provider (admin API) or, through discoverModelsForUser, a checked one. */
export async function discoverModels(providerId: string, globalOnly = false): Promise<{ provider_id: string; models: ModelCandidate[] }> {
  await ensureDb();
  const row = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, providerId) });
  if (!row || (globalOnly && row.ownerUserId)) throw new ApiError("not_found", `Provider ${providerId} not found`);
  const p = await loadProviderRuntime(providerId);

  const raw: Record<string, any>[] = [];
  if (p.type === "anthropic") {
    // Paginated: ?limit=&after_id=, response {data, has_more, last_id}.
    let after: string | undefined;
    for (let page = 0; page < MAX_PAGES; page++) {
      const url = new URL(joinUrl(p.baseUrl, "/v1/models"));
      url.searchParams.set("limit", "1000");
      if (after) url.searchParams.set("after_id", after);
      const j = await fetchJson(p, url.toString());
      if (Array.isArray(j.data)) raw.push(...j.data);
      if (!j.has_more || typeof j.last_id !== "string") break;
      after = j.last_id;
    }
  } else {
    // OpenAI-compatible base URLs include /v1. Ollama-style {models:[...]} is accepted too.
    const j = await fetchJson(p, joinUrl(p.baseUrl, "/models"));
    if (Array.isArray(j.data)) raw.push(...j.data);
    else if (Array.isArray(j.models)) raw.push(...j.models);
  }

  const catalog = await getDb().query.model.findMany({ where: eq(schema.model.providerId, providerId) });
  const seen = new Set<string>();
  const models: ModelCandidate[] = [];
  for (const m of raw) {
    if (!m || typeof m !== "object") continue;
    const c = toCandidate(m);
    if (!c || seen.has(c.upstream_model)) continue;
    seen.add(c.upstream_model);
    models.push({ ...c, catalog_ids: catalog.filter((e) => e.upstreamModel === c.upstream_model).map((e) => e.id) });
  }
  models.sort((a, b) => a.upstream_model.localeCompare(b.upstream_model));
  return { provider_id: providerId, models };
}
