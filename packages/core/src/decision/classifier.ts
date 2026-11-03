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
import type { Principal } from "../services/devices.ts";
import type { TestResult } from "../services/discovery.ts";
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
    if (rt.kind === "classifier" && (rt.type as string) === "decision-http") {
      step = {
        type: "http",
        id: target?.type === "http" ? target.id : "http:classifier",
        ...keepName,
        settings: {
          url: rt.baseUrl,
          credentials_provider: providerId,
          allow_private_network: rt.allowPrivateNetwork,
          timeout_ms: input.timeout_ms || DEFAULT_HTTP_TIMEOUT_MS,
        },
      };
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
    const step: HttpStep = {
      type: "http",
      id: "http:test",
      settings: { url: rt.baseUrl, credentials_provider: providerId, allow_private_network: rt.allowPrivateNetwork, timeout_ms: DEFAULT_HTTP_TIMEOUT_MS },
    };
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
