/**
 * Model discovery: lists the models an upstream offers via its `/v1/models` endpoint, through the SSRF
 * guard, and returns them as candidates for the model catalog. A preset without a model list (and every
 * classifier provider) answers the preset's curated models instead. Also the LLM half of TestProvider. A list that
 * gives prices is the provider's own word on them: services/model-prices.ts applies what it says.
 */
import { authorize, CloudAuthError } from "../gateway/cloud/credentials.ts";
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ANTHROPIC_VERSION } from "../gateway/adapters/anthropic.ts";
import { joinUrl } from "../gateway/adapters/types.ts";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { PublicUser } from "./devices.ts";
import { applyModelPrices, type PriceChangeCount } from "./model-prices.ts";
import { backfilledCapabilities, DEFAULT_MAX_OUTPUT_TOKENS, EFFORT_LEVELS, type Effort, type ListedCapabilities, type ModelPricing } from "./models.ts";
import { catalogPriceOf, loadCatalogSnapshot } from "./price-catalog.ts";
import { presetOf, presetPriceOf } from "./presets.ts";
import { loadProviderRuntime, providerForUser, redactSecrets, type ProviderRuntime } from "./providers.ts";

export interface ModelCandidate {
  /** Upstream model id, to be used as `upstream_model` of a catalog entry. */
  upstream_model: string;
  display_name: string;
  /** Present when the upstream reports it (OpenRouter `context_length`, vLLM `max_model_len`, ...). */
  context_window?: number;
  max_output_tokens?: number;
  /** USD per 1M tokens, when the upstream's list says (OpenRouter `pricing`, per token there) or else the catalog or its preset does. */
  pricing?: ModelPricing;
  /** What the upstream's list says the model can do (see listedCapabilities). */
  capabilities?: ListedCapabilities;
  created_at?: string;
  owned_by?: string;
  /** Catalog aliases that already point at this provider + upstream model. */
  catalog_ids: string[];
}

const MAX_PAGES = 20;

/** The presets whose service gives prices in a list, so that the daily refresh is worth asking it (model-prices.ts). */
const PRICED_LIST_PRESETS = ["openrouter", "xai"];

export function listsItsPrices(presetId: string | undefined): boolean {
  return !!presetId && PRICED_LIST_PRESETS.includes(presetId);
}

/** The prices of each provider's last model list, by upstream model. */
const listedPrices = new Map<string, Map<string, ModelPricing>>();

/** The price the provider's last model list gave for a model (a user adds models from that list). */
export function listedPriceOf(providerId: string, upstreamModel: string): ModelPricing | undefined {
  return listedPrices.get(providerId)?.get(upstreamModel);
}

/** The capabilities of each provider's last model list, by upstream model. */
const listedCaps = new Map<string, Map<string, ListedCapabilities>>();

/** The capabilities the provider's last model list gave for a model (a user adds models from that list). */
export function listedCapabilitiesOf(providerId: string, upstreamModel: string): ListedCapabilities | undefined {
  return listedCaps.get(providerId)?.get(upstreamModel);
}

/** The context window and output limit of each provider's last model list, by upstream model. */
const listedLimits = new Map<string, Map<string, ListedLimits>>();

export interface ListedLimits {
  context_window?: number;
  max_output_tokens?: number;
}

/** The limits the provider's last model list gave for a model (a user adds models from that list). */
export function listedLimitsOf(providerId: string, upstreamModel: string): ListedLimits | undefined {
  return listedLimits.get(providerId)?.get(upstreamModel);
}

function num(v: unknown): number | undefined {
  const n = typeof v === "string" ? Number(v) : v;
  return typeof n === "number" && Number.isFinite(n) && n > 0 ? Math.floor(n) : undefined;
}

function headersFor(p: ProviderRuntime): Record<string, string> {
  const h: Record<string, string> = { accept: "application/json" };
  if (p.type === "anthropic" || p.type === "bedrock-invoke" || p.type === "vertex-anthropic") {
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
    const plain = headersFor(p);
    res = await guardedFetch(url, {
      method: "GET",
      headers: p.cloud ? await authorize(p.cloud, { method: "GET", url, headers: plain, body: "" }) : plain,
      allowPrivateNetwork: p.allowPrivateNetwork,
      signal: AbortSignal.timeout(20_000),
    });
  } catch (err) {
    if (err instanceof ApiError) throw err;
    if (err instanceof CloudAuthError) throw new ApiError("upstream", redactSecrets(err.message, p), { upstreamStatus: err.retryable ? 503 : 401 });
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

/**
 * Prices from xAI's GET /v1/language-models, by model id and alias. Its prices are USD cents per 100 million tokens
 * (docs.x.ai/developers/rest-api-reference/inference/models), so 20000 is $2 per 1M. Long-context prices apply "at or above"
 * a threshold (0: no tier), where `above_tokens` is exclusive; a 0 long-context price is the standard one.
 */
export function xaiListedPrices(j: Record<string, any>): Map<string, ModelPricing> {
  const prices = new Map<string, ModelPricing>();
  const perMillion = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v > 0 ? Math.round(v * 100) / 1e6 : undefined);
  for (const m of Array.isArray(j.models) ? j.models : []) {
    if (!m || typeof m.id !== "string") continue;
    const input = perMillion(m.prompt_text_token_price);
    const output = perMillion(m.completion_text_token_price);
    if (input === undefined || output === undefined) continue;
    const pricing: ModelPricing = { input, output };
    const cached = perMillion(m.cached_prompt_text_token_price);
    if (cached !== undefined) pricing.cache_read = cached;
    const threshold = typeof m.long_context_threshold === "number" && m.long_context_threshold > 0 ? m.long_context_threshold : 0;
    if (threshold) {
      const cachedLong = perMillion(m.cached_prompt_text_token_price_long_context) ?? cached;
      pricing.long_context = {
        above_tokens: threshold - 1,
        input: perMillion(m.prompt_text_token_price_long_context) ?? input,
        output: perMillion(m.completion_text_token_price_long_context) ?? output,
        ...(cachedLong !== undefined && { cache_read: cachedLong }),
      };
    }
    for (const id of [m.id, ...(Array.isArray(m.aliases) ? m.aliases : [])]) if (typeof id === "string") prices.set(id, pricing);
  }
  return prices;
}

/** The levels to offer when a list says a model reasons but not at which levels. */
const BASIC_EFFORTS: Effort[] = ["low", "medium", "high"];

/**
 * What one entry of a model list says the model can do; only what it says yes to. Two formats:
 *   - OpenRouter style: `reasoning` (`supported_efforts`, highest first, null for no allowlist; `default_effort`), else
 *     `supported_parameters`, and `architecture.input_modalities`. Levels harness has no name for ("minimal",
 *     "none") are left out, and so is a default that is not one of the levels kept.
 *   - Anthropic: `capabilities`, an object of `{supported: boolean}` per capability and per effort level.
 */
export function listedCapabilities(m: Record<string, any>): ListedCapabilities | undefined {
  const c: ListedCapabilities = {};
  const params: unknown[] = Array.isArray(m.supported_parameters) ? m.supported_parameters : [];
  const modalities: unknown[] = Array.isArray(m.architecture?.input_modalities) ? m.architecture.input_modalities : [];
  if (m.reasoning && typeof m.reasoning === "object") {
    c.thinking = true;
    const levels: unknown[] | undefined = Array.isArray(m.reasoning.supported_efforts) ? m.reasoning.supported_efforts : undefined;
    c.effort = levels ? EFFORT_LEVELS.filter((l) => levels.includes(l)) : BASIC_EFFORTS;
  } else if (["reasoning", "include_reasoning", "reasoning_effort"].some((p) => params.includes(p))) {
    c.thinking = true;
    c.effort = BASIC_EFFORTS;
  }
  if (modalities.includes("image")) c.images = true;
  if (modalities.includes("file")) c.pdf = true;

  const caps = m.capabilities;
  if (caps && typeof caps === "object" && !Array.isArray(caps)) {
    if (caps.thinking?.supported === true) c.thinking = true;
    if (caps.effort?.supported === true) c.effort = EFFORT_LEVELS.filter((l) => caps.effort[l]?.supported === true);
    if (caps.image_input?.supported === true) c.images = true;
    if (caps.pdf_input?.supported === true) c.pdf = true;
  }
  if (!c.effort?.length) delete c.effort;
  const listedDefault = m.reasoning?.default_effort;
  if (c.effort?.includes(listedDefault)) c.default_effort = listedDefault;
  return Object.keys(c).length ? c : undefined;
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
  const capabilities = listedCapabilities(m);
  if (capabilities) c.capabilities = capabilities;
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
export async function discoverModels(providerId: string, globalOnly = false): Promise<{ provider_id: string; models: ModelCandidate[]; prices: PriceChangeCount }> {
  await ensureDb();
  const row = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, providerId) });
  if (!row || row.kind === "speech" || (globalOnly && row.ownerUserId)) throw new ApiError("not_found", `Provider ${providerId} not found`);
  if (row.kind === "search") throw new ApiError("invalid_request", "Search providers serve no models");
  const p = await loadProviderRuntime(providerId);
  const preset = presetOf(row);
  // Before the service is asked: a list that fails or gives no prices must not keep what else is known from a model.
  const known = await applyModelPrices({ providerId });

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

  // xAI's model list is curated here, but it has a list of prices. Its failing costs prices, not the discovery.
  const listed = new Map<string, ModelPricing>();
  if (preset?.id === "xai" && p.apiKey) {
    try {
      for (const [id, pricing] of xaiListedPrices(await fetchJson(p, joinUrl(p.baseUrl, "/language-models")))) listed.set(id, pricing);
    } catch (err) {
      console.error(`[harness] could not read the prices of ${providerId}: ${(err as Error).message}`);
    }
  }

  const catalog = await getDb().query.model.findMany({ where: eq(schema.model.providerId, providerId) });
  const snapshot = await loadCatalogSnapshot();
  const seen = new Set<string>();
  const models: ModelCandidate[] = [];
  for (const m of raw) {
    if (!m || typeof m !== "object") continue;
    const c = toCandidate(m);
    if (!c || seen.has(c.upstream_model)) continue;
    seen.add(c.upstream_model);
    if (c.pricing) listed.set(c.upstream_model, c.pricing);
    const pricing = listed.get(c.upstream_model) ?? catalogPriceOf(snapshot, row, c.upstream_model) ?? presetPriceOf(row, c.upstream_model);
    models.push({ ...c, ...(pricing && { pricing }), catalog_ids: catalog.filter((e) => e.upstreamModel === c.upstream_model).map((e) => e.id) });
  }
  models.sort((a, b) => a.upstream_model.localeCompare(b.upstream_model));
  // What the list says about prices and capabilities: for a model added from it next, and for the ones already added.
  // A list that says nothing changes nothing.
  listedPrices.set(providerId, listed);
  const caps = new Map(models.flatMap((m) => (m.capabilities ? [[m.upstream_model, m.capabilities] as const] : [])));
  listedCaps.set(providerId, caps);
  const limits = new Map(
    models.flatMap((m) =>
      m.context_window || m.max_output_tokens ? [[m.upstream_model, { context_window: m.context_window, max_output_tokens: m.max_output_tokens }] as const] : [],
    ),
  );
  listedLimits.set(providerId, limits);
  for (const e of catalog) {
    const capabilities = backfilledCapabilities(e.capabilities, caps.get(e.upstreamModel));
    if (capabilities) await getDb().update(schema.model).set({ capabilities }).where(eq(schema.model.id, e.id));
    // A model added before its limit was taken from the list kept the default; the list's value replaces only that.
    const out = limits.get(e.upstreamModel)?.max_output_tokens;
    if (out && e.maxOutputTokens === DEFAULT_MAX_OUTPUT_TOKENS && out !== e.maxOutputTokens) {
      await getDb().update(schema.model).set({ maxOutputTokens: out }).where(eq(schema.model.id, e.id));
    }
  }
  const applied = await applyModelPrices({ providerId, listed });
  return { provider_id: providerId, models, prices: { updated: known.updated + applied.updated, held: known.held + applied.held } };
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
  // A cloud platform may list no models, or refuse the list to a role allowed to run them: the credentials are what can be
  // told for sure here, and the first request tells the rest.
  if (p.cloud) {
    try {
      await authorize(p.cloud, { method: "GET", url, headers: headersFor(p), body: "" });
    } catch (e) {
      return { ok: false, message: redactSecrets((e as Error).message, p), model_count: 0 };
    }
    try {
      const j = await fetchJson(p, url);
      const list = Array.isArray(j.data) ? j.data : [];
      return { ok: true, message: list.length ? `Connected, ${list.length} models offered` : "Connected", model_count: list.length };
    } catch (e) {
      const status = e instanceof ApiError ? e.upstreamStatus : undefined;
      if (status === 401) return { ok: false, message: `The credentials were rejected (HTTP ${status})`, model_count: 0 };
      if (status === 403 || status === 404 || status === 405) return { ok: true, message: "Credentials obtained; whether they may run a model shows on the first request", model_count: 0 };
      return { ok: false, message: e instanceof Error ? e.message : String(e), model_count: 0 };
    }
  }
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
