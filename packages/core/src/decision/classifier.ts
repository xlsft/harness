/**
 * The settings view of the auto-mode classifier: the chain's `decision_model` step (a typed decision model
 * such as Jev, paid with a global provider's key through `api_key_provider`) plus the model of the `llm`
 * step. A narrow, UI-shaped layer over the full chain config in config.ts: reading picks the first
 * decision_model step; writing replaces (or inserts, right after `rules`) that step and keeps every other
 * step and setting as stored, secrets included.
 */
import { ApiError } from "../lib/errors.ts";
import { loadProviderRuntime } from "../services/providers.ts";
import { loadDecisionConfig, setDecisionConfig, type DecisionConfig, type StoredProvider } from "./config.ts";
import { DECISION_MODEL_ENDPOINTS, DEFAULT_DECISION_MODEL_TIMEOUT_MS, DEFAULT_MIN_MARGIN, DEFAULT_MIN_PROBABILITY, type OnUncertain } from "./decision-model.ts";

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

function firstDecisionModel(c: DecisionConfig): DecisionModelStep | undefined {
  return c.providers.find((p): p is DecisionModelStep => p.type === "decision_model");
}

export async function getClassifier(): Promise<ClassifierSettings> {
  const { config } = await loadDecisionConfig();
  const dm = firstDecisionModel(config);
  const s = dm?.settings;
  return {
    enabled: !!dm,
    provider_id: s?.api_key_provider ?? "",
    endpoint: s?.endpoint ?? "",
    model: s?.model ?? "",
    min_probability: s?.min_probability ?? DEFAULT_MIN_PROBABILITY,
    min_margin: s?.min_margin ?? DEFAULT_MIN_MARGIN,
    on_uncertain: s?.on_uncertain ?? "next",
    timeout_ms: s?.timeout_ms ?? DEFAULT_DECISION_MODEL_TIMEOUT_MS,
    llm_model: config.classifier_model ?? "",
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

export async function setClassifier(input: Partial<ClassifierSettings>, updatedBy?: string): Promise<ClassifierSettings> {
  const { config } = await loadDecisionConfig();
  const target = firstDecisionModel(config);
  const steps = config.providers.map(asInput);
  const at = target ? config.providers.indexOf(target) : -1;

  if (input.enabled) {
    const providerId = input.provider_id?.trim();
    if (!providerId) throw new ApiError("invalid_request", "provider_id is required");
    let rt: Awaited<ReturnType<typeof loadProviderRuntime>> | undefined;
    try {
      rt = await loadProviderRuntime(providerId);
    } catch {
      rt = undefined;
    }
    // Same answer for a missing and a personal provider: the chain only uses global keys.
    if (!rt || rt.ownerUserId) throw new ApiError("invalid_request", `Unknown global provider ${providerId}`);
    const endpoint = input.endpoint?.trim() || endpointForBaseUrl(rt.baseUrl);
    if (!endpoint) throw new ApiError("invalid_request", `Provider ${providerId} serves no known decision endpoint; set endpoint`);
    const step = {
      type: "decision_model",
      id: target?.id ?? "decision_model",
      ...(target?.display_name ? { display_name: target.display_name } : {}),
      settings: {
        endpoint,
        ...(input.model?.trim() ? { model: input.model.trim() } : {}),
        api_key_provider: providerId,
        ...(target?.settings.allow_private_network ? { allow_private_network: true } : {}),
        min_probability: input.min_probability ?? DEFAULT_MIN_PROBABILITY,
        min_margin: input.min_margin ?? DEFAULT_MIN_MARGIN,
        on_uncertain: input.on_uncertain || "next",
        timeout_ms: input.timeout_ms || DEFAULT_DECISION_MODEL_TIMEOUT_MS,
      },
    };
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
