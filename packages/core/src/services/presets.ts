/**
 * Provider presets: well-known services the settings screens connect in one step (the user enters only what
 * the service needs, usually its API key), served by ProviderService.ListPresets so a new preset needs no
 * app release. Everything else is a custom provider, where the user fills in every field.
 *
 * Each base URL and protocol below was checked against the service's own documentation (November 2026).
 * Base URL conventions follow the gateway adapters: an "anthropic" base URL has no /v1 (the adapter appends
 * /v1/messages), an OpenAI-compatible one ends where /chat/completions, /responses and /models are appended.
 * A classifier preset's base URL is the full decision endpoint (decision-model.ts).
 *
 * `discovers_models`: the service documents a model list (GET /models); the others ship a curated list
 * (`default_models`) the user picks from instead.
 *
 * `billing` (LLM presets): "plan" marks a flat-rate subscription / coding plan. Usage on such a provider
 * counts tokens only and never cost, even for a model with a catalog price; everything else is "metered"
 * (pay as you go), where a priced model's requests cost what the price says. Custom providers are metered.
 *
 * Search presets (group "search") name the adapter in src/search/adapters.ts as their type; their base URL is
 * the API's endpoint (SearXNG: the instance root). `options` are the settings a connected search provider
 * may change, only those the API documents. The built-in SearXNG (`builtin`) is not stored: it is the
 * instance the server ships with (HARNESS_SEARXNG_URL), always offered, and its provider id is its preset id.
 */

export type ProviderGroup = "llm" | "classifier" | "search";
/** How an LLM provider charges: "metered" (pay as you go) or "plan" (flat-rate subscription / coding plan). */
export type Billing = "metered" | "plan";

export const LLM_TYPES = ["anthropic", "openai-chat", "openai-responses"] as const;
export const CLASSIFIER_TYPES = ["decision-model", "decision-http"] as const;
export const SEARCH_TYPES = ["searxng", "brave", "tavily", "exa", "kagi", "perplexity"] as const;
export type SearchType = (typeof SEARCH_TYPES)[number];
/** Provider id of the built-in SearXNG (reserved: no stored provider may use it). */
export const BUILTIN_SEARCH_ID = "searxng";

export interface PresetField {
  name: "api_key" | "base_url";
  label: string;
  required: boolean;
  placeholder?: string;
}

export interface PresetModel {
  upstream_model: string;
  display_name: string;
  context_window?: number;
  max_output_tokens?: number;
}

export type SearchOptionName = "max_results" | "safe_search" | "country" | "language";

export interface PresetOption {
  name: SearchOptionName;
  label: string;
  kind: "number" | "select" | "text";
  choices?: { value: string; label: string }[];
  default_value?: string;
  placeholder?: string;
  min?: number;
  max?: number;
}

export interface ProviderPreset {
  id: string;
  group: ProviderGroup;
  display_name: string;
  description: string;
  type: string;
  base_url: string;
  fields: PresetField[];
  docs_url?: string;
  key_url?: string;
  default_models: PresetModel[];
  icon: string;
  discovers_models: boolean;
  local?: boolean;
  options?: PresetOption[];
  builtin?: boolean;
  /** Default "metered". */
  billing?: Billing;
}

export interface ProviderProtocol {
  id: string;
  group: ProviderGroup;
  label: string;
  base_url_hint: string;
}

const KEY: PresetField = { name: "api_key", label: "API key", required: true };

// --- Search options -------------------------------------------------------------------------------
const resultsOption = (max: number): PresetOption => ({ name: "max_results", label: "Results", kind: "number", default_value: "10", min: 1, max });
const SAFE_3: PresetOption = {
  name: "safe_search",
  label: "Safe search",
  kind: "select",
  choices: [
    { value: "off", label: "Off" },
    { value: "moderate", label: "Moderate" },
    { value: "strict", label: "Strict" },
  ],
  default_value: "moderate",
};
// APIs with an on/off switch only.
const SAFE_2: PresetOption = {
  name: "safe_search",
  label: "Safe search",
  kind: "select",
  choices: [
    { value: "off", label: "Off" },
    { value: "strict", label: "On" },
  ],
  default_value: "off",
};
const COUNTRY: PresetOption = { name: "country", label: "Country", kind: "text", placeholder: "Two-letter code, e.g. US" };
const LANGUAGE: PresetOption = { name: "language", label: "Language", kind: "text", placeholder: "Two-letter code, e.g. en" };

const KIMI_PLATFORM_MODELS: PresetModel[] = [
  { upstream_model: "kimi-k3.1-pro", display_name: "Kimi K3.1 Pro", context_window: 1_000_000 },
  { upstream_model: "kimi-k3.1-pro-highspeed", display_name: "Kimi K3.1 Pro HighSpeed" },
  { upstream_model: "kimi-k3", display_name: "Kimi K3", context_window: 1_000_000 },
  { upstream_model: "kimi-k2.7-code", display_name: "Kimi K2.7 Code", context_window: 256_000 },
];

export const PRESETS: readonly ProviderPreset[] = [
  // --- LLM models -----------------------------------------------------------------------------
  {
    id: "openrouter",
    group: "llm",
    display_name: "OpenRouter",
    description: "Hundreds of models from every major lab behind one key.",
    type: "openai-chat",
    base_url: "https://openrouter.ai/api/v1",
    fields: [KEY],
    docs_url: "https://openrouter.ai/docs/quickstart",
    key_url: "https://openrouter.ai/settings/keys",
    default_models: [],
    icon: "openrouter",
    discovers_models: true,
  },
  {
    id: "anthropic",
    group: "llm",
    display_name: "Anthropic",
    description: "Claude models through the Claude API.",
    type: "anthropic",
    base_url: "https://api.anthropic.com",
    fields: [KEY],
    docs_url: "https://platform.claude.com/docs/en/api/overview",
    key_url: "https://platform.claude.com/settings/keys",
    default_models: [
      { upstream_model: "claude-opus-5-5", display_name: "Claude Opus 5.5", context_window: 1_000_000, max_output_tokens: 128_000 },
      { upstream_model: "claude-sonnet-5-5", display_name: "Claude Sonnet 5.5", context_window: 1_000_000, max_output_tokens: 128_000 },
      { upstream_model: "claude-haiku-5-5", display_name: "Claude Haiku 5.5", context_window: 1_000_000, max_output_tokens: 128_000 },
    ],
    icon: "anthropic",
    discovers_models: true,
  },
  {
    id: "openai",
    group: "llm",
    display_name: "OpenAI",
    description: "GPT models through the Responses API.",
    type: "openai-responses",
    base_url: "https://api.openai.com/v1",
    fields: [KEY],
    docs_url: "https://developers.openai.com/api/reference/overview",
    key_url: "https://platform.openai.com/settings/organization/api-keys",
    default_models: [
      { upstream_model: "gpt-6-astra", display_name: "GPT-6 Astra" },
      { upstream_model: "gpt-6.1-sol", display_name: "GPT-6.1 Sol" },
      { upstream_model: "gpt-6-luna", display_name: "GPT-6 Luna" },
    ],
    icon: "openai",
    discovers_models: true,
  },
  {
    id: "kimi-code",
    group: "llm",
    display_name: "Kimi Code (kimi.com)",
    description: "The kimi.com coding subscription, mainland China (Anthropic-compatible endpoint).",
    type: "anthropic",
    base_url: "https://api.kimi.com/coding",
    fields: [KEY],
    docs_url: "https://www.kimi.com/code/docs/en/",
    key_url: "https://www.kimi.com/code/console",
    default_models: [
      { upstream_model: "kimi-for-coding", display_name: "Kimi for Coding" },
      { upstream_model: "kimi-for-coding-highspeed", display_name: "Kimi for Coding HighSpeed" },
      { upstream_model: "k3.1-pro", display_name: "K3.1 Pro" },
      { upstream_model: "k3.1-pro-highspeed", display_name: "K3.1 Pro HighSpeed" },
      { upstream_model: "k3", display_name: "K3" },
      { upstream_model: "k3-256k", display_name: "K3 256k", context_window: 256_000 },
    ],
    icon: "kimi",
    discovers_models: false,
    billing: "plan",
  },
  {
    id: "kimi-code-global",
    group: "llm",
    display_name: "Kimi Code (kimi.ai)",
    description: "The kimi.ai coding subscription, international (Anthropic-compatible endpoint).",
    type: "anthropic",
    base_url: "https://api.kimi.ai/coding",
    fields: [KEY],
    docs_url: "https://www.kimi.com/code/docs/en/",
    key_url: "https://www.kimi.ai/code/console",
    default_models: [
      { upstream_model: "kimi-for-coding", display_name: "Kimi for Coding" },
      { upstream_model: "kimi-for-coding-highspeed", display_name: "Kimi for Coding HighSpeed" },
      { upstream_model: "k3.1-pro", display_name: "K3.1 Pro" },
      { upstream_model: "k3.1-pro-highspeed", display_name: "K3.1 Pro HighSpeed" },
      { upstream_model: "k3", display_name: "K3" },
      { upstream_model: "k3-256k", display_name: "K3 256k", context_window: 256_000 },
    ],
    icon: "kimi",
    discovers_models: false,
    billing: "plan",
  },
  {
    id: "moonshot",
    group: "llm",
    display_name: "Kimi Platform",
    description: "Moonshot AI's Kimi API, international (platform.moonshot.ai).",
    type: "openai-chat",
    base_url: "https://api.moonshot.ai/v1",
    fields: [KEY],
    docs_url: "https://platform.kimi.ai/docs/guide/start-using-kimi-api",
    default_models: KIMI_PLATFORM_MODELS,
    icon: "moonshot",
    discovers_models: false,
  },
  {
    id: "moonshot-cn",
    group: "llm",
    display_name: "Kimi Platform China",
    description: "Moonshot AI's Kimi API for mainland China (platform.moonshot.cn).",
    type: "openai-chat",
    base_url: "https://api.moonshot.cn/v1",
    fields: [KEY],
    docs_url: "https://platform.kimi.com/docs/guide/start-using-kimi-api",
    key_url: "https://platform.kimi.com/console/api-keys",
    default_models: KIMI_PLATFORM_MODELS,
    icon: "moonshot",
    discovers_models: false,
  },
  {
    id: "zai",
    group: "llm",
    display_name: "Z.ai",
    description: "GLM models through the Z.ai API (pay as you go).",
    type: "openai-chat",
    base_url: "https://api.z.ai/api/paas/v4",
    fields: [KEY],
    docs_url: "https://docs.z.ai/guides/overview/quick-start",
    default_models: [
      { upstream_model: "glm-5.3", display_name: "GLM-5.3", context_window: 1_000_000, max_output_tokens: 128_000 },
      { upstream_model: "glm-5.3-flash", display_name: "GLM-5.3 Flash" },
    ],
    icon: "zai",
    discovers_models: false,
  },
  {
    id: "zai-coding",
    group: "llm",
    display_name: "Z.ai Coding Plan",
    description: "The GLM Coding Plan subscription (Anthropic-compatible endpoint).",
    type: "anthropic",
    base_url: "https://api.z.ai/api/anthropic",
    fields: [KEY],
    docs_url: "https://docs.z.ai/devpack/tool/claude",
    default_models: [
      { upstream_model: "glm-5.3", display_name: "GLM-5.3", context_window: 1_000_000, max_output_tokens: 128_000 },
      { upstream_model: "glm-5.3-flash", display_name: "GLM-5.3 Flash" },
    ],
    icon: "zai",
    discovers_models: false,
    billing: "plan",
  },
  {
    id: "deepseek",
    group: "llm",
    display_name: "DeepSeek",
    description: "DeepSeek models through the DeepSeek API.",
    type: "openai-chat",
    base_url: "https://api.deepseek.com",
    fields: [KEY],
    docs_url: "https://api-docs.deepseek.com/",
    default_models: [
      { upstream_model: "deepseek-flash", display_name: "DeepSeek V4.1 Flash" },
      { upstream_model: "deepseek-v4-pro", display_name: "DeepSeek V4 Pro" },
    ],
    icon: "deepseek",
    discovers_models: true,
  },
  {
    id: "gemini",
    group: "llm",
    display_name: "Google Gemini",
    description: "Gemini models through the Gemini API's OpenAI-compatible endpoint.",
    type: "openai-chat",
    base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
    fields: [KEY],
    docs_url: "https://aistudio.google.com/docs/openai",
    key_url: "https://aistudio.google.com/apikey",
    default_models: [{ upstream_model: "gemini-3.8-flash", display_name: "Gemini 3.8 Flash" }],
    icon: "gemini",
    discovers_models: true,
  },
  {
    id: "xai",
    group: "llm",
    display_name: "xAI",
    description: "Grok models through the xAI API.",
    type: "openai-responses",
    base_url: "https://api.x.ai/v1",
    fields: [KEY],
    docs_url: "https://docs.x.ai/docs/models",
    key_url: "https://console.x.ai/team/default/api-keys",
    default_models: [
      { upstream_model: "grok-4.7", display_name: "Grok 4.7" },
      { upstream_model: "grok-4.6", display_name: "Grok 4.6" },
      { upstream_model: "grok-build-0.1", display_name: "Grok Build 0.1" },
    ],
    icon: "xai",
    discovers_models: false,
  },
  {
    id: "ollama",
    group: "llm",
    display_name: "Ollama",
    description: "Models served by Ollama on your network.",
    type: "openai-chat",
    base_url: "http://localhost:11434/v1",
    fields: [
      { name: "base_url", label: "Server URL", required: true, placeholder: "http://localhost:11434/v1" },
      { name: "api_key", label: "API key (optional)", required: false },
    ],
    docs_url: "https://docs.ollama.com/api/openai-compatibility",
    default_models: [],
    icon: "ollama",
    discovers_models: true,
    local: true,
  },

  // --- Classifiers ----------------------------------------------------------------------------
  {
    id: "openrouter-decisions",
    group: "classifier",
    display_name: "OpenRouter decisions",
    description: "Decision model through OpenRouter's decisions endpoint.",
    type: "decision-model",
    base_url: "https://openrouter.ai/api/alpha/decisions",
    fields: [KEY],
    key_url: "https://openrouter.ai/settings/keys",
    default_models: [{ upstream_model: "typesafe/jev-1.13", display_name: "Jev 1.13" }],
    icon: "openrouter",
    discovers_models: false,
  },
  {
    id: "typesafe",
    group: "classifier",
    display_name: "TypeSafe",
    description: "The Jev decision model from TypeSafe's own API.",
    type: "decision-model",
    base_url: "https://api.typesafe.ai/v1/systemone",
    fields: [KEY],
    default_models: [{ upstream_model: "jev-1.13.0", display_name: "Jev 1.13" }],
    icon: "TS",
    discovers_models: false,
  },

  // --- Web search -----------------------------------------------------------------------------
  // Verified November 2026 against each API reference (docs_url). Serper and Jina were left out: their
  // official docs do not document the request (Serper) or the JSON response (Jina search).
  {
    id: BUILTIN_SEARCH_ID,
    group: "search",
    display_name: "SearXNG (built in)",
    description: "The metasearch engine that ships with the server. No key needed.",
    type: "searxng",
    base_url: "",
    fields: [],
    docs_url: "https://docs.searxng.org/dev/search_api.html",
    default_models: [],
    icon: "searxng",
    discovers_models: false,
    builtin: true,
  },
  {
    id: "searxng-url",
    group: "search",
    display_name: "SearXNG",
    description: "Your own SearXNG instance (the JSON format must be enabled).",
    type: "searxng",
    base_url: "",
    fields: [{ name: "base_url", label: "Instance URL", required: true, placeholder: "https://searx.example.com" }],
    docs_url: "https://docs.searxng.org/dev/search_api.html",
    default_models: [],
    icon: "searxng",
    discovers_models: false,
    local: true,
    options: [resultsOption(25), SAFE_3, { ...LANGUAGE, placeholder: "e.g. en, or all" }],
  },
  {
    id: "brave",
    group: "search",
    display_name: "Brave Search",
    description: "Brave's independent web index through the Brave Search API.",
    type: "brave",
    base_url: "https://api.search.brave.com/res/v1/web/search",
    fields: [KEY],
    docs_url: "https://api-dashboard.search.brave.com/app/documentation/web-search/query",
    default_models: [],
    icon: "brave",
    discovers_models: false,
    options: [resultsOption(20), SAFE_3, COUNTRY, LANGUAGE],
  },
  {
    id: "tavily",
    group: "search",
    display_name: "Tavily",
    description: "Search built for AI agents, with page excerpts.",
    type: "tavily",
    base_url: "https://api.tavily.com/search",
    fields: [KEY],
    docs_url: "https://docs.tavily.com/documentation/api-reference/endpoint/search",
    default_models: [],
    icon: "tavily",
    discovers_models: false,
    options: [resultsOption(20), SAFE_2],
  },
  {
    id: "exa",
    group: "search",
    display_name: "Exa",
    description: "Neural web search with highlights from each page.",
    type: "exa",
    base_url: "https://api.exa.ai/search",
    fields: [KEY],
    docs_url: "https://docs.exa.ai/reference/search",
    default_models: [],
    icon: "exa",
    discovers_models: false,
    options: [resultsOption(25), COUNTRY],
  },
  {
    id: "kagi",
    group: "search",
    display_name: "Kagi",
    description: "Kagi's premium search results through the Kagi Search API.",
    type: "kagi",
    base_url: "https://kagi.com/api/v1/search",
    fields: [KEY],
    docs_url: "https://help.kagi.com/kagi/api/search.html",
    key_url: "https://kagi.com/api/keys",
    default_models: [],
    icon: "kagi",
    discovers_models: false,
    options: [resultsOption(25), SAFE_2, COUNTRY],
  },
  {
    id: "perplexity",
    group: "search",
    display_name: "Perplexity Search",
    description: "Ranked web results with snippets from the Perplexity Search API.",
    type: "perplexity",
    base_url: "https://api.perplexity.ai/search",
    fields: [KEY],
    docs_url: "https://docs.perplexity.ai/api-reference/search-post",
    default_models: [],
    icon: "perplexity",
    discovers_models: false,
    options: [resultsOption(20), COUNTRY, LANGUAGE],
  },
];

export const PROTOCOLS: readonly ProviderProtocol[] = [
  { id: "openai-chat", group: "llm", label: "OpenAI Chat Completions", base_url_hint: "https://api.example.com/v1" },
  { id: "openai-responses", group: "llm", label: "OpenAI Responses", base_url_hint: "https://api.example.com/v1" },
  { id: "anthropic", group: "llm", label: "Anthropic Messages", base_url_hint: "https://api.example.com" },
  { id: "decision-model", group: "classifier", label: "Typed decision model", base_url_hint: "https://decisions.example.com/v1/decide" },
  { id: "decision-http", group: "classifier", label: "HTTP policy webhook", base_url_hint: "https://policy.example.com/decide" },
  { id: "searxng", group: "search", label: "SearXNG-compatible", base_url_hint: "https://searx.example.com" },
];

export function findPreset(id: string): ProviderPreset | undefined {
  return PRESETS.find((p) => p.id === id);
}

export function groupOfType(type: string): ProviderGroup | undefined {
  if ((LLM_TYPES as readonly string[]).includes(type)) return "llm";
  if ((CLASSIFIER_TYPES as readonly string[]).includes(type)) return "classifier";
  if ((SEARCH_TYPES as readonly string[]).includes(type)) return "search";
  return undefined;
}

function normalizeUrl(url: string): string {
  return url.trim().replace(/\/+$/, "").toLowerCase();
}

/**
 * The preset a stored provider belongs to: the one it was connected from, else (rows created before presets
 * existed) the preset of the same group with the same type and base URL. Undefined: a custom provider.
 */
export function presetOf(row: { presetId: string | null; kind: string; type: string; baseUrl: string }): ProviderPreset | undefined {
  if (row.presetId) return findPreset(row.presetId);
  const url = normalizeUrl(row.baseUrl);
  return PRESETS.find((p) => p.group === row.kind && p.type === row.type && !p.local && !p.builtin && normalizeUrl(p.base_url) === url);
}

/** How a stored provider charges: its preset's billing; custom providers (and non-LLM groups) are metered. */
export function billingOf(row: { presetId: string | null; kind: string; type: string; baseUrl: string }): Billing {
  return presetOf(row)?.billing ?? "metered";
}

/** The options a stored search provider may set: its preset's, or for a custom SearXNG-compatible one the SearXNG preset's. */
export function searchOptionSpecs(row: { presetId: string | null; kind: string; type: string; baseUrl: string }): PresetOption[] {
  if (row.kind !== "search") return [];
  const preset = presetOf(row);
  if (preset?.options) return preset.options;
  return row.type === "searxng" ? (findPreset("searxng-url")?.options ?? []) : [];
}
