/**
 * Which search provider serves a user's web searches, on two levels like the classifier:
 *   - the server default (admins): a global search provider, or the built-in SearXNG. Stored in the `setting`
 *     table under `search`; empty or stale (removed, disabled) means the built-in SearXNG;
 *   - the user's own choice (everyone): any search provider they may use (global and enabled, or their own,
 *     enabled), or the built-in. Stored under `search:user:<user id>`; empty or stale follows the default.
 * SearchService resolves it per principal, so the client only calls Search. Also the search half of
 * ProviderService.TestProvider and SearchService.ListProviders.
 */
import { and, asc, eq, isNull, or } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "../services/devices.ts";
import { BUILTIN_SEARCH_ID, findPreset, type SearchType } from "../services/presets.ts";
import { loadProviderRuntime } from "../services/providers.ts";
import { createSearchProvider } from "./adapters.ts";
import { DEFAULT_MAX_RESULTS, runSearch, searchRegistry, type ProviderStatus, type SearchInput, type SearchOutput } from "./service.ts";
import type { SearchProvider } from "./types.ts";

const DEFAULT_KEY = "search";
const userKey = (userId: string) => `search:user:${userId}`;

export interface SearchSettings {
  default_provider_id: string;
  my_provider_id: string;
  effective_provider_id: string;
  editable: boolean;
}

type Row = typeof schema.provider.$inferSelect;

async function readSetting(key: string): Promise<string> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, key) });
  const v = (r?.value as { provider_id?: unknown } | undefined)?.provider_id;
  return typeof v === "string" ? v : "";
}

async function writeSetting(key: string, providerId: string): Promise<void> {
  await ensureDb();
  const db = getDb();
  if (!providerId) {
    await db.delete(schema.setting).where(eq(schema.setting.key, key));
    return;
  }
  const now = new Date();
  await db
    .insert(schema.setting)
    .values({ key, value: { provider_id: providerId }, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: { provider_id: providerId }, updatedAt: now } });
}

/** An enabled search provider row: global ones for everyone, personal ones only for their owner (user null: global only). */
async function usableRow(id: string, user: PublicUser | null): Promise<Row | undefined> {
  if (!id || id === BUILTIN_SEARCH_ID) return undefined;
  await ensureDb();
  const r = await getDb().query.provider.findFirst({ where: and(eq(schema.provider.id, id), eq(schema.provider.kind, "search")) });
  if (!r || !r.enabled) return undefined;
  if (r.ownerUserId && r.ownerUserId !== user?.id) return undefined;
  return r;
}

/** The server default with a stale choice reset to the built-in. */
async function effectiveDefault(): Promise<string> {
  const id = await readSetting(DEFAULT_KEY);
  return id && (await usableRow(id, null)) ? id : BUILTIN_SEARCH_ID;
}

async function effectiveMine(user: PublicUser): Promise<string> {
  const id = await readSetting(userKey(user.id));
  if (id === BUILTIN_SEARCH_ID) return id;
  return id && (await usableRow(id, user)) ? id : "";
}

export async function getSearchSettings(user: PublicUser): Promise<SearchSettings> {
  const [def, mine] = await Promise.all([effectiveDefault(), effectiveMine(user)]);
  return { default_provider_id: def, my_provider_id: mine, effective_provider_id: mine || def, editable: user.role === "admin" };
}

/** Admins: the server default (a global, enabled search provider; "" or "searxng": the built-in). */
export async function setSearchDefault(user: PublicUser, providerId: string): Promise<SearchSettings> {
  if (user.role !== "admin") throw new ApiError("permission", "Admin role required");
  const id = providerId.trim();
  if (id && id !== BUILTIN_SEARCH_ID && !(await usableRow(id, null))) throw new ApiError("invalid_request", `Unknown search provider ${id}`);
  await writeSetting(DEFAULT_KEY, id === BUILTIN_SEARCH_ID ? "" : id);
  return getSearchSettings(user);
}

/** Anyone: the caller's own choice ("" follows the default). */
export async function setMySearch(user: PublicUser, providerId: string): Promise<SearchSettings> {
  const id = providerId.trim();
  if (id && id !== BUILTIN_SEARCH_ID && !(await usableRow(id, user))) throw new ApiError("invalid_request", `Unknown search provider ${id}`);
  await writeSetting(userKey(user.id), id);
  return getSearchSettings(user);
}

interface Resolved {
  provider: SearchProvider;
  defaultMax: number;
}

async function adapterOf(row: Row): Promise<Resolved> {
  const rt = await loadProviderRuntime(row.id);
  const max = Number(rt.options.max_results);
  return {
    provider: createSearchProvider({
      id: row.id,
      displayName: row.displayName || findPreset(row.presetId ?? "")?.display_name || row.id,
      type: rt.type as unknown as SearchType,
      baseUrl: rt.baseUrl,
      apiKey: rt.apiKey,
      options: rt.options,
      allowPrivateNetwork: rt.allowPrivateNetwork && !rt.ownerUserId,
    }),
    defaultMax: Number.isInteger(max) && max > 0 ? max : DEFAULT_MAX_RESULTS,
  };
}

/** The provider for one query: the requested one (it must be usable by the user), else the user's effective one. */
async function resolve(user: PublicUser, requested?: string): Promise<Resolved> {
  const id = requested?.trim() || (await getSearchSettings(user)).effective_provider_id;
  if (id === BUILTIN_SEARCH_ID) return { provider: searchRegistry().get(BUILTIN_SEARCH_ID), defaultMax: DEFAULT_MAX_RESULTS };
  const row = await usableRow(id, user);
  if (!row) throw new ApiError("invalid_request", `Unknown search provider "${id}"`);
  return adapterOf(row);
}

export async function searchForUser(user: PublicUser, input: SearchInput): Promise<SearchOutput> {
  const r = await resolve(user, input.provider);
  return runSearch(r.provider, input, r.defaultMax);
}

/** The search providers a user may use (the built-in first), with health, and the user's effective one. */
export async function searchStatusForUser(user: PublicUser, signal?: AbortSignal): Promise<{ providers: ProviderStatus[]; defaultProvider: string }> {
  await ensureDb();
  const rows = await getDb()
    .select()
    .from(schema.provider)
    .where(and(eq(schema.provider.kind, "search"), eq(schema.provider.enabled, true), or(isNull(schema.provider.ownerUserId), eq(schema.provider.ownerUserId, user.id))))
    .orderBy(asc(schema.provider.createdAt));
  const builtin = searchRegistry().get(BUILTIN_SEARCH_ID);
  const providers = await Promise.all([
    builtin.healthy(signal).then((healthy) => ({ id: builtin.id, displayName: builtin.displayName, healthy })),
    ...rows.map(async (r) => {
      const { provider } = await adapterOf(r);
      return { id: r.id, displayName: provider.displayName, healthy: await provider.healthy(signal) };
    }),
  ]);
  return { providers, defaultProvider: (await getSearchSettings(user)).effective_provider_id };
}

const TEST_QUERY = "open source web search";

/**
 * One sample query on a search provider (TestProvider). The caller checked access: the built-in, or a row the
 * user may read. Never throws: a failure is the message.
 */
export async function testSearchProvider(id: string): Promise<{ ok: boolean; message: string; model_count: number }> {
  try {
    let r: Resolved;
    if (id === BUILTIN_SEARCH_ID) r = { provider: searchRegistry().get(BUILTIN_SEARCH_ID), defaultMax: 5 };
    else {
      await ensureDb();
      const row = await getDb().query.provider.findFirst({ where: and(eq(schema.provider.id, id), eq(schema.provider.kind, "search")) });
      if (!row) throw new ApiError("not_found", `Provider ${id} not found`);
      r = await adapterOf(row);
    }
    const out = await runSearch(r.provider, { query: TEST_QUERY, maxResults: 5, signal: AbortSignal.timeout(20_000) });
    const n = out.results.length;
    return { ok: true, message: n ? `Connected, ${n} result${n === 1 ? "" : "s"} for a sample query` : "Connected, but the sample query found nothing", model_count: 0 };
  } catch (e) {
    return { ok: false, message: e instanceof Error ? e.message : String(e), model_count: 0 };
  }
}
