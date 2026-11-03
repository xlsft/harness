/**
 * Connect implementation of harness.llm.v1.ProviderService: proto <-> src/services/providers.ts, models.ts,
 * discovery.ts and src/decision/classifier.ts. Global providers and the classifier need the admin role;
 * personal providers belong to the caller (services/providers.ts has the scope rules).
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { ProviderScope, ProviderService, type Classifier } from "../gen/harness/llm/v1/providers_pb.ts";
import { getClassifier, setClassifier, testClassifierProvider, type ClassifierSettings } from "../decision/classifier.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "../services/devices.ts";
import { discoverModelsForUser, testLlmProvider } from "../services/discovery.ts";
import {
  ROLE_SLOT_NAMES,
  addProviderModel,
  getSlots,
  listModelsForUser,
  listProviderModels,
  removeProviderModel,
  setSlots,
  type ModelEntry,
  type Slots,
} from "../services/models.ts";
import { PRESETS, PROTOCOLS } from "../services/presets.ts";
import {
  PROVIDER_TYPES,
  createProvider,
  deleteProvider,
  listProvidersForUser,
  providerForUser,
  updateProvider,
  type ProviderInput,
  type PublicProvider,
} from "../services/providers.ts";
import { checkClientVersion, requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

function modelToProto(m: ModelEntry) {
  return {
    id: m.id,
    displayName: m.displayName,
    upstreamModel: m.upstreamModel,
    contextWindow: BigInt(m.contextWindow),
    maxOutputTokens: BigInt(m.maxOutputTokens),
    enabled: m.enabled,
  };
}

async function providerToProto(p: PublicProvider, user: PublicUser) {
  const editable = p.scope === "personal" || user.role === "admin";
  return {
    id: p.id,
    scope: p.scope === "personal" ? ProviderScope.PERSONAL : ProviderScope.GLOBAL,
    type: p.type,
    displayName: p.display_name ?? "",
    baseUrl: p.base_url,
    apiKeySet: p.api_key_set,
    apiKeyHint: p.api_key_hint ?? "",
    headerNames: p.header_names,
    enabled: p.enabled,
    allowPrivateNetwork: p.allow_private_network,
    editable,
    models: (await listProviderModels(p.id, user)).map(modelToProto),
    createdAt: p.created_at,
    updatedAt: p.updated_at,
    presetId: p.preset_id ?? "",
    group: p.group,
  };
}

/** The scope an update or delete works in, after checking the caller may change the provider. */
async function writableOwner(id: string, user: PublicUser) {
  const { owner, writable } = await providerForUser(id, user);
  if (!writable) throw new ApiError("permission", "Only admins can change global providers");
  return owner;
}

function requireAdmin(user: PublicUser) {
  if (user.role !== "admin") throw new ApiError("permission", "Admin role required");
}

function classifierToProto(c: ClassifierSettings): Classifier {
  return {
    $typeName: "harness.llm.v1.Classifier",
    enabled: c.enabled,
    providerId: c.provider_id,
    endpoint: c.endpoint,
    model: c.model,
    minProbability: c.min_probability,
    minMargin: c.min_margin,
    onUncertain: c.on_uncertain,
    timeoutMs: c.timeout_ms,
    llmModel: c.llm_model,
  };
}

/** The role slots as configured ("" for one that follows its default); the classifier slot is not shown. */
function slotsToProto(s: Slots) {
  return { main: s.main ?? "", small: s.small ?? "", plan: s.plan ?? "", title: s.title ?? "" };
}

export function registerProviderService(router: ConnectRouter): void {
  const principal = async (ctx: HandlerContext) => {
    checkClientVersion(ctx);
    return (await requirePrincipal(ctx)).user;
  };

  router.service(ProviderService, {
    listPresets: guard(async (_req, ctx: HandlerContext) => {
      await principal(ctx);
      return {
        presets: PRESETS.map((p) => ({
          id: p.id,
          group: p.group,
          displayName: p.display_name,
          description: p.description,
          type: p.type,
          baseUrl: p.base_url,
          fields: p.fields.map((f) => ({ name: f.name, label: f.label, required: f.required, placeholder: f.placeholder ?? "" })),
          docsUrl: p.docs_url ?? "",
          keyUrl: p.key_url ?? "",
          defaultModels: p.default_models.map((m) => ({
            upstreamModel: m.upstream_model,
            displayName: m.display_name,
            contextWindow: BigInt(m.context_window ?? 0),
            maxOutputTokens: BigInt(m.max_output_tokens ?? 0),
          })),
          icon: p.icon,
          discoversModels: p.discovers_models,
          local: !!p.local,
        })),
        protocols: PROTOCOLS.map((p) => ({ id: p.id, group: p.group, label: p.label, baseUrlHint: p.base_url_hint })),
      };
    }),

    listProviders: guard(async (_req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      const providers = await listProvidersForUser(user);
      return {
        providers: await Promise.all(providers.map((p) => providerToProto(p, user))),
        types: [...PROVIDER_TYPES],
        canManageGlobal: user.role === "admin",
      };
    }),

    createProvider: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      const global = req.scope === ProviderScope.GLOBAL;
      if (global) requireAdmin(user);
      else if (req.scope !== ProviderScope.PERSONAL) throw new ApiError("invalid_request", "scope must be GLOBAL or PERSONAL");
      const input: ProviderInput = {
        preset_id: req.presetId || undefined,
        kind: req.group || undefined,
        type: req.type,
        display_name: req.displayName || null,
        base_url: req.baseUrl,
        api_key: req.apiKey || null,
        headers: req.headers,
        allow_private_network: req.allowPrivateNetwork,
      };
      if (global) input.id = req.id;
      else if (req.id) throw new ApiError("invalid_request", "Personal providers get a generated id");
      const p = await createProvider(input, global ? null : { userId: user.id });
      return { provider: await providerToProto(p, user) };
    }),

    updateProvider: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      const owner = await writableOwner(req.id, user);
      const input: ProviderInput = {};
      if (req.type !== undefined) input.type = req.type;
      if (req.displayName !== undefined) input.display_name = req.displayName;
      if (req.baseUrl !== undefined) input.base_url = req.baseUrl;
      if (req.apiKey !== undefined) input.api_key = req.apiKey || null;
      if (req.headers !== undefined) input.headers = req.headers.values;
      if (req.enabled !== undefined) input.enabled = req.enabled;
      if (req.allowPrivateNetwork !== undefined) input.allow_private_network = req.allowPrivateNetwork;
      const p = await updateProvider(req.id, input, owner);
      return { provider: await providerToProto(p, user) };
    }),

    deleteProvider: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      await deleteProvider(req.id, await writableOwner(req.id, user));
      return {};
    }),

    discoverModels: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      const r = await discoverModelsForUser(req.providerId, user);
      return {
        models: r.models.map((m) => ({
          upstreamModel: m.upstream_model,
          displayName: m.display_name,
          contextWindow: BigInt(m.context_window ?? 0),
          maxOutputTokens: BigInt(m.max_output_tokens ?? 0),
          modelIds: m.catalog_ids,
        })),
      };
    }),

    addModel: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      const m = await addProviderModel(
        req.providerId,
        {
          id: req.id,
          upstream_model: req.upstreamModel,
          display_name: req.displayName,
          context_window: Number(req.contextWindow),
          max_output_tokens: Number(req.maxOutputTokens),
          capabilities: { thinking: req.thinking, images: req.images },
        },
        user,
      );
      return { model: modelToProto(m) };
    }),

    testProvider: guard(async (req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      const p = await requirePrincipal(ctx);
      const { row, writable } = await providerForUser(req.providerId, p.user);
      if (!writable) throw new ApiError("permission", "Only admins can change global providers");
      const r = row.kind === "classifier" ? await testClassifierProvider(row.id, p) : await testLlmProvider(row.id);
      return { ok: r.ok, message: r.message, modelCount: r.model_count };
    }),

    removeModel: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      await removeProviderModel(req.modelId, user);
      return {};
    }),

    getClassifier: guard(async (_req, ctx: HandlerContext) => {
      requireAdmin(await principal(ctx));
      return { classifier: classifierToProto(await getClassifier()) };
    }),

    setClassifier: guard(async (req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      requireAdmin(user);
      const c = req.classifier;
      if (!c) throw new ApiError("invalid_request", "classifier is required");
      if (c.onUncertain && c.onUncertain !== "next" && c.onUncertain !== "ask") throw new ApiError("invalid_request", 'on_uncertain must be "next" or "ask"');
      const out = await setClassifier(
        {
          enabled: c.enabled,
          provider_id: c.providerId,
          endpoint: c.endpoint,
          model: c.model,
          min_probability: c.minProbability,
          min_margin: c.minMargin,
          on_uncertain: (c.onUncertain || "next") as "next" | "ask",
          timeout_ms: c.timeoutMs,
          llm_model: c.llmModel,
        },
        user.id,
      );
      return { classifier: classifierToProto(out) };
    }),

    getSlots: guard(async (_req, ctx: HandlerContext) => {
      const user = await principal(ctx);
      // An empty slot resolves to the first catalog model, an empty title slot to the small slot.
      const catalog = await listModelsForUser(user);
      const first = String(catalog.models[0]?.id ?? "");
      const small = catalog.slots.small ? String(catalog.slots.small) : first;
      const defaults: Slots = { main: first, small: first, plan: first, title: small };
      return { slots: slotsToProto(await getSlots()), editable: user.role === "admin", defaults: slotsToProto(defaults) };
    }),

    setSlots: guard(async (req, ctx: HandlerContext) => {
      requireAdmin(await principal(ctx));
      const input = req.slots;
      if (!input) throw new ApiError("invalid_request", "slots is required");
      // The classifier slot belongs to SetClassifier; keep it.
      const next: Slots = { classifier: (await getSlots()).classifier };
      for (const name of ROLE_SLOT_NAMES) next[name] = input[name];
      return { slots: slotsToProto(await setSlots(next)) };
    }),
  });
}

