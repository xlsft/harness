/**
 * DecisionService configuration (admin JSON API `GET/PUT /api/admin/decision`), stored in the
 * `decision_config` table. Shape (snake_case, as in the API):
 *
 *   {
 *     "providers": [                                   // the chain, in order
 *       {"type": "rules", "id": "rules"},
 *       {"type": "llm", "id": "llm", "settings": {"model"?, "timeout_ms"?, "max_tokens"?, "min_confidence"?}},
 *       {"type": "http", "id": "opa", "display_name"?, "settings": {"url", "bearer_token"?, "headers"?,
 *                                                                  "allow_private_network"?, "timeout_ms"?, "health_url"?}},
 *       {"type": "decision_model", "id": "decision_model", "display_name"?,     // typed decision model (decision-model.ts)
 *        "settings": {"endpoint"?: "openrouter" | "typesafe" | "<https URL>",  // default "openrouter"
 *                     "model"?,                // default "typesafe/jev-1.13" (openrouter), "jev-1.13.0" (typesafe)
 *                     "api_key"?,              // write-only
 *                     "api_key_provider"?,     // or: reuse the key of this gateway provider (same host only)
 *                     "allow_private_network"?, "timeout_ms"?: 3000,
 *                     "min_probability"?: 0.8, "min_margin"?: 0.15, "on_uncertain"?: "next" | "ask"}}
 *     ],
 *     "on_error": "ask" | "next",                      // default "ask"
 *     "cache_ttl_seconds": 600,                        // classifier decision cache; 0 disables
 *     "classifier_model": null,                        // catalog id; else the `classifier` slot, else `small`
 *     "rate_limit_per_min": 120,                       // Decide calls per user; 0 disables
 *     "rules": {"extra_deny": [{"pattern", "reason"?, "categories"?}], "extra_allow": ["regex"],
 *               "protected_branches": ["main", "master", "release*"], "deny_all_force_push": false}
 *   }
 *
 * Provider ids: "rules"; "llm" or "llm:<name>"; "http:<name>"; "decision_model" or "decision_model:<name>"
 * (a bare name is prefixed with the type).
 * Webhook secrets (`bearer_token`, `headers`) are write-only: stored encrypted, shown as `bearer_token_set`
 * and `header_names`; omitting them on PUT keeps the stored values, null clears them. The same holds for a
 * decision model's `api_key` (shown as `api_key_set`). `api_key_provider` names a gateway provider
 * (/api/admin/providers) whose key is used instead, read when deciding; its base URL must have the same
 * host as the decision endpoint, so the key never goes to another service.
 */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { decryptSecret, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { sha256 } from "../lib/ids.ts";
import { assertUrlAllowed } from "../lib/ssrf.ts";
import { findModel } from "../services/models.ts";
import { loadProviderRuntime } from "../services/providers.ts";
import {
  DECISION_MODEL_ENDPOINTS,
  DEFAULT_DECISION_MODEL_TIMEOUT_MS,
  DEFAULT_MIN_MARGIN,
  DEFAULT_MIN_PROBABILITY,
  decisionEndpointUrl,
  type OnUncertain,
} from "./decision-model.ts";
import { DEFAULT_HTTP_TIMEOUT_MS } from "./http.ts";
import { DEFAULT_LLM_TIMEOUT_MS, type LlmSettings } from "./llm.ts";
import { DEFAULT_PROTECTED_BRANCHES, RULE_CATEGORIES, type ExtraDenyRule, type RuleCategory } from "./rules.ts";
import { ProviderError, type ProviderType } from "./types.ts";

export type OnErrorPolicy = "ask" | "next";

export interface RulesExtras {
  extra_deny: ExtraDenyRule[];
  extra_allow: string[];
  protected_branches: string[];
  deny_all_force_push: boolean;
}

/** Stored http settings: secrets encrypted. */
export interface HttpSettingsStored {
  url: string;
  allow_private_network: boolean;
  timeout_ms: number;
  health_url?: string;
  bearer_token_enc?: string;
  headers_enc?: string;
}

/** Stored decision_model settings: the API key encrypted. */
export interface DecisionModelSettingsStored {
  /** "openrouter", "typesafe" or a full URL. */
  endpoint: string;
  model: string;
  allow_private_network: boolean;
  timeout_ms: number;
  min_probability: number;
  min_margin: number;
  on_uncertain: OnUncertain;
  /** Gateway provider whose API key is used (instead of api_key_enc). */
  api_key_provider?: string;
  api_key_enc?: string;
}

export type StoredProvider =
  | { type: "rules"; id: string; display_name?: string; settings: Record<string, never> }
  | { type: "llm"; id: string; display_name?: string; settings: LlmSettings }
  | { type: "http"; id: string; display_name?: string; settings: HttpSettingsStored }
  | { type: "decision_model"; id: string; display_name?: string; settings: DecisionModelSettingsStored };

export interface DecisionConfig {
  providers: StoredProvider[];
  on_error: OnErrorPolicy;
  cache_ttl_seconds: number;
  classifier_model: string | null;
  rate_limit_per_min: number;
  rules: RulesExtras;
}

export const DEFAULT_CACHE_TTL_SECONDS = 600;
export const DEFAULT_RATE_LIMIT_PER_MIN = 120;
const ROW_ID = "default";
const MAX_PROVIDERS = 10;
const NAME_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export function defaultConfig(): DecisionConfig {
  return {
    providers: [
      { type: "rules", id: "rules", settings: {} },
      { type: "llm", id: "llm", settings: {} },
    ],
    on_error: "ask",
    cache_ttl_seconds: DEFAULT_CACHE_TTL_SECONDS,
    classifier_model: null,
    rate_limit_per_min: DEFAULT_RATE_LIMIT_PER_MIN,
    rules: { extra_deny: [], extra_allow: [], protected_branches: [...DEFAULT_PROTECTED_BRANCHES], deny_all_force_push: false },
  };
}

// --- loading -------------------------------------------------------------------------------

interface Loaded {
  config: DecisionConfig;
  configured: boolean;
  /** Changes whenever the stored config changes (used to rebuild the chain). */
  version: string;
}

let cached: { value: Loaded; at: number } | undefined;
/** Other replicas pick up admin changes within this time. */
const RELOAD_MS = 5_000;

export async function loadDecisionConfig(): Promise<Loaded> {
  if (cached && Date.now() - cached.at < RELOAD_MS) return cached.value;
  await ensureDb();
  const row = await getDb().query.decisionConfig.findFirst({ where: eq(schema.decisionConfig.id, ROW_ID) });
  const value: Loaded = row
    ? { config: { ...defaultConfig(), ...(row.config as DecisionConfig) }, configured: true, version: sha256(JSON.stringify(row.config)) }
    : { config: defaultConfig(), configured: false, version: "default" };
  cached = { value, at: Date.now() };
  return value;
}

export function invalidateDecisionConfig(): void {
  cached = undefined;
}

/** Decrypted webhook secrets, only for building providers. */
export function httpSecrets(p: Extract<StoredProvider, { type: "http" }>): { bearerToken?: string; headers: Record<string, string> } {
  const s = p.settings;
  return {
    ...(s.bearer_token_enc ? { bearerToken: decryptSecret(s.bearer_token_enc, `decision:${p.id}:token`) } : {}),
    headers: s.headers_enc ? (JSON.parse(decryptSecret(s.headers_enc, `decision:${p.id}:headers`)) as Record<string, string>) : {},
  };
}

/**
 * The API key of a gateway provider, for a decision endpoint on the same host. Throws ProviderError("config")
 * when the provider is gone, has no key or lives on another host.
 */
export async function gatewayProviderKey(providerId: string, endpointUrl: string): Promise<string> {
  let rt: Awaited<ReturnType<typeof loadProviderRuntime>>;
  try {
    rt = await loadProviderRuntime(providerId);
  } catch {
    throw new ProviderError(`Gateway provider ${providerId} does not exist`, "config");
  }
  if (!rt.apiKey) throw new ProviderError(`Gateway provider ${providerId} has no API key`, "config");
  let same = false;
  try {
    same = new URL(rt.baseUrl).hostname.toLowerCase() === new URL(endpointUrl).hostname.toLowerCase();
  } catch {
    same = false;
  }
  if (!same) throw new ProviderError(`Gateway provider ${providerId} is on another host than the decision endpoint; its key is not sent there`, "config");
  return rt.apiKey;
}

/** Resolves a decision model's API key when deciding (decrypted secret or a gateway provider's key). */
export function decisionModelKeyResolver(p: Extract<StoredProvider, { type: "decision_model" }>): () => Promise<string | undefined> {
  const s = p.settings;
  if (s.api_key_provider) {
    const providerId = s.api_key_provider;
    const url = decisionEndpointUrl(s.endpoint);
    return () => gatewayProviderKey(providerId, url);
  }
  const key = s.api_key_enc ? decryptSecret(s.api_key_enc, `decision:${p.id}:api_key`) : undefined;
  return async () => key;
}

// --- public view -----------------------------------------------------------------------------

export function publicConfig(c: DecisionConfig) {
  return {
    providers: c.providers.map((p) => {
      if (p.type === "decision_model") {
        const { api_key_enc, ...rest } = p.settings;
        return { type: p.type, id: p.id, ...(p.display_name ? { display_name: p.display_name } : {}), settings: { ...rest, api_key_set: !!api_key_enc } };
      }
      if (p.type !== "http") return { type: p.type, id: p.id, ...(p.display_name ? { display_name: p.display_name } : {}), settings: p.settings };
      const { bearer_token_enc, headers_enc, ...rest } = p.settings;
      let headerNames: string[] = [];
      if (headers_enc) headerNames = Object.keys(httpSecrets(p).headers);
      return {
        type: p.type,
        id: p.id,
        ...(p.display_name ? { display_name: p.display_name } : {}),
        settings: { ...rest, bearer_token_set: !!bearer_token_enc, header_names: headerNames },
      };
    }),
    on_error: c.on_error,
    cache_ttl_seconds: c.cache_ttl_seconds,
    classifier_model: c.classifier_model,
    rate_limit_per_min: c.rate_limit_per_min,
    rules: c.rules,
  };
}

export async function getDecisionConfigAdmin() {
  const l = await loadDecisionConfig();
  return { config: publicConfig(l.config), configured: l.configured };
}

// --- validation and saving -------------------------------------------------------------------

const bad = (msg: string) => new ApiError("invalid_request", msg);

function obj(v: unknown, field: string): Record<string, unknown> {
  if (v === undefined || v === null) return {};
  if (typeof v !== "object" || Array.isArray(v)) throw bad(`${field} must be an object`);
  return v as Record<string, unknown>;
}

function onlyKeys(o: Record<string, unknown>, allowed: string[], field: string) {
  for (const k of Object.keys(o)) if (!allowed.includes(k)) throw bad(`Unknown field ${field}.${k}`);
}

function int(v: unknown, field: string, min: number, max: number, def: number): number {
  if (v === undefined || v === null) return def;
  if (typeof v !== "number" || !Number.isInteger(v) || v < min || v > max) throw bad(`${field} must be an integer between ${min} and ${max}`);
  return v;
}

function bool(v: unknown, field: string, def: boolean): boolean {
  if (v === undefined || v === null) return def;
  if (typeof v !== "boolean") throw bad(`${field} must be a boolean`);
  return v;
}

function str(v: unknown, field: string): string | undefined {
  if (v === undefined || v === null || v === "") return undefined;
  if (typeof v !== "string") throw bad(`${field} must be a string`);
  return v;
}

function regex(p: unknown, field: string): string {
  if (typeof p !== "string" || !p) throw bad(`${field} must be a non-empty regular expression`);
  try {
    new RegExp(p);
  } catch (e) {
    throw bad(`${field} is not a valid regular expression: ${(e as Error).message}`);
  }
  const js = jsOnlySyntax(p);
  if (js) throw bad(`${field} uses ${js}, which RE2 does not support (clients evaluate rules with RE2)`);
  return p;
}

/** Names the first JavaScript-only construct in p that RE2 rejects (lookaround, backreference), or "". */
export function jsOnlySyntax(p: string): string {
  let inClass = false;
  for (let i = 0; i < p.length; i++) {
    const c = p[i];
    if (c === "\\") {
      const n = p[i + 1] ?? "";
      if (!inClass && /[1-9]/.test(n)) return "a backreference";
      if (!inClass && n === "k" && p[i + 2] === "<") return "a named backreference";
      i++;
      continue;
    }
    if (inClass) {
      if (c === "]") inClass = false;
      continue;
    }
    if (c === "[") {
      inClass = true;
      if (p[i + 1] === "]" || (p[i + 1] === "^" && p[i + 2] === "]")) i += p[i + 1] === "^" ? 2 : 1;
      continue;
    }
    if (c === "(" && p[i + 1] === "?") {
      const a = p.slice(i + 2, i + 4);
      if (a[0] === "=" || a[0] === "!") return "a lookahead";
      if (a === "<=" || a === "<!") return "a lookbehind";
    }
  }
  return "";
}

function categories(v: unknown, field: string): RuleCategory[] {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v)) throw bad(`${field} must be an array`);
  return v.map((c, i) => {
    const n = typeof c === "string" ? c.toLowerCase().replace(/^risk_category_/, "") : "";
    if (!RULE_CATEGORIES.includes(n as RuleCategory)) throw bad(`${field}[${i}] must be one of ${RULE_CATEGORIES.join(", ")}`);
    return n as RuleCategory;
  });
}

function validateRules(v: unknown): RulesExtras {
  const o = obj(v, "rules");
  onlyKeys(o, ["extra_deny", "extra_allow", "protected_branches", "deny_all_force_push"], "rules");
  const deny = o.extra_deny ?? [];
  const allow = o.extra_allow ?? [];
  const branches = o.protected_branches ?? DEFAULT_PROTECTED_BRANCHES;
  if (!Array.isArray(deny)) throw bad("rules.extra_deny must be an array");
  if (!Array.isArray(allow)) throw bad("rules.extra_allow must be an array");
  if (!Array.isArray(branches) || branches.some((b) => typeof b !== "string" || !b)) throw bad("rules.protected_branches must be an array of branch globs");
  return {
    extra_deny: deny.map((d, i) => {
      const r = obj(d, `rules.extra_deny[${i}]`);
      onlyKeys(r, ["pattern", "reason", "categories"], `rules.extra_deny[${i}]`);
      const reason = str(r.reason, `rules.extra_deny[${i}].reason`);
      return {
        pattern: regex(r.pattern, `rules.extra_deny[${i}].pattern`),
        ...(reason ? { reason } : {}),
        categories: categories(r.categories, `rules.extra_deny[${i}].categories`),
      };
    }),
    extra_allow: allow.map((p, i) => regex(p, `rules.extra_allow[${i}]`)),
    protected_branches: branches as string[],
    deny_all_force_push: bool(o.deny_all_force_push, "rules.deny_all_force_push", false),
  };
}

function normalizeId(type: ProviderType, raw: unknown, index: number): string {
  const field = `providers[${index}].id`;
  if (type === "rules") {
    if (raw !== undefined && raw !== null && raw !== "rules") throw bad(`${field} must be "rules"`);
    return "rules";
  }
  // llm and decision_model may use the bare type as id (one of each is the common case).
  const bareOk = type === "llm" || type === "decision_model";
  const given = raw === undefined || raw === null || raw === "" ? (bareOk ? type : undefined) : raw;
  if (typeof given !== "string") throw bad(`${field} is required`);
  let name: string = given;
  if (name.startsWith(`${type}:`)) name = name.slice(type.length + 1);
  if (bareOk && name === type) return type;
  if (!NAME_RE.test(name)) throw bad(`${field} must match [a-z0-9][a-z0-9._-]*`);
  return `${type}:${name}`;
}

async function validateProvider(raw: unknown, index: number, previous: DecisionConfig | undefined): Promise<StoredProvider> {
  const field = `providers[${index}]`;
  const o = obj(raw, field);
  onlyKeys(o, ["type", "id", "display_name", "settings"], field);
  const type = o.type;
  if (type !== "rules" && type !== "llm" && type !== "http" && type !== "decision_model") {
    throw bad(`${field}.type must be rules, llm, http or decision_model`);
  }
  const id = normalizeId(type, o.id, index);
  const display_name = str(o.display_name, `${field}.display_name`);
  const settings = obj(o.settings, `${field}.settings`);
  const base = { id, ...(display_name ? { display_name } : {}) };
  if (type === "rules") {
    onlyKeys(settings, [], `${field}.settings`);
    return { type, ...base, settings: {} };
  }
  if (type === "llm") {
    onlyKeys(settings, ["model", "timeout_ms", "max_tokens", "min_confidence"], `${field}.settings`);
    const model = str(settings.model, `${field}.settings.model`);
    if (model && !(await findModel(model))) throw bad(`Unknown model ${model} in ${field}.settings.model`);
    const s: LlmSettings = {
      ...(model ? { model } : {}),
      timeout_ms: int(settings.timeout_ms, `${field}.settings.timeout_ms`, 100, 120_000, DEFAULT_LLM_TIMEOUT_MS),
    };
    if (settings.max_tokens !== undefined) s.max_tokens = int(settings.max_tokens, `${field}.settings.max_tokens`, 64, 32_000, 1024);
    if (settings.min_confidence !== undefined) {
      const m = settings.min_confidence;
      if (typeof m !== "number" || m < 0 || m > 1) throw bad(`${field}.settings.min_confidence must be between 0 and 1`);
      s.min_confidence = m;
    }
    return { type, ...base, settings: s };
  }
  if (type === "decision_model") return { type, ...base, settings: await validateDecisionModel(settings, field, id, previous) };
  onlyKeys(settings, ["url", "bearer_token", "headers", "allow_private_network", "timeout_ms", "health_url"], `${field}.settings`);
  const allowPrivate = bool(settings.allow_private_network, `${field}.settings.allow_private_network`, false);
  const url = str(settings.url, `${field}.settings.url`);
  if (!url) throw bad(`${field}.settings.url is required`);
  assertUrlAllowed(url, allowPrivate);
  const healthUrl = str(settings.health_url, `${field}.settings.health_url`);
  if (healthUrl) assertUrlAllowed(healthUrl, allowPrivate);
  const prev = previous?.providers.find((p): p is Extract<StoredProvider, { type: "http" }> => p.type === "http" && p.id === id);
  const s: HttpSettingsStored = {
    url,
    allow_private_network: allowPrivate,
    timeout_ms: int(settings.timeout_ms, `${field}.settings.timeout_ms`, 100, 60_000, DEFAULT_HTTP_TIMEOUT_MS),
    ...(healthUrl ? { health_url: healthUrl } : {}),
  };
  if (settings.bearer_token === undefined) {
    if (prev?.settings.bearer_token_enc) s.bearer_token_enc = prev.settings.bearer_token_enc;
  } else if (settings.bearer_token !== null && settings.bearer_token !== "") {
    if (typeof settings.bearer_token !== "string") throw bad(`${field}.settings.bearer_token must be a string`);
    s.bearer_token_enc = encryptSecret(settings.bearer_token, `decision:${id}:token`);
  }
  if (settings.headers === undefined) {
    if (prev?.settings.headers_enc) s.headers_enc = prev.settings.headers_enc;
  } else if (settings.headers !== null) {
    const h = obj(settings.headers, `${field}.settings.headers`);
    for (const [k, v] of Object.entries(h)) {
      if (!/^[A-Za-z0-9-]+$/.test(k) || typeof v !== "string") throw bad(`Invalid header ${k} in ${field}.settings.headers`);
    }
    if (Object.keys(h).length) s.headers_enc = encryptSecret(JSON.stringify(h), `decision:${id}:headers`);
  }
  return { type, ...base, settings: s };
}

function prob(v: unknown, field: string, def: number): number {
  if (v === undefined || v === null) return def;
  if (typeof v !== "number" || !Number.isFinite(v) || v < 0 || v > 1) throw bad(`${field} must be a number between 0 and 1`);
  return v;
}

async function validateDecisionModel(
  settings: Record<string, unknown>,
  field: string,
  id: string,
  previous: DecisionConfig | undefined,
): Promise<DecisionModelSettingsStored> {
  const f = `${field}.settings`;
  onlyKeys(
    settings,
    ["endpoint", "model", "api_key", "api_key_provider", "allow_private_network", "timeout_ms", "min_probability", "min_margin", "on_uncertain"],
    f,
  );
  const allowPrivate = bool(settings.allow_private_network, `${f}.allow_private_network`, false);
  const endpoint = str(settings.endpoint, `${f}.endpoint`) ?? "openrouter";
  const named = endpoint in DECISION_MODEL_ENDPOINTS ? DECISION_MODEL_ENDPOINTS[endpoint as keyof typeof DECISION_MODEL_ENDPOINTS] : undefined;
  if (!named) {
    if (!/^https?:\/\//i.test(endpoint)) throw bad(`${f}.endpoint must be "openrouter", "typesafe" or an https URL`);
    const url = assertUrlAllowed(endpoint, allowPrivate);
    if (url.protocol !== "https:" && !allowPrivate) throw bad(`${f}.endpoint must use https (plain http needs allow_private_network)`);
  }
  const model = str(settings.model, `${f}.model`) ?? named?.defaultModel;
  if (!model) throw bad(`${f}.model is required for a custom endpoint`);
  if (model.length > 200) throw bad(`${f}.model is too long`);
  const onUncertain = settings.on_uncertain ?? "next";
  if (onUncertain !== "next" && onUncertain !== "ask") throw bad(`${f}.on_uncertain must be "next" or "ask"`);
  const s: DecisionModelSettingsStored = {
    endpoint,
    model,
    allow_private_network: allowPrivate,
    timeout_ms: int(settings.timeout_ms, `${f}.timeout_ms`, 100, 60_000, DEFAULT_DECISION_MODEL_TIMEOUT_MS),
    min_probability: prob(settings.min_probability, `${f}.min_probability`, DEFAULT_MIN_PROBABILITY),
    min_margin: prob(settings.min_margin, `${f}.min_margin`, DEFAULT_MIN_MARGIN),
    on_uncertain: onUncertain,
  };
  const keyProvider = str(settings.api_key_provider, `${f}.api_key_provider`);
  const keyGiven = settings.api_key !== undefined && settings.api_key !== null && settings.api_key !== "";
  if (keyProvider) {
    if (keyGiven) throw bad(`${f}: set api_key or api_key_provider, not both`);
    try {
      await gatewayProviderKey(keyProvider, decisionEndpointUrl(endpoint));
    } catch (e) {
      throw bad(`${f}.api_key_provider: ${(e as Error).message}`);
    }
    s.api_key_provider = keyProvider;
  } else if (keyGiven) {
    if (typeof settings.api_key !== "string") throw bad(`${f}.api_key must be a string`);
    s.api_key_enc = encryptSecret(settings.api_key, `decision:${id}:api_key`);
  } else if (settings.api_key === undefined) {
    const prev = previous?.providers.find((p): p is Extract<StoredProvider, { type: "decision_model" }> => p.type === "decision_model" && p.id === id);
    if (prev?.settings.api_key_enc) s.api_key_enc = prev.settings.api_key_enc;
  }
  if (named && !s.api_key_enc && !s.api_key_provider) throw bad(`${f}: api_key or api_key_provider is required for the ${endpoint} endpoint`);
  return s;
}

export async function validateDecisionConfig(input: unknown, previous?: DecisionConfig): Promise<DecisionConfig> {
  const o = obj(input, "body");
  onlyKeys(o, ["providers", "on_error", "cache_ttl_seconds", "classifier_model", "rate_limit_per_min", "rules"], "body");
  const def = defaultConfig();
  let providers = def.providers;
  if (o.providers !== undefined) {
    if (!Array.isArray(o.providers)) throw bad("providers must be an array");
    if (o.providers.length > MAX_PROVIDERS) throw bad(`At most ${MAX_PROVIDERS} providers`);
    providers = [];
    for (const [i, p] of o.providers.entries()) providers.push(await validateProvider(p, i, previous));
    const ids = new Set<string>();
    for (const p of providers) {
      if (ids.has(p.id)) throw bad(`Duplicate provider id ${p.id}`);
      ids.add(p.id);
    }
  }
  const onError = o.on_error ?? "ask";
  if (onError !== "ask" && onError !== "next") throw bad('on_error must be "ask" or "next"');
  const classifier = str(o.classifier_model, "classifier_model") ?? null;
  if (classifier && !(await findModel(classifier))) throw bad(`Unknown model ${classifier} in classifier_model`);
  return {
    providers,
    on_error: onError,
    cache_ttl_seconds: int(o.cache_ttl_seconds, "cache_ttl_seconds", 0, 7 * 24 * 3600, DEFAULT_CACHE_TTL_SECONDS),
    classifier_model: classifier,
    rate_limit_per_min: int(o.rate_limit_per_min, "rate_limit_per_min", 0, 100_000, DEFAULT_RATE_LIMIT_PER_MIN),
    rules: validateRules(o.rules),
  };
}

/** PUT semantics: replaces the whole config (secrets of kept webhooks carry over when omitted). */
export async function setDecisionConfig(input: unknown, updatedBy?: string) {
  const previous = (await loadDecisionConfig()).config;
  const config = await validateDecisionConfig(input, previous);
  const now = new Date();
  await ensureDb();
  await getDb()
    .insert(schema.decisionConfig)
    .values({ id: ROW_ID, config, updatedAt: now, updatedBy: updatedBy ?? null })
    .onConflictDoUpdate({ target: schema.decisionConfig.id, set: { config, updatedAt: now, updatedBy: updatedBy ?? null } });
  invalidateDecisionConfig();
  return { config: publicConfig(config), configured: true };
}
