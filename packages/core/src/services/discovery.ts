/**
 * Model discovery: lists the models an upstream offers via its `/v1/models` endpoint, through the SSRF
 * guard, and returns them as candidates for the model catalog. A preset without a model list (and every
 * classifier provider) answers the preset's curated models instead. Also the LLM half of TestProvider.
 */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ANTHROPIC_VERSION } from "../gateway/adapters/anthropic.ts";
import { joinUrl } from "../gateway/adapters/types.ts";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { PublicUser } from "./devices.ts";
import type { ModelPricing } from "./models.ts";
import { presetOf } from "./presets.ts";
import { loadProviderRuntime, providerForUser, redactSecrets, type ProviderRuntime } from "./providers.ts";

export interface ModelCandidate {
  /** Upstream model id, to be used as `upstream_model` of a catalog entry. */
  upstream_model: string;
  display_name: string;
  /** Present when the upstream reports it (OpenRouter `context_length`, vLLM `max_model_len`, ...). */
  context_window?: number;
  max_output_tokens?: number;
  /** USD per 1M tokens, when the upstream's list says (OpenRouter `pricing`, per token there). */
  pricing?: ModelPricing;
  created_at?: string;
  owned_by?: string;
  /** Catalog aliases that already point at this provider + upstream model. */
  catalog_ids: string[];
}

const MAX_PAGES = 20;

/** The prices of each provider's last model list, by upstream model. */
const listedPrices = new Map<string, Map<string, ModelPricing>>();

/** The price the provider's last model list gave for a model (a user adds models from that list). */
export function listedPriceOf(providerId: string, upstreamModel: string): ModelPricing | undefined {
  return listedPrices.get(providerId)?.get(upstreamModel);
}

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

/** A model list's per-token prices as USD per 1M tokens; undefined without both, or for a router's "-1" (varies). */
function listedPricing(p: unknown): ModelPricing | undefined {
  if (!p || typeof p !== "object") return undefined;
  const o = p as Record<string, unknown>;
  const perMillion = (v: unknown) => {
    const n = typeof v === "string" ? Number(v) : v;
    if (typeof n !== "number" || !Number.isFinite(n) || n < 0 || v === "") return undefined;
    return Math.round(n * 1e6 * 1e6) / 1e6;
  };
  const input = perMillion(o.prompt);
  const output = perMillion(o.completion);
  if (input === undefined || output === undefined) return undefined;
  const out: ModelPricing = { input, output };
  const read = perMillion(o.input_cache_read);
  const write = perMillion(o.input_cache_write);
  if (read) out.cache_read = read;
  if (write) out.cache_write = write;
  return out;
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
  const pricing = listedPricing(m.pricing);
  if (pricing) c.pricing = pricing;
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
  if (!row || row.kind === "speech" || (globalOnly && row.ownerUserId)) throw new ApiError("not_found", `Provider ${providerId} not found`);
  if (row.kind === "search") throw new ApiError("invalid_request", "Search providers serve no models");
  const p = await loadProviderRuntime(providerId);
  const preset = presetOf(row);

  const raw: Record<string, any>[] = [];
  if (row.kind === "classifier" || (preset && !preset.discovers_models)) {
    for (const m of preset?.default_models ?? []) {
      raw.push({ id: m.upstream_model, display_name: m.display_name, context_window: m.context_window, max_output_tokens: m.max_output_tokens });
    }
  } else if (p.type === "anthropic") {
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
  // What the list says about prices: for a model added from it next, and for the ones already added without any.
  const prices = new Map(models.flatMap((m) => (m.pricing ? [[m.upstream_model, m.pricing] as const] : [])));
  listedPrices.set(providerId, prices);
  for (const e of catalog) {
    const pricing = prices.get(e.upstreamModel);
    if (!e.pricing && pricing) await getDb().update(schema.model).set({ pricing }).where(eq(schema.model.id, e.id));
  }
  return { provider_id: providerId, models };
}

export interface TestResult {
  ok: boolean;
  message: string;
  model_count: number;
}

/**
 * Connection test of an LLM provider: asks for its model list with its key. 2xx: connected; 401 / 403: the
 * key is rejected; 404 / 405: reachable, but the service has no model list (its key is checked on first use).
 */
export async function testLlmProvider(providerId: string): Promise<TestResult> {
  const p = await loadProviderRuntime(providerId);
  const url = p.type === "anthropic" ? joinUrl(p.baseUrl, "/v1/models") : joinUrl(p.baseUrl, "/models");
  try {
    const j = await fetchJson(p, url);
    const list = Array.isArray(j.data) ? j.data : Array.isArray(j.models) ? j.models : [];
    return { ok: true, message: list.length ? `Connected, ${list.length} models offered` : "Connected", model_count: list.length };
  } catch (e) {
    const status = e instanceof ApiError ? e.upstreamStatus : undefined;
    if (status === 401 || status === 403) return { ok: false, message: `The key was rejected (HTTP ${status})`, model_count: 0 };
    if (status === 404 || status === 405) {
      return { ok: true, message: "Reachable; the service lists no models, so the key is checked on first use", model_count: 0 };
    }
    return { ok: false, message: e instanceof Error ? e.message : String(e), model_count: 0 };
  }
}
