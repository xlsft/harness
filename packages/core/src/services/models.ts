/**
 * Model catalog, default slots and capability-negotiation memory.
 *
 * A model belongs to the scope of its provider (services/providers.ts):
 *   - global models (global provider): admin-chosen aliases, offered to every user the model's `roles` allow;
 *   - personal models (personal provider): offered only to the provider's owner. Their id is always
 *     `<provider id>/<upstream model>`; personal provider ids are random and carry a reserved prefix, so a
 *     personal id can neither clash with a global alias nor with another user's model.
 * A user's catalog is the global models first, then their own; slots point at global models only (a user
 * without any global model gets their first own model as the default).
 */
import { and, asc, eq, isNull, or } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "./devices.ts";
import { presetOf } from "./presets.ts";
import { PERSONAL_PREFIX, providerForUser } from "./providers.ts";

export const EFFORT_LEVELS = ["low", "medium", "high", "xhigh", "max"] as const;
export type Effort = (typeof EFFORT_LEVELS)[number];

export interface ModelCapabilities {
  thinking: boolean;
  effort: Effort[];
  images: boolean;
  pdf: boolean;
  parallel_tool_calls: boolean;
  prompt_cache: boolean;
}

/** What a provider's model list can say about a model. A list only ever turns these on. */
const LISTED_CAPS = ["thinking", "effort", "images", "pdf"] as const;
export type ListedCapabilities = Partial<Pick<ModelCapabilities, (typeof LISTED_CAPS)[number]>>;

/** As stored: `set_by_hand` names the capabilities someone set, which a model list leaves alone even when empty. */
type StoredCapabilities = Partial<ModelCapabilities> & { set_by_hand?: string[] };

/** USD per 1M tokens. */
export interface ModelPricing {
  input: number;
  output: number;
  cache_read?: number;
  cache_write?: number;
}

export interface ModelEntry {
  id: string;
  displayName: string;
  providerId: string;
  upstreamModel: string;
  contextWindow: number;
  maxOutputTokens: number;
  capabilities: ModelCapabilities;
  /** The capabilities someone set by hand. */
  setByHand?: string[];
  pricing: ModelPricing | null;
  /** For showing (shownAs): the provider's name and the model's id as a person reads it. */
  providerName?: string;
  displayId?: string;
  fallbacks: string[];
  extraBody: Record<string, unknown> | null;
  roles: string[] | null;
  enabled: boolean;
  /** Owner of the model's provider; null for a global model. */
  ownerUserId: string | null;
}

/**
 * `title`: model of session titles; falls back to `small`. `classifier`: model of the auto-mode decision
 * classifier (src/decision/llm.ts); falls back to `small`.
 */
export const SLOT_NAMES = ["main", "small", "plan", "title", "classifier"] as const;
/** The slots the settings screens set (ProviderService.SetSlots); the classifier has its own call. */
export const ROLE_SLOT_NAMES = ["main", "small", "plan", "title"] as const;
export type Slots = Partial<Record<(typeof SLOT_NAMES)[number], string>>;

const DEFAULT_CAPS: ModelCapabilities = {
  thinking: false,
  effort: [],
  images: false,
  pdf: false,
  parallel_tool_calls: true,
  prompt_cache: false,
};

type Row = typeof schema.model.$inferSelect;

/** What the UI shows for a model's provider and id: a personal provider's generated `u-...` id means nothing to anyone. */
function shownAs(r: Row, provider: typeof schema.provider.$inferSelect) {
  const preset = presetOf(provider);
  const providerName = provider.displayName || preset?.display_name || provider.id;
  const slug = preset?.id || providerName.toLowerCase().replace(/[^a-z0-9]+/g, "-");
  return { providerName, displayId: provider.ownerUserId ? `${slug}/${r.upstreamModel}` : r.id };
}

function toEntry(r: Row, ownerUserId: string | null): ModelEntry {
  const { set_by_hand, ...capabilities } = (r.capabilities ?? {}) as StoredCapabilities;
  return {
    ownerUserId,
    id: r.id,
    displayName: r.displayName,
    providerId: r.providerId,
    upstreamModel: r.upstreamModel,
    contextWindow: r.contextWindow,
    maxOutputTokens: r.maxOutputTokens,
    capabilities: { ...DEFAULT_CAPS, ...capabilities },
    setByHand: set_by_hand,
    pricing: (r.pricing as ModelPricing | null) ?? null,
    fallbacks: (r.fallbacks as string[]) ?? [],
    extraBody: (r.extraBody as Record<string, unknown> | null) ?? null,
    roles: (r.roles as string[] | null) ?? null,
    enabled: r.enabled,
  };
}

/** Shape of GET /api/llm/models entries (docs/protocol/gateway.md). */
export function catalogEntry(m: ModelEntry) {
  const out: Record<string, unknown> = {
    id: m.id,
    display_name: m.displayName,
    provider: m.providerId,
    provider_name: m.providerName || m.providerId,
    display_id: m.displayId || m.id,
    context_window: m.contextWindow,
    max_output_tokens: m.maxOutputTokens,
    capabilities: m.capabilities,
  };
  if (m.pricing) out.pricing = m.pricing;
  return out;
}

/** Admin view: catalog entry plus server-only fields. */
export function adminEntry(m: ModelEntry) {
  return {
    ...catalogEntry(m),
    provider_id: m.providerId,
    upstream_model: m.upstreamModel,
    fallbacks: m.fallbacks,
    extra_body: m.extraBody,
    roles: m.roles,
    enabled: m.enabled,
  };
}

/** Personal models: only their owner. Global models: the model's roles. */
function canSee(m: ModelEntry, user: PublicUser): boolean {
  if (!m.enabled) return false;
  if (m.ownerUserId) return m.ownerUserId === user.id;
  return !m.roles || m.roles.includes(user.role);
}

// --- Client-facing ---------------------------------------------------------------

export async function listModelsForUser(user: PublicUser) {
  await ensureDb();
  const rows = await getDb()
    .select()
    .from(schema.model)
    .innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId))
    .where(and(eq(schema.provider.enabled, true), or(isNull(schema.provider.ownerUserId), eq(schema.provider.ownerUserId, user.id))))
    .orderBy(asc(schema.model.sortOrder), asc(schema.model.createdAt));
  const entries = rows.map((r) => ({ ...toEntry(r.model, r.provider.ownerUserId), ...shownAs(r.model, r.provider) })).filter((m) => canSee(m, user));
  const visible = [...entries.filter((m) => !m.ownerUserId), ...entries.filter((m) => m.ownerUserId)];
  const ids = new Set(visible.map((m) => m.id));
  const slots = await getSlots();
  const effective: Slots = {};
  for (const name of SLOT_NAMES) {
    const v = slots[name];
    if (v && ids.has(v)) effective[name] = v;
    else if (name === "title" && effective.small) effective.title = effective.small;
    else if (visible[0]) effective[name] = visible[0].id;
  }
  return { models: visible.map(catalogEntry), slots: effective };
}

/** Resolves a model alias for a request, enforcing visibility (another user's model is "not available"). */
export async function resolveModelForUser(id: string, user: PublicUser): Promise<ModelEntry> {
  const m = await findModel(id);
  if (!m || !canSee(m, user)) throw new ApiError("not_found", `Model ${id} is not available`);
  return m;
}

/** Any model, global or personal (no visibility check). */
export async function findModel(id: string): Promise<ModelEntry | undefined> {
  await ensureDb();
  const [r] = await getDb()
    .select()
    .from(schema.model)
    .innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId))
    .where(eq(schema.model.id, id))
    .limit(1);
  return r ? toEntry(r.model, r.provider.ownerUserId) : undefined;
}

/** A global model; personal models are invisible to server-wide settings (slots, classifier). */
export async function findGlobalModel(id: string): Promise<ModelEntry | undefined> {
  const m = await findModel(id);
  return m && !m.ownerUserId ? m : undefined;
}

// --- Admin CRUD --------------------------------------------------------------------

export interface ModelInput {
  id?: string;
  display_name?: string;
  provider_id?: string;
  upstream_model?: string;
  context_window?: number;
  max_output_tokens?: number;
  capabilities?: Partial<ModelCapabilities>;
  pricing?: ModelPricing | null;
  fallbacks?: string[];
  extra_body?: Record<string, unknown> | null;
  roles?: string[] | null;
  enabled?: boolean;
  sort_order?: number;
}

const ID_RE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;

function validateCaps(c: unknown): Partial<ModelCapabilities> {
  if (c === undefined) return {};
  if (typeof c !== "object" || c === null) throw new ApiError("invalid_request", "capabilities must be an object");
  const caps = c as Record<string, unknown>;
  const out: Partial<ModelCapabilities> = {};
  for (const k of ["thinking", "images", "pdf", "parallel_tool_calls", "prompt_cache"] as const) {
    if (caps[k] !== undefined) {
      if (typeof caps[k] !== "boolean") throw new ApiError("invalid_request", `capabilities.${k} must be boolean`);
      out[k] = caps[k] as boolean;
    }
  }
  if (caps.effort !== undefined) {
    if (!Array.isArray(caps.effort) || caps.effort.some((e) => !EFFORT_LEVELS.includes(e))) {
      throw new ApiError("invalid_request", `capabilities.effort must be a subset of ${EFFORT_LEVELS.join(", ")}`);
    }
    out.effort = EFFORT_LEVELS.filter((l) => (caps.effort as string[]).includes(l));
  }
  return out;
}

/** `caps` with the capabilities given by hand on top of the ones already recorded as such. */
function markSetByHand(caps: Partial<ModelCapabilities>, given: Partial<ModelCapabilities>, before: string[] = []): StoredCapabilities {
  const set = [...new Set([...before, ...LISTED_CAPS.filter((k) => given[k] !== undefined)])];
  return set.length ? { ...caps, set_by_hand: set } : caps;
}

/**
 * What a model list adds to a model already in the catalog: what it reports and the model has at its empty default,
 * unless someone set that by hand. Undefined when nothing changes.
 */
export function backfilledCapabilities(stored: unknown, listed: ListedCapabilities | undefined): StoredCapabilities | undefined {
  if (!listed) return undefined;
  const caps = (stored ?? {}) as StoredCapabilities;
  const added: ListedCapabilities = {};
  if (listed.thinking && !caps.thinking && !caps.set_by_hand?.includes("thinking")) added.thinking = true;
  if (listed.effort?.length && !caps.effort?.length && !caps.set_by_hand?.includes("effort")) added.effort = listed.effort;
  if (listed.images && !caps.images && !caps.set_by_hand?.includes("images")) added.images = true;
  if (listed.pdf && !caps.pdf && !caps.set_by_hand?.includes("pdf")) added.pdf = true;
  return Object.keys(added).length ? { ...caps, ...added } : undefined;
}

function validatePricing(p: unknown): ModelPricing | null {
  if (p === null || p === undefined) return null;
  const o = p as Record<string, unknown>;
  for (const k of ["input", "output", "cache_read", "cache_write"]) {
    if (o[k] !== undefined && (typeof o[k] !== "number" || (o[k] as number) < 0)) {
      throw new ApiError("invalid_request", `pricing.${k} must be a non-negative number`);
    }
  }
  if (typeof o.input !== "number" || typeof o.output !== "number") {
    throw new ApiError("invalid_request", "pricing.input and pricing.output are required");
  }
  return o as unknown as ModelPricing;
}

function positiveInt(v: unknown, name: string): number {
  if (typeof v !== "number" || !Number.isInteger(v) || v <= 0) throw new ApiError("invalid_request", `${name} must be a positive integer`);
  return v;
}

/** The admin API manages global models only: the provider must be a global one. */
async function assertGlobalProvider(id: string) {
  const p = await getDb().query.provider.findFirst({ where: and(eq(schema.provider.id, id), isNull(schema.provider.ownerUserId)) });
  if (!p) throw new ApiError("invalid_request", `Unknown provider ${id}`);
}

async function globalModel(id: string): Promise<ModelEntry> {
  const m = await findGlobalModel(id);
  if (!m) throw new ApiError("not_found", `Model ${id} not found`);
  return m;
}

/** Global models (admin API). */
export async function listModelsAdmin() {
  await ensureDb();
  const rows = await getDb()
    .select()
    .from(schema.model)
    .innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId))
    .where(isNull(schema.provider.ownerUserId))
    .orderBy(asc(schema.model.sortOrder), asc(schema.model.createdAt));
  return rows.map((r) => adminEntry(toEntry(r.model, null)));
}

export async function getModelAdmin(id: string) {
  return adminEntry(await globalModel(id));
}

export async function createModel(input: ModelInput) {
  await ensureDb();
  if (!input.id || !ID_RE.test(input.id)) throw new ApiError("invalid_request", "id is required ([A-Za-z0-9._:/-])");
  if (input.id.startsWith(PERSONAL_PREFIX)) throw new ApiError("invalid_request", `Model ids starting with "${PERSONAL_PREFIX}" are reserved for personal models`);
  if (!input.provider_id) throw new ApiError("invalid_request", "provider_id is required");
  if (!input.upstream_model) throw new ApiError("invalid_request", "upstream_model is required");
  await assertGlobalProvider(input.provider_id);
  await insertModel(input as ModelInput & { id: string; provider_id: string; upstream_model: string });
  return getModelAdmin(input.id);
}

async function insertModel(input: ModelInput & { id: string; provider_id: string; upstream_model: string; listed?: ListedCapabilities }) {
  if (await findModel(input.id)) throw new ApiError("invalid_request", `Model ${input.id} already exists`, { status: 409 });
  const provider = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, input.provider_id) });
  if (provider?.kind === "classifier") throw new ApiError("invalid_request", "Classifier providers serve no chat models");
  if (provider?.kind === "search") throw new ApiError("invalid_request", "Search providers serve no chat models");
  if (provider?.kind === "speech") throw new ApiError("invalid_request", `Unknown provider ${input.provider_id}`);
  const now = new Date();
  await getDb()
    .insert(schema.model)
    .values({
      id: input.id,
      displayName: input.display_name ?? input.id,
      providerId: input.provider_id,
      upstreamModel: input.upstream_model,
      contextWindow: positiveInt(input.context_window ?? 128_000, "context_window"),
      maxOutputTokens: positiveInt(input.max_output_tokens ?? 8192, "max_output_tokens"),
      capabilities: markSetByHand({ ...DEFAULT_CAPS, ...input.listed, ...validateCaps(input.capabilities) }, validateCaps(input.capabilities)),
      pricing: validatePricing(input.pricing),
      fallbacks: validateFallbacks(input.fallbacks, input.id),
      extraBody: input.extra_body ?? null,
      roles: input.roles ?? null,
      enabled: input.enabled ?? true,
      sortOrder: input.sort_order ?? 0,
      createdAt: now,
      updatedAt: now,
    });
}

function validateFallbacks(f: unknown, self: string): string[] {
  if (f === undefined || f === null) return [];
  if (!Array.isArray(f) || f.some((x) => typeof x !== "string" || x === self)) {
    throw new ApiError("invalid_request", "fallbacks must be an array of other model ids");
  }
  return f as string[];
}

export async function updateModel(id: string, input: ModelInput) {
  const cur = await globalModel(id);
  const patch: Partial<Row> = { updatedAt: new Date() };
  if (input.display_name !== undefined) patch.displayName = input.display_name;
  if (input.provider_id !== undefined) {
    await assertGlobalProvider(input.provider_id);
    patch.providerId = input.provider_id;
  }
  if (input.upstream_model !== undefined) patch.upstreamModel = input.upstream_model;
  if (input.context_window !== undefined) patch.contextWindow = positiveInt(input.context_window, "context_window");
  if (input.max_output_tokens !== undefined) patch.maxOutputTokens = positiveInt(input.max_output_tokens, "max_output_tokens");
  if (input.capabilities !== undefined) {
    const given = validateCaps(input.capabilities);
    patch.capabilities = markSetByHand({ ...cur.capabilities, ...given }, given, cur.setByHand);
  }
  if (input.pricing !== undefined) patch.pricing = validatePricing(input.pricing);
  if (input.fallbacks !== undefined) patch.fallbacks = validateFallbacks(input.fallbacks, id);
  if (input.extra_body !== undefined) patch.extraBody = input.extra_body;
  if (input.roles !== undefined) patch.roles = input.roles;
  if (input.enabled !== undefined) patch.enabled = input.enabled;
  if (input.sort_order !== undefined) patch.sortOrder = input.sort_order;
  await getDb().update(schema.model).set(patch).where(eq(schema.model.id, id));
  // Admin changed the model: forget negotiated feature removals so they are re-probed.
  if (input.upstream_model !== undefined || input.provider_id !== undefined) await clearNegotiation(id);
  return getModelAdmin(id);
}

export async function deleteModel(id: string) {
  await globalModel(id);
  await getDb().delete(schema.model).where(eq(schema.model.id, id));
  negotiationCache.delete(id);
  return { deleted: true };
}

// --- Per-user model management (settings UIs, rpc/providers.ts) ----------------------

export interface ProviderModelInput {
  /** Global providers only: the catalog alias (defaults to the upstream model id). */
  id?: string;
  upstream_model?: string;
  display_name?: string;
  context_window?: number;
  max_output_tokens?: number;
  capabilities?: Partial<ModelCapabilities>;
  /** What the provider's model list says it can do; `capabilities` goes over it. */
  listed?: ListedCapabilities;
  /** What the provider's model list says it costs. */
  pricing?: ModelPricing;
}

/**
 * Models of a provider as the user sees them in settings: all of them for whoever may change the provider
 * (admins on global providers, the owner on personal ones), otherwise the ones the user can use.
 */
export async function listProviderModels(providerId: string, user: PublicUser): Promise<ModelEntry[]> {
  const { row, writable } = await providerForUser(providerId, user);
  const rows = await getDb()
    .select()
    .from(schema.model)
    .where(eq(schema.model.providerId, providerId))
    .orderBy(asc(schema.model.sortOrder), asc(schema.model.createdAt));
  const all = rows.map((r) => toEntry(r, row.ownerUserId));
  return writable ? all : all.filter((m) => canSee(m, user));
}

/**
 * Adds a model to a provider the user may change. On a personal provider the id is always
 * `<provider id>/<upstream model>`; on a global one (admins) it is the given alias or the upstream id.
 */
export async function addProviderModel(providerId: string, input: ProviderModelInput, user: PublicUser): Promise<ModelEntry> {
  const { row, writable } = await providerForUser(providerId, user);
  if (!writable) throw new ApiError("permission", "Only admins can change global providers");
  const upstream = input.upstream_model?.trim();
  if (!upstream) throw new ApiError("invalid_request", "upstream_model is required");
  if (upstream.length > 200) throw new ApiError("invalid_request", "upstream_model is too long");
  let id: string;
  if (row.ownerUserId) {
    id = `${providerId}/${upstream}`;
  } else {
    id = input.id?.trim() || upstream;
    if (id.startsWith(PERSONAL_PREFIX)) throw new ApiError("invalid_request", `Model ids starting with "${PERSONAL_PREFIX}" are reserved for personal models`);
  }
  if (!ID_RE.test(id)) throw new ApiError("invalid_request", `Model id ${id} may only use letters, digits and . _ : / -`);
  await insertModel({
    id,
    provider_id: providerId,
    upstream_model: upstream,
    display_name: input.display_name?.trim() || upstream,
    context_window: input.context_window || undefined,
    max_output_tokens: input.max_output_tokens || undefined,
    capabilities: input.capabilities,
    listed: input.listed,
    pricing: input.pricing,
  });
  return (await findModel(id))!;
}

/** A model of a provider the user may change (a global one only as admin). */
async function writableModel(modelId: string, user: PublicUser): Promise<ModelEntry> {
  const m = await findModel(modelId);
  if (!m) throw new ApiError("not_found", `Model ${modelId} not found`);
  // Throws not_found for another user's provider.
  const { writable } = await providerForUser(m.providerId, user).catch(() => {
    throw new ApiError("not_found", `Model ${modelId} not found`);
  });
  if (!writable) throw new ApiError("permission", "Only admins can change global providers");
  return m;
}

/**
 * Sets a model's effort levels by hand. Kept from then on, also none; a refusal remembered from the upstream goes,
 * so the levels are tried again.
 */
export async function setProviderModelEfforts(modelId: string, efforts: string[], user: PublicUser): Promise<ModelEntry> {
  const m = await writableModel(modelId, user);
  const given = validateCaps({ effort: efforts });
  const capabilities = markSetByHand({ ...m.capabilities, ...given }, given, m.setByHand);
  await getDb().update(schema.model).set({ capabilities, updatedAt: new Date() }).where(eq(schema.model.id, modelId));
  await clearNegotiation(modelId, "effort");
  return (await findModel(modelId))!;
}

/** Removes a model from a provider the user may change (a global one only as admin). */
export async function removeProviderModel(modelId: string, user: PublicUser) {
  await writableModel(modelId, user);
  await getDb().delete(schema.model).where(eq(schema.model.id, modelId));
  negotiationCache.delete(modelId);
  return { deleted: true };
}

// --- Slots -------------------------------------------------------------------------

export async function getSlots(): Promise<Slots> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, "slots") });
  return (r?.value as Slots | undefined) ?? {};
}

export async function setSlots(input: Slots) {
  await ensureDb();
  const slots: Slots = {};
  for (const name of SLOT_NAMES) {
    const v = input[name];
    // Unset or empty: the slot follows its default.
    if (v === undefined || v === null || v === "") continue;
    if (!(await findGlobalModel(v))) throw new ApiError("invalid_request", `Unknown model ${v} for slot ${name}`);
    slots[name] = v;
  }
  await getDb()
    .insert(schema.setting)
    .values({ key: "slots", value: slots, updatedAt: new Date() })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: slots, updatedAt: new Date() } });
  return slots;
}

// --- Capability negotiation memory ---------------------------------------------------

const negotiationCache = new Map<string, Set<string>>();

export async function getDisabledFeatures(modelId: string): Promise<Set<string>> {
  const hit = negotiationCache.get(modelId);
  if (hit) return hit;
  await ensureDb();
  const rows = await getDb().select().from(schema.modelNegotiation).where(eq(schema.modelNegotiation.modelId, modelId));
  const set = new Set(rows.map((r) => r.feature));
  negotiationCache.set(modelId, set);
  return set;
}

export async function disableFeature(modelId: string, feature: string, reason: string): Promise<void> {
  const set = await getDisabledFeatures(modelId);
  if (set.has(feature)) return;
  set.add(feature);
  await getDb()
    .insert(schema.modelNegotiation)
    .values({ modelId, feature, reason: reason.slice(0, 500), createdAt: new Date() })
    .onConflictDoNothing();
}

export async function clearNegotiation(modelId: string, feature?: string): Promise<void> {
  await ensureDb();
  const cond = feature
    ? and(eq(schema.modelNegotiation.modelId, modelId), eq(schema.modelNegotiation.feature, feature))
    : eq(schema.modelNegotiation.modelId, modelId);
  await getDb().delete(schema.modelNegotiation).where(cond);
  negotiationCache.delete(modelId);
}

export async function listNegotiation(modelId: string) {
  await ensureDb();
  const rows = await getDb().select().from(schema.modelNegotiation).where(eq(schema.modelNegotiation.modelId, modelId));
  return rows.map((r) => ({ feature: r.feature, reason: r.reason, created_at: r.createdAt.toISOString() }));
}

/** Highest supported level <= requested; otherwise the lowest supported; undefined if none. */
export function downgradeEffort(requested: string | undefined, supported: Effort[]): Effort | undefined {
  if (!requested || supported.length === 0) return undefined;
  const idx = EFFORT_LEVELS.indexOf(requested as Effort);
  if (idx < 0) return undefined;
  for (let i = idx; i >= 0; i--) if (supported.includes(EFFORT_LEVELS[i]!)) return EFFORT_LEVELS[i];
  return supported[0];
}
