/**
 * The settings view of the auto-mode classifier: the chain's decision step plus the model of the `llm` step.
 * The decision step uses a global provider:
 *   - a classifier provider of type "decision-model" (a typed decision model such as Jev): a `decision_model`
 *     step whose endpoint is the provider's base URL and whose key comes from it (`api_key_provider`);
 *   - a classifier provider of type "decision-http" (a policy webhook): an `http` step whose token and
 *     headers come from it (`credentials_provider`);
 *   - an LLM provider on the host of a named decision endpoint (an OpenRouter key set up before classifier
 *     providers existed): a `decision_model` step reusing its key.
 * A narrow, UI-shaped layer over the full chain config in config.ts: reading picks the managed step (the
 * first decision_model step, or the first http step that uses a provider's credentials); writing replaces
 * (or inserts, right after `rules`) that step and keeps every other step and setting as stored, secrets
 * included.
 */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import type { TestResult } from "../services/discovery.ts";
import { resolveModelForUser } from "../services/models.ts";
import { presetOf } from "../services/presets.ts";
import { loadProviderRuntime } from "../services/providers.ts";
import { httpCredentialsResolver, loadDecisionConfig, setDecisionConfig, type DecisionConfig, type StoredProvider } from "./config.ts";
import {
  DECISION_MODEL_ENDPOINTS,
  DEFAULT_DECISION_MODEL_TIMEOUT_MS,
  DEFAULT_MIN_MARGIN,
  DEFAULT_MIN_PROBABILITY,
  DecisionModelProvider,
  decisionEndpointUrl,
  type OnUncertain,
} from "./decision-model.ts";
import { DEFAULT_HTTP_TIMEOUT_MS, HttpProvider } from "./http.ts";
import type { DecideInput, DecisionProvider } from "./types.ts";

export interface ClassifierSettings {
  enabled: boolean;
  provider_id: string;
  endpoint: string;
  model: string;
  min_probability: number;
  min_margin: number;
  on_uncertain: OnUncertain;
  timeout_ms: number;
  llm_model: string;
}

type DecisionModelStep = Extract<StoredProvider, { type: "decision_model" }>;
type HttpStep = Extract<StoredProvider, { type: "http" }>;
type ManagedStep = DecisionModelStep | HttpStep;

function managedStep(c: DecisionConfig): ManagedStep | undefined {
  return c.providers.find((p): p is ManagedStep => p.type === "decision_model" || (p.type === "http" && !!p.settings.credentials_provider));
}

export async function getClassifier(): Promise<ClassifierSettings> {
  const { config } = await loadDecisionConfig();
  const step = managedStep(config);
  const base = {
    enabled: !!step,
    min_probability: DEFAULT_MIN_PROBABILITY,
    min_margin: DEFAULT_MIN_MARGIN,
    on_uncertain: "next" as OnUncertain,
    llm_model: config.classifier_model ?? "",
  };
  if (step?.type === "http") {
    return { ...base, provider_id: step.settings.credentials_provider ?? "", endpoint: step.settings.url, model: "", timeout_ms: step.settings.timeout_ms };
  }
  const s = step?.settings;
  return {
    ...base,
    provider_id: s?.api_key_provider ?? "",
    endpoint: s?.endpoint ?? "",
    model: s?.model ?? "",
    min_probability: s?.min_probability ?? DEFAULT_MIN_PROBABILITY,
    min_margin: s?.min_margin ?? DEFAULT_MIN_MARGIN,
    on_uncertain: s?.on_uncertain ?? "next",
    timeout_ms: s?.timeout_ms ?? DEFAULT_DECISION_MODEL_TIMEOUT_MS,
  };
}

/** The named endpoint served by a provider's host, if any. */
export function endpointForBaseUrl(baseUrl: string): string | undefined {
  let host: string;
  try {
    host = new URL(baseUrl).hostname.toLowerCase();
  } catch {
    return undefined;
  }
  for (const [name, e] of Object.entries(DECISION_MODEL_ENDPOINTS)) {
    if (new URL(e.url).hostname === host) return name;
  }
  return undefined;
}

/** A classifier provider's endpoint: the named endpoint when its base URL is exactly one, else the URL. */
function endpointOfClassifier(baseUrl: string): string {
  const url = baseUrl.replace(/\/+$/, "");
  for (const [name, e] of Object.entries(DECISION_MODEL_ENDPOINTS)) if (e.url === url) return name;
  return url;
}

/**
 * A stored step as PUT input: write-only secrets are left out, so validateDecisionConfig carries the stored
 * ones over (it keeps a step's secrets when the field is omitted).
 */
function asInput(p: StoredProvider): Record<string, unknown> {
  const base = { type: p.type, id: p.id, ...(p.display_name ? { display_name: p.display_name } : {}) };
  if (p.type === "http") {
    const { bearer_token_enc: _t, headers_enc: _h, ...rest } = p.settings;
    return { ...base, settings: rest };
  }
  if (p.type === "decision_model") {
    const { api_key_enc: _k, ...rest } = p.settings;
    return { ...base, settings: rest };
  }
  return { ...base, settings: p.settings };
}

async function globalProvider(id: string) {
  let rt: Awaited<ReturnType<typeof loadProviderRuntime>> | undefined;
  try {
    rt = await loadProviderRuntime(id);
  } catch {
    rt = undefined;
  }
  // Same answer for a missing and a personal provider: the chain only uses global keys.
  if (!rt || rt.ownerUserId) throw new ApiError("invalid_request", `Unknown global provider ${id}`);
  return rt;
}

type ProviderRuntime = Awaited<ReturnType<typeof loadProviderRuntime>>;

const isHttpClassifier = (rt: ProviderRuntime) => rt.kind === "classifier" && (rt.type as string) === "decision-http";

/** The http step of a policy-webhook classifier provider (its key and headers are read when deciding). */
function httpStep(rt: ProviderRuntime, providerId: string, id: string, timeoutMs?: number): HttpStep {
  return {
    type: "http",
    id,
    settings: { url: rt.baseUrl, credentials_provider: providerId, allow_private_network: rt.allowPrivateNetwork, timeout_ms: timeoutMs || DEFAULT_HTTP_TIMEOUT_MS },
  };
}

/** The decision model a classifier provider uses by default: its preset's first model. */
async function defaultModelOf(providerId: string): Promise<string | undefined> {
  await ensureDb();
  const row = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, providerId) });
  return row ? presetOf(row)?.default_models[0]?.upstream_model : undefined;
}

export async function setClassifier(input: Partial<ClassifierSettings>, updatedBy?: string): Promise<ClassifierSettings> {
  const { config } = await loadDecisionConfig();
  const target = managedStep(config);
  const steps = config.providers.map(asInput);
  const at = target ? config.providers.indexOf(target) : -1;

  if (input.enabled) {
    const providerId = input.provider_id?.trim();
    if (!providerId) throw new ApiError("invalid_request", "provider_id is required");
    const rt = await globalProvider(providerId);
    const keepName = target?.display_name ? { display_name: target.display_name } : {};
    let step: Record<string, unknown>;
    if (isHttpClassifier(rt)) {
      step = { ...httpStep(rt, providerId, target?.type === "http" ? target.id : "http:classifier", input.timeout_ms), ...keepName };
    } else {
      let endpoint: string | undefined;
      let model = input.model?.trim();
      if (rt.kind === "classifier") {
        endpoint = endpointOfClassifier(rt.baseUrl);
        model ||= await defaultModelOf(providerId);
      } else {
        endpoint = input.endpoint?.trim() || endpointForBaseUrl(rt.baseUrl);
        if (!endpoint) throw new ApiError("invalid_request", `Provider ${providerId} serves no known decision endpoint; set endpoint`);
      }
      step = {
        type: "decision_model",
        id: target?.type === "decision_model" ? target.id : "decision_model",
        ...keepName,
        settings: {
          endpoint,
          ...(model ? { model } : {}),
          api_key_provider: providerId,
          ...(rt.allowPrivateNetwork || (target?.type === "decision_model" && target.settings.allow_private_network) ? { allow_private_network: true } : {}),
          min_probability: input.min_probability ?? DEFAULT_MIN_PROBABILITY,
          min_margin: input.min_margin ?? DEFAULT_MIN_MARGIN,
          on_uncertain: input.on_uncertain || "next",
          timeout_ms: input.timeout_ms || DEFAULT_DECISION_MODEL_TIMEOUT_MS,
        },
      };
    }
    if (at >= 0) steps[at] = step;
    else steps.splice(config.providers.findIndex((p) => p.type === "rules") + 1, 0, step);
  } else if (at >= 0) {
    steps.splice(at, 1);
  }

  await setDecisionConfig(
    {
      providers: steps,
      on_error: config.on_error,
      cache_ttl_seconds: config.cache_ttl_seconds,
      classifier_model: input.llm_model === undefined ? config.classifier_model : input.llm_model.trim() || null,
      rate_limit_per_min: config.rate_limit_per_min,
      rules: config.rules,
    },
    updatedBy,
  );
  return getClassifier();
}

/** A harmless tool call the connection test asks about. */
const SAMPLE: DecideInput = {
  sessionId: "",
  toolName: "Bash",
  inputJson: '{"command":"ls"}',
  input: { command: "ls" },
  cwd: "/tmp/harness-test",
  additionalDirectories: [],
  recentUserMessages: ["List the files in this directory."],
  assistantRationale: "",
  facts: { subcommands: ["ls"], writePaths: [], networkHosts: [], insideGitRepo: false, gitBranch: "" },
};

/**
 * Connection test of a classifier provider: one sample decision about a harmless command. A decision model
 * uses the classifier's model when this provider is the configured one, else its preset's default model.
 */
export async function testClassifierProvider(providerId: string, principal: Principal): Promise<TestResult> {
  const rt = await loadProviderRuntime(providerId);
  const settings = await getClassifier();
  const configured = settings.enabled && settings.provider_id === providerId;
  let provider: DecisionProvider;
  if ((rt.type as string) === "decision-http") {
    const step = httpStep(rt, providerId, "http:test");
    provider = new HttpProvider({ id: step.id, url: rt.baseUrl, credentials: httpCredentialsResolver(step), allowPrivateNetwork: rt.allowPrivateNetwork, timeoutMs: 10_000 });
  } else {
    const model = (configured && settings.model) || (await defaultModelOf(providerId));
    if (!model) return { ok: false, message: "Choose the decision model in the classifier settings first", model_count: 0 };
    provider = new DecisionModelProvider({
      id: "decision_model:test",
      url: decisionEndpointUrl(endpointOfClassifier(rt.baseUrl)),
      model,
      apiKey: async () => rt.apiKey,
      allowPrivateNetwork: rt.allowPrivateNetwork,
      timeoutMs: 15_000,
      // Any answer counts: the test is about the connection, not the verdict.
      minProbability: 0,
      minMargin: 0,
    });
  }
  try {
    const d = await provider.decide(SAMPLE, { principal });
    return { ok: true, message: `Connected: a sample "ls" was answered ${d.decision}`, model_count: 0 };
  } catch (e) {
    return { ok: false, message: e instanceof Error ? e.message : String(e), model_count: 0 };
  }
}

// --- per-user override -----------------------------------------------------------------------------

/**
 * A user's own classifier choice over the server default (ProviderService.GetMyClassifier / SetMyClassifier),
 * stored in the `setting` table under `classifier:user:<user id>`. It changes only that user's chain: the
 * decision step (another global classifier provider, or none) and the model of the `llm` steps that follow
 * the chain's classifier_model. A choice that went stale (provider removed or disabled; model gone or hidden
 * from the user) follows the server default again, so a user is never left with a broken chain.
 */
export interface ClassifierOverride {
  /** "default": follow the server default; "none": no decision step; "provider": `provider_id`. */
  choice: "default" | "none" | "provider";
  provider_id: string;
  /** Global model of the model classifier; "" follows the server default. */
  llm_model: string;
}

export const FOLLOW_DEFAULT: Readonly<ClassifierOverride> = { choice: "default", provider_id: "", llm_model: "" };

const overrideKey = (userId: string) => `classifier:user:${userId}`;

/** A global, enabled classifier provider, or an error naming the id. */
async function classifierProviderForOverride(id: string): Promise<ProviderRuntime> {
  let rt: ProviderRuntime | undefined;
  try {
    rt = await loadProviderRuntime(id);
  } catch {
    rt = undefined;
  }
  if (!rt || rt.ownerUserId || rt.kind !== "classifier" || !rt.enabled) throw new ApiError("invalid_request", `Unknown classifier provider ${id}`);
  return rt;
}

/** A global model the user may use, or an error naming the id. */
async function modelForOverride(id: string, user: PublicUser): Promise<string> {
  const m = await resolveModelForUser(id, user).catch(() => undefined);
  if (!m || m.ownerUserId) throw new ApiError("invalid_request", `Unknown model ${id}`);
  return m.id;
}

/**
 * The decision step a user's chosen provider runs as. Its id names the provider, so the audit log shows
 * which classifier decided and its cache entries stay apart from the server default's.
 */
async function overrideStep(providerId: string): Promise<ManagedStep> {
  const rt = await classifierProviderForOverride(providerId);
  if (isHttpClassifier(rt)) return httpStep(rt, providerId, `http:${providerId}`);
  const endpoint = endpointOfClassifier(rt.baseUrl);
  const named = endpoint in DECISION_MODEL_ENDPOINTS ? DECISION_MODEL_ENDPOINTS[endpoint as keyof typeof DECISION_MODEL_ENDPOINTS] : undefined;
  const model = (await defaultModelOf(providerId)) ?? named?.defaultModel;
  if (!model) throw new ApiError("invalid_request", `Classifier provider ${providerId} has no default decision model`);
  return {
    type: "decision_model",
    id: `decision_model:${providerId}`,
    settings: {
      endpoint,
      model,
      api_key_provider: providerId,
      allow_private_network: rt.allowPrivateNetwork,
      timeout_ms: DEFAULT_DECISION_MODEL_TIMEOUT_MS,
      min_probability: DEFAULT_MIN_PROBABILITY,
      min_margin: DEFAULT_MIN_MARGIN,
      on_uncertain: "next",
    },
  };
}

/** The global provider a managed step uses. */
const stepProviderId = (s: ManagedStep) => (s.type === "http" ? s.settings.credentials_provider : s.settings.api_key_provider) ?? "";

async function storedOverride(userId: string): Promise<ClassifierOverride> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, overrideKey(userId)) });
  const v = r?.value as Partial<ClassifierOverride> | undefined;
  if (!v) return { ...FOLLOW_DEFAULT };
  const choice = v.choice === "none" || v.choice === "provider" ? v.choice : "default";
  return { choice, provider_id: choice === "provider" ? (v.provider_id ?? "") : "", llm_model: v.llm_model ?? "" };
}

interface Resolved {
  override: ClassifierOverride;
  /** The decision step of a "provider" choice. */
  step?: ManagedStep;
}

/** Decide asks for every prompting tool call, so resolved overrides are kept briefly; other replicas follow within this time. */
const RESOLVED_MS = 5_000;
const resolvedCache = new Map<string, { value: Resolved; at: number }>();

/** The user's override with stale parts reset to the server default, plus the step of a chosen provider. */
async function resolveOverride(user: PublicUser): Promise<Resolved> {
  const hit = resolvedCache.get(user.id);
  if (hit && Date.now() - hit.at < RESOLVED_MS) return hit.value;
  const o = await storedOverride(user.id);
  const value: Resolved = { override: o };
  if (o.choice === "provider") {
    try {
      value.step = await overrideStep(o.provider_id);
    } catch {
      value.override = { ...value.override, choice: "default", provider_id: "" };
    }
  }
  if (o.llm_model) {
    try {
      await modelForOverride(o.llm_model, user);
    } catch {
      value.override = { ...value.override, llm_model: "" };
    }
  }
  resolvedCache.set(user.id, { value, at: Date.now() });
  return value;
}

/** For tests. */
export function clearClassifierOverrideCache(): void {
  resolvedCache.clear();
}

/** The user's override as it applies now (a stale choice reads as following the server default). */
export async function getClassifierOverride(user: PublicUser): Promise<ClassifierOverride> {
  return { ...(await resolveOverride(user)).override };
}

export async function setClassifierOverride(user: PublicUser, input: Partial<ClassifierOverride>): Promise<ClassifierOverride> {
  const choice = input.choice ?? "default";
  if (choice !== "default" && choice !== "none" && choice !== "provider") throw new ApiError("invalid_request", 'choice must be "default", "none" or "provider"');
  const next: ClassifierOverride = { choice, provider_id: "", llm_model: "" };
  if (choice === "provider") {
    const id = input.provider_id?.trim();
    if (!id) throw new ApiError("invalid_request", "provider_id is required");
    await overrideStep(id);
    next.provider_id = id;
  }
  const model = input.llm_model?.trim();
  if (model) next.llm_model = await modelForOverride(model, user);

  await ensureDb();
  const key = overrideKey(user.id);
  if (next.choice === "default" && !next.llm_model) {
    await getDb().delete(schema.setting).where(eq(schema.setting.key, key));
  } else {
    const now = new Date();
    await getDb()
      .insert(schema.setting)
      .values({ key, value: next, updatedAt: now })
      .onConflictDoUpdate({ target: schema.setting.key, set: { value: next, updatedAt: now } });
  }
  resolvedCache.delete(user.id);
  return getClassifierOverride(user);
}

/**
 * The chain configuration for one user: the server default with the user's override applied. Returns the
 * given config itself when nothing changes, so the caller keeps the chain built from it. Steps that do not
 * change keep their identity (the caller reuses their providers); `llm` steps that take the user's model
 * are copies.
 */
export async function decisionConfigFor(user: PublicUser, config: DecisionConfig): Promise<DecisionConfig> {
  const { override: o, step } = await resolveOverride(user);
  let providers = config.providers;
  if (o.choice !== "default") {
    const target = managedStep(config);
    const at = target ? providers.indexOf(target) : -1;
    if (o.choice === "none") {
      if (at >= 0) providers = providers.filter((_, i) => i !== at);
    } else if (step && !(target && stepProviderId(target) === o.provider_id)) {
      // The server default's own provider keeps the admin's step (model, thresholds) as it is.
      if (at >= 0) providers = providers.map((p, i) => (i === at ? step : p));
      else {
        const rules = providers.findIndex((p) => p.type === "rules");
        providers = [...providers.slice(0, rules + 1), step, ...providers.slice(rules + 1)];
      }
    }
  }
  let classifierModel = config.classifier_model;
  if (o.llm_model && o.llm_model !== config.classifier_model) {
    classifierModel = o.llm_model;
    providers = providers.map((p) => (p.type === "llm" ? { ...p } : p));
  }
  if (providers === config.providers && classifierModel === config.classifier_model) return config;
  return { ...config, providers, classifier_model: classifierModel };
}
