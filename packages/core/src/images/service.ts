/**
 * Image generation (proto/harness/llm/v1/images.proto, docs/design/images.md): the image models of the LLM providers,
 * the Images settings section and GatewayService.GenerateImages.
 *
 * An image model is a row of the model table with kind "image" on an LLM provider: the provider's key, scope and policy
 * apply as for chat models, and so does the allowlist of a global model (its `roles`). Image models never enter the
 * chat catalog, and the chat gateway refuses them (services/models.ts resolveModelForUser). The server default is the
 * "image" slot; each user may pick another model for themselves in their synced settings (the client reads that).
 */
import { and, asc, eq, isNull, or } from "drizzle-orm";
import { can, need, ROLES } from "../auth/roles.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { backoffDelay } from "../services/gateway.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import { providerAllowed, serverPolicyFor } from "../services/managed-policy.ts";
import { findModel, getSlots, insertModel, resolveModelForUser, setSlots, type ModelEntry } from "../services/models.ts";
import { billingOf, presetOf } from "../services/presets.ts";
import { loadProviderRuntime, PERSONAL_PREFIX, providerForUser } from "../services/providers.ts";
import { enforceQuotas } from "../services/quotas.ts";
import { countMetric, recordMetric } from "../telemetry/metrics.ts";
import { apiTakesInputs, generateImages, imageApiOf, providerImageApi, sniffImage, type ImageApi, type ImageInput, type ImageOutput } from "./adapters.ts";
import { defaultImagePricing, imageCost, validImagePricing, type ImagePricing } from "./pricing.ts";

export const MAX_COUNT = 4;
export const MAX_INPUT_IMAGES = 8;
export const MAX_INPUT_BYTES = 20 << 20;
export const MAX_INPUT_TOTAL = 50 << 20;
export const MAX_PROMPT_CHARS = 32_000;
const ACCEPTED_INPUTS = ["image/png", "image/jpeg", "image/webp"];
const RETRIES = 2;

type ModelRow = typeof schema.model.$inferSelect;
type ProviderRow = typeof schema.provider.$inferSelect;

export interface ImageModelView {
  id: string;
  display_name: string;
  provider_id: string;
  provider_name: string;
  upstream_model: string;
  image_api: ImageApi | "";
  edits: boolean;
  roles: string[];
  enabled: boolean;
  editable: boolean;
  pricing: ImagePricing | null;
  personal: boolean;
  usable: boolean;
}

export interface ImageProviderView {
  id: string;
  display_name: string;
  personal: boolean;
  image_api: "openai" | "gemini" | "openrouter";
  editable: boolean;
}

function providerName(p: ProviderRow): string {
  return p.displayName || presetOf(p)?.display_name || p.id;
}

function usableBy(m: ModelRow, p: ProviderRow, user: PublicUser): boolean {
  if (!m.enabled || !p.enabled) return false;
  if (p.ownerUserId) return p.ownerUserId === user.id;
  const roles = m.roles as string[] | null;
  return !roles || roles.length === 0 || roles.includes(user.role);
}

function view(m: ModelRow, p: ProviderRow, user: PublicUser, usable: boolean): ImageModelView {
  const api = imageApiOf(p, m.upstreamModel);
  const admin = can(user, "providers.write");
  return {
    id: m.id,
    display_name: m.displayName,
    provider_id: p.id,
    provider_name: providerName(p),
    upstream_model: m.upstreamModel,
    image_api: api,
    edits: api !== "" && apiTakesInputs(api),
    roles: admin && !p.ownerUserId ? ((m.roles as string[] | null) ?? []) : [],
    enabled: m.enabled,
    editable: p.ownerUserId ? p.ownerUserId === user.id : admin,
    pricing: (m.pricing as ImagePricing | null) ?? null,
    personal: !!p.ownerUserId,
    usable,
  };
}

/** The image models the user may see (admins: every global one too), global first, and the providers they may add to. */
export async function getImageSettings(user: PublicUser) {
  await ensureDb();
  const admin = can(user, "providers.write");
  const policy = await serverPolicyFor(user);
  const rows = await getDb()
    .select()
    .from(schema.model)
    .innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId))
    .where(and(eq(schema.model.kind, "image"), or(isNull(schema.provider.ownerUserId), eq(schema.provider.ownerUserId, user.id))))
    .orderBy(asc(schema.model.sortOrder), asc(schema.model.createdAt));
  const models: ImageModelView[] = [];
  for (const { model, provider } of rows) {
    const usable = usableBy(model, provider, user) && providerAllowed(policy, provider);
    if (usable || (admin && !provider.ownerUserId)) models.push(view(model, provider, user, usable));
  }
  models.sort((a, b) => Number(a.personal) - Number(b.personal));
  const providerRows = await getDb()
    .select()
    .from(schema.provider)
    .where(and(eq(schema.provider.kind, "llm"), or(isNull(schema.provider.ownerUserId), eq(schema.provider.ownerUserId, user.id))))
    .orderBy(asc(schema.provider.createdAt));
  const providers: ImageProviderView[] = [];
  for (const p of providerRows) {
    const api = providerImageApi(p);
    if (!api) continue;
    if (!p.ownerUserId && !admin) continue;
    if (!providerAllowed(policy, p) && !admin) continue;
    providers.push({ id: p.id, display_name: providerName(p), personal: !!p.ownerUserId, image_api: api, editable: true });
  }
  providers.sort((a, b) => Number(a.personal) - Number(b.personal));
  return { models, providers, default_model: (await getSlots()).image ?? "", can_manage: admin, roles: [...ROLES] };
}

function requireAdmin(user: PublicUser) {
  need(user, "providers.write");
}

export async function setImageDefault(user: PublicUser, modelId: string): Promise<string> {
  requireAdmin(user);
  const next = await setSlots({ ...(await getSlots()), image: modelId.trim() || undefined });
  return next.image ?? "";
}

function validRoles(roles: string[] | undefined): string[] | null {
  if (!roles?.length) return null;
  for (const r of roles) if (!(ROLES as readonly string[]).includes(r)) throw new ApiError("invalid_request", `Unknown role ${JSON.stringify(r)}`);
  return [...new Set(roles)];
}

function pricingOf(p: unknown): ImagePricing {
  try {
    return validImagePricing(p);
  } catch (e) {
    throw new ApiError("invalid_request", (e as Error).message);
  }
}

const ID_RE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;

export interface AddImageModelInput {
  provider_id: string;
  upstream_model: string;
  id?: string;
  display_name?: string;
  roles?: string[];
  pricing?: ImagePricing;
}

async function rowsOf(modelId: string): Promise<{ model: ModelRow; provider: ProviderRow }> {
  const [r] = await getDb().select().from(schema.model).innerJoin(schema.provider, eq(schema.provider.id, schema.model.providerId)).where(eq(schema.model.id, modelId)).limit(1);
  if (!r) throw new ApiError("not_found", `Image model ${modelId} not found`);
  return r;
}

/** Marks the price as set by hand, so no price refresh touches it (services/models.ts priceSetByHand). */
function handPriced(capabilities: unknown) {
  const caps = (capabilities ?? {}) as { set_by_hand?: string[] };
  return { ...caps, set_by_hand: [...new Set([...(caps.set_by_hand ?? []), "pricing"])] };
}

export async function addImageModel(user: PublicUser, input: AddImageModelInput): Promise<ImageModelView> {
  await ensureDb();
  const { row, writable } = await providerForUser(input.provider_id, user);
  if (!writable) throw new ApiError("permission", "Only admins can change global providers");
  if (row.kind !== "llm") throw new ApiError("invalid_request", `Provider ${row.id} serves no models`);
  const upstream = input.upstream_model?.trim();
  if (!upstream) throw new ApiError("invalid_request", "upstream_model is required");
  if (upstream.length > 200) throw new ApiError("invalid_request", "upstream_model is too long");
  const api = imageApiOf(row, upstream);
  if (!api) throw new ApiError("invalid_request", `${providerName(row)} has no image generation API (OpenAI-compatible, Gemini and OpenRouter providers do)`);
  let id: string;
  if (row.ownerUserId) {
    id = `${row.id}/${upstream}`;
    if (input.roles?.length) throw new ApiError("invalid_request", "A personal model has no allowlist: only its owner uses it");
  } else {
    id = input.id?.trim() || upstream;
    if (id.startsWith(PERSONAL_PREFIX)) throw new ApiError("invalid_request", `Model ids starting with "${PERSONAL_PREFIX}" are reserved for personal models`);
  }
  if (!ID_RE.test(id)) throw new ApiError("invalid_request", `Model id ${id} may only use letters, digits and . _ : / -`);
  const roles = row.ownerUserId ? null : validRoles(input.roles);
  const given = input.pricing ? pricingOf(input.pricing) : null;
  const pricing = billingOf(row) === "plan" ? null : (given ?? defaultImagePricing(api, upstream));
  await insertModel({ id, provider_id: row.id, upstream_model: upstream, display_name: input.display_name?.trim() || upstream, roles, kind: "image", price_source: "hand" });
  const m = await findModel(id);
  await getDb()
    .update(schema.model)
    .set({ pricing, pricingSource: given ? "hand" : pricing ? "preset" : null, pricingUpdatedAt: pricing ? new Date() : null, capabilities: handPriced(m ? { ...m.capabilities, set_by_hand: m.setByHand } : {}) })
    .where(eq(schema.model.id, id));
  const r = await rowsOf(id);
  return view(r.model, r.provider, user, usableBy(r.model, r.provider, user));
}

async function writableImageModel(user: PublicUser, modelId: string) {
  await ensureDb();
  const r = await rowsOf(modelId).catch(() => undefined);
  if (!r || r.model.kind !== "image" || (r.provider.ownerUserId && r.provider.ownerUserId !== user.id)) throw new ApiError("not_found", `Image model ${modelId} not found`);
  if (!r.provider.ownerUserId && !can(user, "providers.write")) throw new ApiError("permission", "Only admins can change global image models");
  return r;
}

export interface UpdateImageModelInput {
  display_name?: string;
  enabled?: boolean;
  roles?: string[];
  pricing?: ImagePricing;
  clear_pricing?: boolean;
}

export async function updateImageModel(user: PublicUser, modelId: string, input: UpdateImageModelInput): Promise<ImageModelView> {
  const { model, provider } = await writableImageModel(user, modelId);
  const patch: Partial<ModelRow> = { updatedAt: new Date() };
  if (input.display_name !== undefined) {
    const name = input.display_name.trim();
    if (!name) throw new ApiError("invalid_request", "display_name is empty");
    patch.displayName = name;
  }
  if (input.enabled !== undefined) patch.enabled = input.enabled;
  if (input.roles !== undefined) {
    if (provider.ownerUserId) throw new ApiError("invalid_request", "A personal model has no allowlist: only its owner uses it");
    patch.roles = validRoles(input.roles);
  }
  if (input.clear_pricing || input.pricing) {
    if (input.pricing && billingOf(provider) === "plan") throw new ApiError("invalid_request", "A subscription plan's requests carry no price");
    patch.pricing = input.clear_pricing ? null : pricingOf(input.pricing);
    Object.assign(patch, { pricingSource: "hand", pricingUpdatedAt: new Date(), capabilities: handPriced(model.capabilities) });
  }
  await getDb().update(schema.model).set(patch).where(eq(schema.model.id, modelId));
  const r = await rowsOf(modelId);
  return view(r.model, r.provider, user, usableBy(r.model, r.provider, user));
}

export async function removeImageModel(user: PublicUser, modelId: string): Promise<void> {
  await writableImageModel(user, modelId);
  await getDb().delete(schema.model).where(eq(schema.model.id, modelId));
  const slots = await getSlots();
  if (slots.image === modelId) await setSlots({ ...slots, image: undefined });
}

// --- Generation --------------------------------------------------------------------------------------------

export interface GenerateInput {
  model: string;
  prompt: string;
  size?: string;
  count?: number;
  quality?: string;
  inputs?: ImageInput[];
  sessionId?: string;
  agentId?: string;
  signal?: AbortSignal;
}

export interface GenerateResult extends ImageOutput {
  costUsd: number;
  priced: boolean;
  servedModel: string;
}

function validSize(size: string | undefined): string {
  const s = (size ?? "").trim().toLowerCase();
  if (!s || s === "auto") return "";
  const m = /^(\d{2,5})x(\d{2,5})$/.exec(s);
  if (!m) throw new ApiError("invalid_request", `size must be WIDTHxHEIGHT (1024x1024) or auto, not ${JSON.stringify(size)}`);
  const w = Number(m[1]);
  const h = Number(m[2]);
  if (w < 64 || h < 64 || w > 8192 || h > 8192) throw new ApiError("invalid_request", "size must be between 64 and 8192 pixels a side");
  return `${w}x${h}`;
}

function validInputs(inputs: ImageInput[]): ImageInput[] {
  if (inputs.length > MAX_INPUT_IMAGES) throw new ApiError("invalid_request", `At most ${MAX_INPUT_IMAGES} input images`);
  let total = 0;
  return inputs.map((img, i) => {
    if (!img.data.byteLength) throw new ApiError("invalid_request", `Input image ${i + 1} is empty`);
    if (img.data.byteLength > MAX_INPUT_BYTES) throw new ApiError("invalid_request", `Input image ${i + 1} is larger than ${MAX_INPUT_BYTES >> 20} MiB`);
    total += img.data.byteLength;
    if (total > MAX_INPUT_TOTAL) throw new ApiError("invalid_request", `The input images are larger than ${MAX_INPUT_TOTAL >> 20} MiB together`);
    // The bytes say what they are; a declared type must agree.
    const sniffed = sniffImage(img.data);
    if (!sniffed || !ACCEPTED_INPUTS.includes(sniffed)) throw new ApiError("invalid_request", `Input image ${i + 1} is not a PNG, JPEG or WebP image`);
    const name = (img.name ?? "").replace(/[^A-Za-z0-9._-]/g, "_").slice(0, 100);
    return { data: img.data, mediaType: sniffed, name: name || undefined };
  });
}

function retryable(err: unknown): number | undefined {
  if (!(err instanceof ApiError)) return undefined;
  if (err.type === "rate_limit") return err.retryAfterMs !== undefined && err.retryAfterMs > 20_000 ? undefined : (err.retryAfterMs ?? 5_000);
  if (err.type === "upstream" && (err.upstreamStatus ?? 0) >= 500) return 0;
  return undefined;
}

/** Generates images for the user: resolves and checks the model, checks spend limits, calls the upstream, records usage. */
export async function generateForUser(principal: Principal, input: GenerateInput): Promise<GenerateResult> {
  const prompt = input.prompt?.trim() ?? "";
  if (!prompt) throw new ApiError("invalid_request", "prompt is empty");
  if (prompt.length > MAX_PROMPT_CHARS) throw new ApiError("invalid_request", `prompt is longer than ${MAX_PROMPT_CHARS} characters`);
  const count = input.count || 1;
  if (!Number.isInteger(count) || count < 1 || count > MAX_COUNT) throw new ApiError("invalid_request", `count must be between 1 and ${MAX_COUNT}`);
  const quality = (input.quality ?? "").trim().toLowerCase();
  if (quality && !["low", "medium", "high", "auto", "standard", "hd"].includes(quality)) throw new ApiError("invalid_request", `Unknown quality ${JSON.stringify(input.quality)}`);
  const size = validSize(input.size);
  const inputs = validInputs(input.inputs ?? []);
  const model = await resolveModelForUser(input.model, principal.user, "image");
  await enforceQuotas(principal.user, input.sessionId || undefined);
  const provider = await loadProviderRuntime(model.providerId);
  if (provider.cloud) throw new ApiError("invalid_request", `Image models on cloud platform providers are not supported`);
  const api = imageApiOf({ type: provider.type, baseUrl: provider.baseUrl, presetId: provider.presetId }, model.upstreamModel);
  if (!api) throw new ApiError("invalid_request", `Provider ${provider.id} has no image generation API`);
  if (inputs.length && !apiTakesInputs(api)) throw new ApiError("invalid_request", `${model.displayName} generates from text only; it takes no input images`);

  const startedAt = Date.now();
  let status = "error";
  let errorType: string | undefined;
  let out: ImageOutput | undefined;
  let retries = 0;
  let cost = 0;
  let priced = false;
  try {
    for (;;) {
      try {
        out = await generateImages(
          { displayName: provider.id, baseUrl: provider.baseUrl, apiKey: provider.apiKey, headers: provider.headers, allowPrivateNetwork: provider.allowPrivateNetwork },
          { api, model: model.upstreamModel, prompt, size, count, quality: quality || undefined, inputs, signal: input.signal },
        );
        break;
      } catch (err) {
        const wait = retryable(err);
        if (wait === undefined || retries >= RETRIES || input.signal?.aborted) throw err;
        await new Promise((r) => setTimeout(r, Math.max(wait, backoffDelay(retries))));
        retries++;
      }
    }
    status = "ok";
  } catch (err) {
    errorType = err instanceof ApiError ? err.type : "internal";
    if (input.signal?.aborted) status = "aborted";
    throw err;
  } finally {
    const images = out?.images.length ?? 0;
    const pricing = billingOf({ presetId: provider.presetId ?? null, kind: provider.kind, type: provider.type, baseUrl: provider.baseUrl }) === "plan" ? null : (model.pricing as ImagePricing | null);
    ({ cost, priced } = out ? imageCost(pricing, images, out.inputTokens, out.outputTokens, out.reportedCostUsd) : { cost: 0, priced: !!pricing });
    await recordImageUsage({ principal, model, input, out, cost, priced, retries, status, errorType, durationMs: Date.now() - startedAt, personal: !!provider.ownerUserId }).catch((e) =>
      console.error("[harness] failed to record image usage", e),
    );
  }
  return { ...out!, costUsd: cost, priced, servedModel: model.id };
}

async function recordImageUsage(r: {
  principal: Principal;
  model: ModelEntry;
  input: GenerateInput;
  out?: ImageOutput;
  cost: number;
  priced: boolean;
  retries: number;
  status: string;
  errorType?: string;
  durationMs: number;
  personal: boolean;
}) {
  await getDb()
    .insert(schema.usageRecord)
    .values({
      id: newId("usg"),
      userId: r.principal.user.id,
      deviceId: r.principal.deviceId ?? null,
      modelId: r.model.id,
      providerId: r.model.providerId,
      upstreamModel: r.model.upstreamModel,
      requestClass: "image",
      sessionId: r.input.sessionId || null,
      agentId: r.input.agentId || null,
      inputTokens: r.out?.inputTokens ?? 0,
      outputTokens: r.out?.outputTokens ?? 0,
      costUsd: r.cost,
      images: r.out?.images.length ?? 0,
      priced: r.priced,
      retries: r.retries,
      status: r.status,
      errorType: r.errorType ?? null,
      durationMs: r.durationMs,
      createdAt: new Date(),
    });
  const attrs = { model: r.personal ? "personal" : r.model.id, provider: r.personal ? "personal" : r.model.providerId, status: r.status };
  countMetric("harness.image.requests", 1, attrs);
  countMetric("harness.image.images", r.out?.images.length ?? 0, attrs);
  if (!r.personal) countMetric("harness.image.cost", r.cost, attrs, "USD");
  recordMetric("harness.image.duration", r.durationMs, attrs, "ms");
}
