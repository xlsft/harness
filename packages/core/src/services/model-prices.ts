/**
 * What a model costs, and keeping that current. Order of authority: by hand, then the provider's own model list, then the
 * catalog (services/price-catalog.ts), then the preset. A source updates a price of its own or of a lower rank, never one
 * of a higher rank; the source goes with the price on the model row. A list or catalog change past MAX_CHANGE either way,
 * or to zero, is not applied by itself: the old price stays and the change is held on the model until someone accepts it
 * (models.ts acceptHeldPrice) or a later value makes it moot. A model without a price takes any value.
 *
 * Prices apply to requests from then on: a usage record holds the cost computed when it was written (services/gateway.ts
 * recordUsage). Plan providers stay unpriced. Nothing here runs per request or per user.
 */
import { and, eq, isNull } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { discoverModels, listsItsPrices } from "./discovery.ts";
import { priceSetByHand, type HeldPriceChange, type ModelPricing, type PriceSource } from "./models.ts";
import { catalogPriceOf, fetchCatalogSnapshot, loadCatalogSnapshot } from "./price-catalog.ts";
import { billingOf, presetOf, presetPriceOf } from "./presets.ts";

const MAX_CHANGE = 5;
const DAY_MS = 86_400_000;
/** How often a stale snapshot is looked for: fetched once a day, sooner again only after a failure. */
const CHECK_MS = 3_600_000;

const RANK: Record<PriceSource, number> = { preset: 1, catalog: 2, list: 3, hand: 4 };

export interface PriceChangeCount {
  /** Models whose price or its source changed. */
  updated: number;
  /** Models with a change newly held back. */
  held: number;
}

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err));

/** Whether two prices say the same, whatever the order of their keys or a cache price of 0 against none. */
export function samePrice(a: ModelPricing | null | undefined, b: ModelPricing | null | undefined): boolean {
  if (!a || !b) return !a && !b;
  const same = (x: Partial<ModelPricing> | undefined, y: Partial<ModelPricing> | undefined) =>
    x?.input === y?.input && x?.output === y?.output && (x?.cache_read || 0) === (y?.cache_read || 0) && (x?.cache_write || 0) === (y?.cache_write || 0);
  return same(a, b) && same(a.long_context, b.long_context) && a.long_context?.above_tokens === b.long_context?.above_tokens;
}

const tooFar = (from: number | undefined, to: number | undefined) => {
  if (from === undefined || to === undefined || from === to) return false;
  if (from === 0 || to === 0) return true;
  return Math.max(from / to, to / from) > MAX_CHANGE;
};

/**
 * Whether a change needs someone to look at it. Input and output decide, also of the long-context tier when both have one;
 * a cache price only when both state it, since one that appears only says what used to follow input.
 */
export function changeIsHeld(from: ModelPricing, to: ModelPricing): boolean {
  const parts = (x: Partial<ModelPricing>, y: Partial<ModelPricing>) =>
    tooFar(x.input, y.input) || tooFar(x.output, y.output) || tooFar(x.cache_read, y.cache_read) || tooFar(x.cache_write, y.cache_write);
  return parts(from, to) || (!!from.long_context && !!to.long_context && parts(from.long_context, to.long_context));
}

export interface PriceState {
  pricing: ModelPricing | null;
  source: PriceSource | null;
  held: HeldPriceChange | null;
}

export interface PriceOffers {
  list?: ModelPricing;
  catalog?: ModelPricing;
  preset?: ModelPricing;
}

/**
 * The state of a model after what its sources say now; `current` itself when nothing changes. By hand is the caller's to
 * check. A price with no recorded source (from before sources were) reads as the preset's when it is the preset's number.
 * Otherwise nobody can tell who set it, so it ranks as a list price: its own list may update it, a catalog does not.
 */
export function decidePrice(current: PriceState, offers: PriceOffers, now: Date): PriceState {
  const offer: { source: Exclude<PriceSource, "hand">; pricing: ModelPricing } | undefined = offers.list
    ? { source: "list", pricing: offers.list }
    : offers.catalog
      ? { source: "catalog", pricing: offers.catalog }
      : offers.preset && { source: "preset", pricing: offers.preset };
  if (!offer) return current;
  const had = current.pricing;
  const source: PriceSource = current.source ?? (!had || samePrice(had, offers.preset) ? "preset" : "list");
  if (had && RANK[offer.source] < RANK[source]) return current;
  if (had && samePrice(had, offer.pricing)) {
    return current.source === offer.source && !current.held ? current : { pricing: had, source: offer.source, held: null };
  }
  // A preset is checked by hand and ships with the server: no guard.
  if (had && offer.source !== "preset" && changeIsHeld(had, offer.pricing)) {
    if (current.held?.source === offer.source && samePrice(current.held.proposed, offer.pricing)) return current;
    return { ...current, held: { proposed: offer.pricing, source: offer.source, held_at: now.toISOString() } };
  }
  return { pricing: offer.pricing, source: offer.source, held: null };
}

/**
 * Brings models in line with their sources (one provider, or all of them), with what is known here and asking nobody:
 * the stored catalog snapshot, the preset, and `listed`, a list the caller just read for that one provider.
 */
export async function applyModelPrices(opts: { providerId?: string; listed?: ReadonlyMap<string, ModelPricing> } = {}): Promise<PriceChangeCount> {
  await ensureDb();
  const snapshot = await loadCatalogSnapshot();
  const rows = await getDb()
    .select()
    .from(schema.model)
    .innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId))
    .where(opts.providerId ? eq(schema.model.providerId, opts.providerId) : undefined);
  const count: PriceChangeCount = { updated: 0, held: 0 };
  const now = new Date();
  for (const { model, provider } of rows) {
    if (priceSetByHand(model.capabilities) || billingOf(provider) === "plan") continue;
    const current: PriceState = {
      pricing: (model.pricing as ModelPricing | null) ?? null,
      source: model.pricingSource as PriceSource | null,
      held: (model.priceChangeHeld as HeldPriceChange | null) ?? null,
    };
    const next = decidePrice(
      current,
      {
        list: opts.listed?.get(model.upstreamModel),
        catalog: catalogPriceOf(snapshot, provider, model.upstreamModel),
        preset: presetPriceOf(provider, model.upstreamModel),
      },
      now,
    );
    if (next === current) continue;
    const moved = next.pricing !== current.pricing || next.source !== current.source;
    if (moved) count.updated++;
    if (next.held && next.held !== current.held) {
      count.held++;
      console.warn(`[harness] model price change held for ${model.id}: ${JSON.stringify(current.pricing)} -> ${JSON.stringify(next.held.proposed)} (${next.held.source})`);
    }
    await getDb()
      .update(schema.model)
      .set({ pricing: next.pricing, pricingSource: next.source, priceChangeHeld: next.held, ...(moved && { pricingUpdatedAt: now }) })
      .where(eq(schema.model.id, model.id));
  }
  return count;
}

/**
 * Asks the global providers that have a priced model list for it (discovery applies what it says). Personal providers
 * wait for their owner to open their list: a background job or an admin's click has no business using someone's key.
 */
async function refreshProviderLists(): Promise<PriceChangeCount> {
  const rows = await getDb()
    .select()
    .from(schema.model)
    .innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId))
    .where(and(isNull(schema.provider.ownerUserId), eq(schema.provider.enabled, true)));
  const ids = new Set<string>();
  for (const { model, provider } of rows) {
    if (model.pricingSource === "list" || listsItsPrices(presetOf(provider)?.id)) ids.add(provider.id);
  }
  const count: PriceChangeCount = { updated: 0, held: 0 };
  for (const id of ids) {
    try {
      const { prices } = await discoverModels(id);
      count.updated += prices.updated;
      count.held += prices.held;
    } catch (err) {
      console.error(`[harness] could not refresh the model list of ${id}: ${errorText(err)}`);
    }
  }
  return count;
}

export interface PriceRefresh extends PriceChangeCount {
  ok: boolean;
  message: string;
}

let listsRefreshedAt = 0;
let running: Promise<PriceRefresh> | undefined;

async function refresh(): Promise<PriceRefresh> {
  let message: string;
  let ok = false;
  if (!config().modelPricesEnabled) {
    message = "The price catalog is off on this server";
  } else {
    try {
      await fetchCatalogSnapshot();
      ok = true;
      message = "Prices refreshed";
    } catch (err) {
      message = `Could not fetch the price catalog, keeping the last one: ${errorText(err)}`;
      console.error(`[harness] ${message}`);
    }
  }
  const catalog = await applyModelPrices();
  listsRefreshedAt = Date.now();
  const lists = await refreshProviderLists();
  return { ok, message, updated: catalog.updated + lists.updated, held: catalog.held + lists.held };
}

/** Fetches the catalog now, applies it, and reads the priced lists of global providers. One at a time; a second call joins the first. */
export function refreshModelPrices(): Promise<PriceRefresh> {
  running ??= refresh().finally(() => {
    running = undefined;
  });
  return running;
}

/** One look at the clock: a snapshot a day old (or none) is fetched again; a failing catalog does not stop the lists' daily turn. */
export async function refreshIfStale(now = Date.now()): Promise<void> {
  const fetchedAt = Date.parse((await loadCatalogSnapshot())?.fetched_at ?? "");
  if (config().modelPricesEnabled && (Number.isNaN(fetchedAt) || now - fetchedAt >= DAY_MS)) {
    await refreshModelPrices();
  } else if (now - listsRefreshedAt >= DAY_MS) {
    listsRefreshedAt = now;
    await refreshProviderLists();
  }
}

let timer: ReturnType<typeof setInterval> | undefined;

/** At start and then hourly, in the background: an unreachable catalog delays nothing. Not while it is off. */
export function startModelPriceRefresh(): void {
  if (timer || !config().modelPricesEnabled) return;
  const run = () => void refreshIfStale().catch((err: unknown) => console.error("[harness] model price refresh failed", err));
  run();
  timer = setInterval(run, CHECK_MS);
  timer.unref();
}
