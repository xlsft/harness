/**
 * The model price catalog: a public document of what models cost (models.dev by default, HARNESS_MODEL_PRICES_URL to
 * change it, HARNESS_MODEL_PRICES=off for none), fetched through the SSRF guard. Of its 6 MB only the prices of the
 * catalog providers some preset maps to are kept (presets.ts PRICE_CATALOG_PROVIDERS), in the `setting` table, so a
 * restart or an offline server keeps working. services/model-prices.ts decides what they do to a model.
 *
 * Document: { "<provider>": { "models": { "<model>": { "cost": { input, output, cache_read, cache_write, tiers: [...] } } } } },
 * USD per 1M tokens. A price has to be right as a whole or it is left out: a model is stored as null when it has no price
 * (cost missing, a 0 input or output) or one harness cannot bill (several tiers, a tier other than by context length,
 * reasoning tokens priced apart from output).
 */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config, DEFAULT_MODEL_PRICES_URL } from "../env.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { ModelPricing } from "./models.ts";
import { catalogProviderOf, PRICE_CATALOG_PROVIDERS } from "./presets.ts";

const SETTING_KEY = "model_price_catalog";
/** The document is about 6 MB; anything far past that is not it. */
const MAX_DOCUMENT_BYTES = 64 * 1024 * 1024;

/** By model; null: the catalog knows the model and gives no price harness can use. */
type CatalogModels = Record<string, ModelPricing | null>;

export interface CatalogSnapshot {
  /** RFC 3339. */
  fetched_at: string;
  /** By catalog provider. */
  prices: Record<string, CatalogModels>;
}

export interface ParsedCatalog {
  prices: Record<string, CatalogModels>;
  /** Models with a cost that was not used, and why. */
  left_out: { provider: string; model: string; reason: string }[];
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isPositive = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v) && v > 0;

/** One set of prices; undefined without a positive input and output, or with a cache price that is no number. A 0 cache price says nothing. */
function pricesOf(o: Record<string, unknown>): Omit<ModelPricing, "long_context"> | undefined {
  if (!isPositive(o.input) || !isPositive(o.output)) return undefined;
  const out: Omit<ModelPricing, "long_context"> = { input: o.input, output: o.output };
  for (const k of ["cache_read", "cache_write"] as const) {
    if (o[k] === undefined || o[k] === 0) continue;
    if (!isPositive(o[k])) return undefined;
    out[k] = o[k];
  }
  return out;
}

/** A model's `cost` as a price, or why it is none. */
export function parseCost(cost: unknown): { pricing: ModelPricing } | { reason: string } {
  if (!isObject(cost)) return { reason: "no cost" };
  const base = pricesOf(cost);
  if (!base) return { reason: "no price" };
  // computeCost bills reasoning tokens as output.
  if (cost.reasoning !== undefined && cost.reasoning !== cost.output) return { reason: "reasoning priced apart from output" };
  const tiers = cost.tiers;
  if (tiers === undefined || (Array.isArray(tiers) && !tiers.length)) {
    // Only a summary of `tiers`, never seen without them; without them it cannot say where it starts.
    return cost.context_over_200k === undefined ? { pricing: base } : { reason: "context_over_200k without tiers" };
  }
  if (!Array.isArray(tiers)) return { reason: "tiers is not a list" };
  if (tiers.length > 1) return { reason: `${tiers.length} tiers` };
  const t: unknown = tiers[0];
  if (!isObject(t) || !isObject(t.tier)) return { reason: "malformed tier" };
  if (t.tier.type !== "context") return { reason: `tier type ${String(t.tier.type)}` };
  const prices = pricesOf(t);
  if (!isPositive(t.tier.size) || !prices) return { reason: "malformed tier" };
  return { pricing: { ...base, long_context: { above_tokens: t.tier.size, ...prices } } };
}

/** The prices a document holds for the mapped catalog providers. Throws on a document that holds none: it is not the catalog. */
export function parseCatalog(doc: unknown): ParsedCatalog {
  if (!isObject(doc)) throw new Error("not an object");
  const parsed: ParsedCatalog = { prices: {}, left_out: [] };
  let priced = 0;
  for (const provider of new Set(Object.values(PRICE_CATALOG_PROVIDERS))) {
    const entry = doc[provider];
    if (!isObject(entry) || !isObject(entry.models)) continue;
    const models: CatalogModels = {};
    for (const [model, m] of Object.entries(entry.models)) {
      const r = parseCost(isObject(m) ? m.cost : undefined);
      if ("pricing" in r) priced++;
      else if (r.reason !== "no cost") parsed.left_out.push({ provider, model, reason: r.reason });
      models[model] = "pricing" in r ? r.pricing : null;
    }
    parsed.prices[provider] = models;
  }
  if (!priced) throw new Error("no prices for any known provider");
  return parsed;
}

const DATED = /-20\d{2}(?:(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])|-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01]))$/;

/** A model's price: its id exactly, else the one model that is the same but for a dated suffix (-20250929, -2025-09-29). */
export function matchCatalogModel(models: CatalogModels, upstreamModel: string): ModelPricing | undefined {
  if (Object.hasOwn(models, upstreamModel)) return models[upstreamModel] ?? undefined;
  const base = upstreamModel.replace(DATED, "");
  const same = Object.keys(models).filter((id) => id.replace(DATED, "") === base);
  return same.length === 1 ? (models[same[0]!] ?? undefined) : undefined;
}

/** The stored snapshot, off or not. */
async function storedSnapshot(): Promise<CatalogSnapshot | undefined> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, SETTING_KEY) });
  return r?.value as CatalogSnapshot | undefined;
}

/** The snapshot prices come from; none while the catalog is off. */
export async function loadCatalogSnapshot(): Promise<CatalogSnapshot | undefined> {
  return config().modelPricesEnabled ? storedSnapshot() : undefined;
}

export async function catalogStatus() {
  return { enabled: config().modelPricesEnabled, updated_at: (await loadCatalogSnapshot())?.fetched_at ?? "" };
}

/** What the snapshot says a model of a stored provider costs; undefined for a provider no preset maps to it. */
export function catalogPriceOf(
  snapshot: CatalogSnapshot | undefined,
  row: { presetId: string | null; kind: string; type: string; baseUrl: string },
  upstreamModel: string,
): ModelPricing | undefined {
  const provider = snapshot && catalogProviderOf(row);
  const models = provider ? snapshot.prices[provider] : undefined;
  return models && matchCatalogModel(models, upstreamModel);
}

async function readDocument(url: string): Promise<unknown> {
  const res = await guardedFetch(url, {
    method: "GET",
    headers: { accept: "application/json" },
    // Set by whoever runs the server, unlike a provider's URL: a mirror on their own network is the point.
    allowPrivateNetwork: url !== DEFAULT_MODEL_PRICES_URL,
    signal: AbortSignal.timeout(60_000),
  });
  if (!res.ok) {
    await res.body?.cancel();
    throw new Error(`answered ${res.status}`);
  }
  const chunks: Uint8Array[] = [];
  let size = 0;
  for await (const chunk of res.body ?? []) {
    size += chunk.length;
    if (size > MAX_DOCUMENT_BYTES) throw new Error("document too large");
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

/**
 * Fetches the catalog and stores its prices. Throws on any failure, a bad document included, with the stored snapshot
 * untouched. A provider that the new document lacks keeps its last prices.
 */
export async function fetchCatalogSnapshot(): Promise<CatalogSnapshot> {
  const { prices, left_out } = parseCatalog(await readDocument(config().modelPricesUrl));
  // A model with a cost of 0 is nothing to report; one with a price harness cannot bill is.
  const unbillable = left_out.filter((m) => m.reason !== "no price");
  if (unbillable.length) console.log(`[harness] model price catalog: left out ${unbillable.map((m) => `${m.provider}/${m.model}: ${m.reason}`).join("; ")}`);
  const snapshot: CatalogSnapshot = { fetched_at: new Date().toISOString(), prices: { ...(await storedSnapshot())?.prices, ...prices } };
  await getDb()
    .insert(schema.setting)
    .values({ key: SETTING_KEY, value: snapshot, updatedAt: new Date() })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: snapshot, updatedAt: new Date() } });
  return snapshot;
}
