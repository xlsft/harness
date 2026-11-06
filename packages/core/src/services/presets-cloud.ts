/**
 * Cloud platform presets (Amazon Bedrock, Google Vertex AI, Microsoft Foundry). Their endpoints name the region, project or
 * resource, so `base_url` is a template over the options, which are validated before they reach it (cloudOptionError): a stray
 * character in a host name would send signed requests elsewhere. The "ambient" mode spends the server's own cloud identity,
 * so only global providers may use it.
 *
 * Endpoints and request shapes come from each vendor's documentation (read November 2026). Prices are left out: these
 * platforms set their own, by region and endpoint type, and someone who needs them sets them by hand.
 */
import type { CloudOptionName, PresetField, PresetOption, ProviderPreset } from "./presets.ts";

const AWS_OPTIONS: PresetOption[] = [
  {
    name: "credential_mode",
    label: "Credentials",
    kind: "select",
    required: true,
    default_value: "keys",
    choices: [
      { value: "keys", label: "Access key (SigV4)" },
      { value: "bearer", label: "Bedrock API key" },
      { value: "ambient", label: "This server's AWS identity (instance role, environment)", global_only: true },
    ],
  },
  { name: "region", label: "Region", kind: "text", placeholder: "us-east-1", required: true },
  { name: "access_key_id", label: "Access key id", kind: "text", placeholder: "AKIA...", required: true, modes: ["keys"] },
];
const AWS_FIELDS: PresetField[] = [
  { name: "api_key", label: "Secret access key", required: true, modes: ["keys"] },
  { name: "api_key", label: "Bedrock API key", required: true, modes: ["bearer"] },
];

const GCP_OPTIONS: PresetOption[] = [
  {
    name: "credential_mode",
    label: "Credentials",
    kind: "select",
    required: true,
    default_value: "service_account",
    choices: [
      { value: "service_account", label: "Service account file" },
      { value: "ambient", label: "This server's application default credentials", global_only: true },
    ],
  },
  { name: "project", label: "Project id", kind: "text", placeholder: "my-project", required: true },
  { name: "location", label: "Location", kind: "text", placeholder: "global, us or us-east5", required: true, default_value: "global" },
];
const GCP_FIELDS: PresetField[] = [
  { name: "api_key", label: "Service account file (JSON)", required: true, modes: ["service_account"], multiline: true, placeholder: "Paste the file. Stored encrypted, never shown again" },
];

const AZURE_OPTIONS: PresetOption[] = [
  {
    name: "credential_mode",
    label: "Credentials",
    kind: "select",
    required: true,
    default_value: "api_key",
    choices: [
      { value: "api_key", label: "API key" },
      { value: "entra", label: "Microsoft Entra app (client secret)" },
      { value: "ambient", label: "This server's managed identity", global_only: true },
    ],
  },
  { name: "resource", label: "Foundry resource", kind: "text", placeholder: "my-resource", required: true },
  { name: "tenant_id", label: "Tenant id", kind: "text", required: true, modes: ["entra"] },
  { name: "client_id", label: "Client id", kind: "text", required: true, modes: ["entra"] },
];
const AZURE_FIELDS: PresetField[] = [
  { name: "api_key", label: "API key", required: true, modes: ["api_key"] },
  { name: "api_key", label: "Client secret", required: true, modes: ["entra"] },
];

const CLAUDE = (prefix: string) => [
  { upstream_model: `${prefix}claude-opus-5-5`, display_name: "Claude Opus 5.5", context_window: 1_000_000, max_output_tokens: 128_000 },
  { upstream_model: `${prefix}claude-sonnet-5-5`, display_name: "Claude Sonnet 5.5", context_window: 1_000_000, max_output_tokens: 128_000 },
  { upstream_model: `${prefix}claude-haiku-5-5`, display_name: "Claude Haiku 5.5", context_window: 1_000_000, max_output_tokens: 128_000 },
];

export const CLOUD_PRESETS: ProviderPreset[] = [
  {
    id: "bedrock",
    group: "llm",
    display_name: "Amazon Bedrock",
    description: "Claude through Bedrock's Messages endpoint, signed with AWS credentials.",
    type: "anthropic",
    base_url: "https://bedrock-mantle.{region}.api.aws/anthropic",
    fields: AWS_FIELDS,
    docs_url: "https://docs.aws.amazon.com/bedrock/latest/userguide/bedrock-mantle.html",
    default_models: CLAUDE("anthropic."),
    icon: "bedrock",
    discovers_models: false,
    options: AWS_OPTIONS,
    cloud: { kind: "aws", service: "bedrock-mantle" },
  },
  {
    id: "bedrock-invoke",
    group: "llm",
    display_name: "Amazon Bedrock (InvokeModel)",
    description: "Older Claude models and inference profiles on bedrock-runtime, signed with AWS credentials.",
    type: "bedrock-invoke",
    base_url: "https://bedrock-runtime.{region}.amazonaws.com",
    fields: AWS_FIELDS,
    docs_url: "https://docs.aws.amazon.com/bedrock/latest/userguide/inference-invoke.html",
    default_models: [],
    icon: "bedrock",
    discovers_models: false,
    options: AWS_OPTIONS,
    cloud: { kind: "aws", service: "bedrock" },
  },
  {
    id: "bedrock-openai",
    group: "llm",
    display_name: "Amazon Bedrock (OpenAI-compatible)",
    description: "Other Bedrock models that take Chat Completions, signed with AWS credentials.",
    type: "openai-chat",
    base_url: "https://bedrock-runtime.{region}.amazonaws.com/openai/v1",
    fields: AWS_FIELDS,
    docs_url: "https://docs.aws.amazon.com/bedrock/latest/userguide/inference-chat-completions.html",
    default_models: [],
    icon: "bedrock",
    discovers_models: false,
    options: AWS_OPTIONS,
    cloud: { kind: "aws", service: "bedrock" },
  },
  {
    id: "vertex-anthropic",
    group: "llm",
    display_name: "Google Vertex AI (Claude)",
    description: "Claude on Vertex AI, with a service account or the server's default credentials.",
    type: "vertex-anthropic",
    base_url: "https://{location_host}aiplatform.googleapis.com/v1/projects/{project}/locations/{location}",
    fields: GCP_FIELDS,
    docs_url: "https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude",
    default_models: CLAUDE(""),
    icon: "vertex",
    discovers_models: false,
    options: GCP_OPTIONS,
    cloud: { kind: "gcp" },
  },
  {
    id: "vertex-openai",
    group: "llm",
    display_name: "Google Vertex AI (Gemini)",
    description: "Gemini and other publisher models through Vertex AI's Chat Completions endpoint.",
    type: "openai-chat",
    base_url: "https://{location_host}aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/endpoints/openapi",
    fields: GCP_FIELDS,
    docs_url: "https://cloud.google.com/vertex-ai/generative-ai/docs/start/openai",
    default_models: [],
    icon: "vertex",
    discovers_models: false,
    options: GCP_OPTIONS,
    cloud: { kind: "gcp" },
  },
  {
    id: "foundry-anthropic",
    group: "llm",
    display_name: "Microsoft Foundry (Claude)",
    description: "Claude deployments of a Foundry resource, with an API key or Microsoft Entra.",
    type: "anthropic",
    base_url: "https://{resource}.services.ai.azure.com/anthropic",
    fields: AZURE_FIELDS,
    docs_url: "https://learn.microsoft.com/azure/ai-foundry/foundry-models/how-to/use-foundry-models-claude",
    default_models: CLAUDE(""),
    icon: "foundry",
    discovers_models: false,
    options: AZURE_OPTIONS,
    cloud: { kind: "azure", service: "anthropic" },
  },
  {
    id: "foundry-openai",
    group: "llm",
    display_name: "Microsoft Foundry (OpenAI)",
    description: "OpenAI and other deployments of a Foundry resource through its Responses endpoint.",
    type: "openai-responses",
    base_url: "https://{resource}.openai.azure.com/openai/v1",
    fields: AZURE_FIELDS,
    docs_url: "https://learn.microsoft.com/azure/ai-foundry/openai/api-version-lifecycle",
    default_models: [],
    icon: "foundry",
    discovers_models: false,
    options: AZURE_OPTIONS,
    cloud: { kind: "azure", service: "openai" },
  },
];

const RULES: Partial<Record<CloudOptionName, { re: RegExp; what: string }>> = {
  region: { re: /^[a-z]{2}(-[a-z]+)+-\d{1,2}$/, what: "Region must look like us-east-1" },
  access_key_id: { re: /^[A-Z0-9]{16,128}$/, what: "Access key id must be letters and digits, as AWS gives it" },
  project: { re: /^[a-z][a-z0-9:.-]{3,62}$/, what: "Project must be a project id, such as my-project" },
  location: { re: /^(global|[a-z]{2,}(-[a-z0-9]+)*)$/, what: "Location must look like global, us or us-east5" },
  resource: { re: /^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$/, what: "Resource must be the name of a Foundry resource, without its domain" },
  tenant_id: { re: /^[a-zA-Z0-9][a-zA-Z0-9.-]{0,127}$/, what: "Tenant id must be a tenant's id or domain" },
  client_id: { re: /^[a-zA-Z0-9-]{1,64}$/, what: "Client id must be an application's id" },
};

/** Why a cloud option's value cannot be used, if so. */
export function cloudOptionError(name: string, value: string): string | undefined {
  const rule = RULES[name as CloudOptionName];
  return rule && !rule.re.test(value) ? rule.what : undefined;
}

/** The credential mode of a cloud preset's options, or its default. */
export function cloudModeOf(preset: ProviderPreset, options: Record<string, string>): string {
  const spec = preset.options?.find((o) => o.name === "credential_mode");
  return options.credential_mode || spec?.default_value || "";
}

/** Whether a field or option of a cloud preset applies with this credential mode. */
export function appliesToMode(spec: { modes?: string[] }, mode: string): boolean {
  return !spec.modes?.length || spec.modes.includes(mode);
}

/** A cloud preset's base URL from validated options. The Vertex host has no prefix for the global location. */
export function cloudBaseUrl(preset: ProviderPreset, options: Record<string, string>): string {
  const location = options.location || "global";
  return preset.base_url.replace(/\{(\w+)\}/g, (_, name: string) => {
    if (name === "location_host") return location === "global" ? "" : `${location}-`;
    if (name === "location") return location;
    const value = options[name];
    if (!value) throw new Error(`Option ${name} is not set`);
    return value;
  });
}

/**
 * The options of a cloud provider, checked against its preset and credential mode. Personal providers cannot pick the
 * server's own identity: the choice is refused here, and again where requests are authorized.
 */
export function validateCloudOptions(preset: ProviderPreset, input: Record<string, string> | null | undefined, personal: boolean): Record<string, string> {
  const specs = preset.options ?? [];
  const out: Record<string, string> = {};
  for (const [name, raw] of Object.entries(input ?? {})) {
    const spec = specs.find((o) => o.name === name);
    if (!spec) throw new CloudOptionProblem(`Unknown option ${name}`);
    const v = typeof raw === "string" ? raw.trim() : "";
    if (v) out[name] = v;
  }
  const mode = cloudModeOf(preset, out);
  const modeSpec = specs.find((o) => o.name === "credential_mode");
  const choice = modeSpec?.choices?.find((c) => c.value === mode);
  if (!choice) throw new CloudOptionProblem(`Credentials must be one of ${modeSpec?.choices?.map((c) => c.value).join(", ")}`);
  if (choice.global_only && personal) throw new CloudOptionProblem("A personal provider cannot use the server's own cloud identity: give it credentials of its own");
  out.credential_mode = mode;
  for (const spec of specs) {
    if (!appliesToMode(spec, mode)) {
      delete out[spec.name];
      continue;
    }
    if (!out[spec.name] && spec.default_value) out[spec.name] = spec.default_value;
    if (spec.required && !out[spec.name]) throw new CloudOptionProblem(`${preset.display_name} needs ${spec.label.toLowerCase()}`);
    const problem = out[spec.name] ? cloudOptionError(spec.name, out[spec.name]!) : undefined;
    if (problem) throw new CloudOptionProblem(problem);
  }
  return out;
}

/** Whether a cloud preset's secret is needed with this mode. */
export function cloudSecretRequired(preset: ProviderPreset, mode: string): boolean {
  return preset.fields.some((f) => f.name === "api_key" && f.required && appliesToMode(f, mode));
}

/** An option the user must correct. */
export class CloudOptionProblem extends Error {}

/** What the settings screens show in place of a secret: a service account's email, else nothing of the file. */
export function cloudSecretHint(kind: string, secret: string): string | undefined {
  if (kind !== "gcp") return undefined;
  try {
    const email = (JSON.parse(secret) as { client_email?: unknown }).client_email;
    return typeof email === "string" ? email : "";
  } catch {
    return "";
  }
}
