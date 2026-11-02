/**
 * `llm` provider: a classifier on any catalog model, called through the gateway as an auxiliary request
 * charged to the requesting user (usage records, quotas). The prompt comes from the prompt registry
 * (prompts/decision/: system.md and the `classify` tool), with per-model overrides like every other
 * prompt file. The model must answer through the forced `classify` tool call; anything malformed is ASK.
 *
 * Model: settings.model, else the chain's classifier_model, else the `classifier` slot, else `small`.
 */
import { config } from "../env.ts";
import { normalizeRequest } from "../gateway/features.ts";
import type { GatewayEvent } from "../gateway/types.ts";
import { ApiError } from "../lib/errors.ts";
import { runMessages } from "../services/gateway.ts";
import { findModel, getSlots, type ModelEntry } from "../services/models.ts";
import { resolvePromptFiles } from "../services/prompts.ts";
import { enforceQuotas } from "../services/quotas.ts";
import { decisionCacheKey, type DecisionCache } from "./cache.ts";
import { ProviderError, type DecideInput, type DecisionContext, type DecisionProvider, type ProviderDecision } from "./types.ts";
import { parseVerdict } from "./verdict.ts";

export const CLASSIFY_TOOL = "classify";
export const DEFAULT_LLM_TIMEOUT_MS = 8_000;
const DEFAULT_MAX_TOKENS = 1024;
const DEFAULT_MIN_CONFIDENCE = 0.5;
const MAX_INPUT_CHARS = 20_000;
const MAX_MESSAGE_CHARS = 4_000;
const MAX_MESSAGES = 8;

export interface LlmSettings {
  /** Catalog model id; overrides classifier_model and the slots. */
  model?: string;
  timeout_ms?: number;
  max_tokens?: number;
  /** ALLOW / DENY below this confidence become ASK. */
  min_confidence?: number;
}

export interface LlmProviderOptions {
  id: string;
  displayName?: string;
  settings: LlmSettings;
  /** Chain-level classifier model (admin config), used when settings.model is empty. */
  classifierModel?: string | null;
  cache: DecisionCache;
  cacheTtlMs: number;
}

export class LlmProvider implements DecisionProvider {
  readonly type = "llm" as const;
  readonly id: string;
  readonly displayName: string;

  constructor(private readonly o: LlmProviderOptions) {
    this.id = o.id;
    this.displayName = o.displayName || "Model classifier";
  }

  /** Catalog id of the classifier model ("" when none is configured). */
  async modelId(): Promise<string> {
    if (this.o.settings.model) return this.o.settings.model;
    if (this.o.classifierModel) return this.o.classifierModel;
    const slots = await getSlots();
    return slots.classifier || slots.small || "";
  }

  async model(): Promise<string> {
    try {
      return (await this.resolveModel()).id;
    } catch {
      return "";
    }
  }

  private async resolveModel(): Promise<ModelEntry> {
    const id = await this.modelId();
    if (!id) throw new ProviderError("No classifier model is configured (set classifier_model or the classifier / small slot)", "config");
    const m = await findModel(id);
    if (!m || !m.enabled) throw new ProviderError(`Classifier model ${id} is not available`, "config");
    return m;
  }

  async healthy(): Promise<boolean> {
    try {
      await this.resolveModel();
      return true;
    } catch {
      return false;
    }
  }

  async decide(input: DecideInput, ctx: DecisionContext): Promise<ProviderDecision> {
    const model = await this.resolveModel();
    const key = decisionCacheKey(ctx.principal.user.id, this.id, model.id, input);
    const hit = this.o.cache.get(key);
    if (hit) return { ...hit, cached: true, costUsd: 0 };

    const files = resolvePromptFiles(config().promptsDir, [model.id, model.upstreamModel]);
    const system = files.get("decision/system.md")?.trimEnd();
    const description = files.get(`decision/${CLASSIFY_TOOL}/description.md`)?.trimEnd();
    const schemaText = files.get(`decision/${CLASSIFY_TOOL}/schema.json`);
    if (!system || !description || !schemaText) throw new ProviderError("Classifier prompt files are missing (prompts/decision/)", "config");
    let schema: Record<string, unknown>;
    try {
      schema = JSON.parse(schemaText) as Record<string, unknown>;
    } catch (e) {
      throw new ProviderError(`Invalid classifier tool schema: ${(e as Error).message}`, "config");
    }

    const req = normalizeRequest(
      {
        model: model.id,
        max_tokens: Math.min(this.o.settings.max_tokens ?? DEFAULT_MAX_TOKENS, model.maxOutputTokens),
        stream: true,
        system: [{ type: "text", text: system }],
        messages: [{ role: "user", content: [{ type: "text", text: renderRequest(input) }] }],
        tools: [{ name: CLASSIFY_TOOL, description, input_schema: schema }],
        tool_choice: { type: "tool", name: CLASSIFY_TOOL, disable_parallel_tool_use: true },
        metadata: { request_class: "auxiliary", ...(input.sessionId ? { session_id: input.sessionId } : {}) },
      },
      { requireStream: true },
    );

    try {
      await enforceQuotas(ctx.principal.user, input.sessionId || undefined);
    } catch (err) {
      if (err instanceof ApiError) throw new ProviderError(err.message, "quota");
      throw err;
    }

    const timeoutMs = this.o.settings.timeout_ms ?? DEFAULT_LLM_TIMEOUT_MS;
    const timeout = AbortSignal.timeout(timeoutMs);
    const signal = ctx.signal ? AbortSignal.any([ctx.signal, timeout]) : timeout;
    const call = await collectToolCall(runMessages({ req, model }, ctx.principal, signal));
    if (timeout.aborted) throw new ProviderError(`Classifier timed out after ${timeoutMs} ms`, "timeout");
    if (ctx.signal?.aborted) throw new ProviderError("Request cancelled", "upstream");
    if (call.error) throw new ProviderError(`Classifier call failed: ${call.error}`, "upstream");

    const costUsd = call.costUsd;
    if (call.json === undefined) {
      return { decision: "ask", reason: "The classifier did not return a decision", categories: [], costUsd };
    }
    let raw: unknown;
    try {
      raw = JSON.parse(call.json || "{}");
    } catch {
      return { decision: "ask", reason: "The classifier returned malformed output", categories: [], costUsd };
    }
    const v = parseVerdict(raw, { requireReason: true });
    if (typeof v === "string") return { decision: "ask", reason: "The classifier returned malformed output", categories: [], costUsd };
    const minConfidence = this.o.settings.min_confidence ?? DEFAULT_MIN_CONFIDENCE;
    if (v.decision !== "ask" && v.confidence !== undefined && v.confidence < minConfidence) {
      return { ...v, decision: "ask", costUsd };
    }
    this.o.cache.set(key, v, this.o.cacheTtlMs);
    return { ...v, costUsd };
  }
}

interface ToolCallResult {
  /** Accumulated JSON input of the `classify` call; undefined when the model did not call it. */
  json?: string;
  costUsd: number;
  error?: string;
}

async function collectToolCall(events: AsyncGenerator<GatewayEvent>): Promise<ToolCallResult> {
  const out: ToolCallResult = { costUsd: 0 };
  let index: number | undefined;
  for await (const ev of events) {
    const d = ev.data as Record<string, any>;
    switch (ev.event) {
      case "content_block_start":
        if (d.content_block?.type === "tool_use" && d.content_block.name === CLASSIFY_TOOL && index === undefined) {
          index = d.index;
          const initial = d.content_block.input;
          out.json = initial && typeof initial === "object" && Object.keys(initial).length ? JSON.stringify(initial) : "";
        }
        break;
      case "content_block_delta":
        if (d.index === index && d.delta?.type === "input_json_delta" && typeof d.delta.partial_json === "string") {
          out.json = (out.json ?? "") + d.delta.partial_json;
        }
        break;
      case "message_stop":
        if (typeof d.harness?.cost_usd === "number") out.costUsd = d.harness.cost_usd;
        break;
      case "error":
        out.error = String(d.error?.message ?? d.error?.type ?? "unknown error");
        break;
    }
  }
  return out;
}

/** Keeps untrusted text from closing the tags it is wrapped in. */
function quote(s: string, max: number): string {
  const t = s.length > max ? `${s.slice(0, max)}\n[... truncated ${s.length - max} characters]` : s;
  return t.replace(/<\//g, "<\\/");
}

/** The user message: the tool call and its context, as tagged data. */
export function renderRequest(input: DecideInput): string {
  const lines: string[] = [];
  lines.push("<tool_call>");
  lines.push(`tool_name: ${quote(input.toolName, 200)}`);
  lines.push("input:");
  lines.push(quote(JSON.stringify(input.input, null, 2), MAX_INPUT_CHARS));
  lines.push("</tool_call>");
  lines.push("");
  lines.push("<environment>");
  lines.push(`cwd: ${quote(input.cwd || "(unknown)", 1000)}`);
  lines.push(`additional_directories: ${input.additionalDirectories.length ? quote(input.additionalDirectories.join(", "), 2000) : "(none)"}`);
  lines.push(`inside_git_repo: ${input.facts.insideGitRepo}`);
  if (input.facts.gitBranch) lines.push(`git_branch: ${quote(input.facts.gitBranch, 200)}`);
  if (input.facts.subcommands.length) lines.push(`subcommands: ${quote(JSON.stringify(input.facts.subcommands), 4000)}`);
  if (input.facts.writePaths.length) lines.push(`write_paths: ${quote(JSON.stringify(input.facts.writePaths), 4000)}`);
  if (input.facts.networkHosts.length) lines.push(`network_hosts: ${quote(JSON.stringify(input.facts.networkHosts), 2000)}`);
  lines.push("</environment>");
  lines.push("");
  lines.push("<recent_user_messages>");
  const msgs = input.recentUserMessages.slice(-MAX_MESSAGES);
  if (!msgs.length) lines.push("(none provided)");
  msgs.forEach((m, i) => lines.push(`[${i + 1}] ${quote(m, MAX_MESSAGE_CHARS)}`));
  lines.push("</recent_user_messages>");
  lines.push("");
  lines.push("<assistant_rationale>");
  lines.push(input.assistantRationale ? quote(input.assistantRationale, MAX_MESSAGE_CHARS) : "(none provided)");
  lines.push("</assistant_rationale>");
  lines.push("");
  lines.push(`Classify this tool call by calling the ${CLASSIFY_TOOL} tool.`);
  return lines.join("\n");
}
