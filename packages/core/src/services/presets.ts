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
 */

export type ProviderGroup = "llm" | "classifier";

export const LLM_TYPES = ["anthropic", "openai-chat", "openai-responses"] as const;
export const CLASSIFIER_TYPES = ["decision-model", "decision-http"] as const;

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
}

export interface ProviderProtocol {
  id: string;
  group: ProviderGroup;
  label: string;
  base_url_hint: string;
}

const KEY: PresetField = { name: "api_key", label: "API key", required: true };

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
    icon: "OR",
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
    icon: "AN",
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
    icon: "OA",
    discovers_models: true,
  },
  {
    id: "kimi-code",
    group: "llm",
    display_name: "Kimi Code",
    description: "The kimi.com coding subscription (Anthropic-compatible endpoint).",
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
    icon: "KC",
    discovers_models: false,
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
    icon: "KI",
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
    icon: "KI",
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
    icon: "Z",
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
    icon: "Z",
    discovers_models: false,
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
    icon: "DS",
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
    icon: "G",
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
    icon: "X",
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
    icon: "OL",
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
    icon: "OR",
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
];

export const PROTOCOLS: readonly ProviderProtocol[] = [
  { id: "openai-chat", group: "llm", label: "OpenAI Chat Completions", base_url_hint: "https://api.example.com/v1" },
  { id: "openai-responses", group: "llm", label: "OpenAI Responses", base_url_hint: "https://api.example.com/v1" },
  { id: "anthropic", group: "llm", label: "Anthropic Messages", base_url_hint: "https://api.example.com" },
  { id: "decision-model", group: "classifier", label: "Typed decision model", base_url_hint: "https://decisions.example.com/v1/decide" },
  { id: "decision-http", group: "classifier", label: "HTTP policy webhook", base_url_hint: "https://policy.example.com/decide" },
];

export function findPreset(id: string): ProviderPreset | undefined {
  return PRESETS.find((p) => p.id === id);
}

export function groupOfType(type: string): ProviderGroup | undefined {
  if ((LLM_TYPES as readonly string[]).includes(type)) return "llm";
  if ((CLASSIFIER_TYPES as readonly string[]).includes(type)) return "classifier";
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
  return PRESETS.find((p) => p.group === row.kind && p.type === row.type && !p.local && normalizeUrl(p.base_url) === url);
}
