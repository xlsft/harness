/**
 * `decision_model` provider: a typed decision model (for example TypeSafe's Jev) that answers a structured
 * question with a label and a probability per label instead of free text. Served by OpenRouter's decisions
 * endpoint, by the vendor's own API, or by any self-hosted server speaking the same protocol:
 *
 *   POST <endpoint>                         Authorization: Bearer <key>
 *   {"model": "typesafe/jev-1.13",
 *    "state": "<the tool call and its context, rendered like the llm classifier's user message>",
 *    "questions": {"decision": {"type": "choice", "instructions": "...",
 *                               "criteria": {"allow": "...", "ask": "...", "deny": "..."}}}}
 *
 *   200 {"model": "...", "answers": {"decision": {"type": "choice", "choice": "allow",
 *        "probabilities": {"allow": 0.97, "ask": 0.02, "deny": 0.01}, "confidence": 0.97}},
 *        "usage": {"input_tokens", "output_tokens", "cost"}, "id", "provider"}
 *   4xx/5xx {"error": {"message", "code"}}
 *
 * The question text lives in prompts/decision/typed/question.json (per-model overrides apply, matched
 * against the configured model id). The answer is validated strictly; anything off is a provider error
 * (the chain's on_error). Mapping:
 *   - the chosen label is taken when its probability >= min_probability and its lead over the runner-up
 *     >= min_margin; a confident "ask" ends the chain with ASK (the model is sure the developer should
 *     decide), a confident "allow" / "deny" decides as usual;
 *   - otherwise on_uncertain applies: "next" (default) defers to the next provider, "ask" ends the chain
 *     with ASK.
 * ALLOW and DENY are cached like the llm classifier's verdicts (cache.ts).
 */
import { config } from "../env.ts";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import { resolvePromptFiles } from "../services/prompts.ts";
import { decisionCacheKey, type DecisionCache } from "./cache.ts";
import { renderContext } from "./llm.ts";
import { ProviderError, type DecideInput, type DecisionContext, type DecisionProvider, type DecisionValue, type ProviderDecision } from "./types.ts";

/** Named endpoints; any other value is a full URL of a server speaking the same protocol. */
export const DECISION_MODEL_ENDPOINTS = {
  openrouter: { url: "https://openrouter.ai/api/alpha/decisions", defaultModel: "typesafe/jev-1.13" },
  typesafe: { url: "https://api.typesafe.ai/v1/systemone", defaultModel: "jev-1.13.0" },
} as const;
export type NamedDecisionEndpoint = keyof typeof DECISION_MODEL_ENDPOINTS;

export const DEFAULT_DECISION_MODEL_TIMEOUT_MS = 3_000;
export const DEFAULT_MIN_PROBABILITY = 0.8;
export const DEFAULT_MIN_MARGIN = 0.15;
export const QUESTION_ID = "decision";
export const QUESTION_FILE = "decision/typed/question.json";
const MAX_RESPONSE_BYTES = 64 * 1024;
const LABELS: readonly DecisionValue[] = ["allow", "ask", "deny"];

export type OnUncertain = "next" | "ask";

/** The URL a named endpoint or a custom URL resolves to. */
export function decisionEndpointUrl(endpoint: string): string {
  return endpoint in DECISION_MODEL_ENDPOINTS ? DECISION_MODEL_ENDPOINTS[endpoint as NamedDecisionEndpoint].url : endpoint;
}

export interface ChoiceQuestion {
  type: "choice";
  instructions: string;
  criteria: Record<DecisionValue, string>;
}

/** Parses the question file; returns an error string when its shape is wrong. */
export function parseQuestion(text: string): ChoiceQuestion | string {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (e) {
    return `invalid JSON: ${(e as Error).message}`;
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return "not an object";
  const q = raw as Record<string, unknown>;
  if (q.type !== "choice") return 'type must be "choice"';
  if (typeof q.instructions !== "string" || !q.instructions.trim()) return "instructions must be a non-empty string";
  const c = q.criteria;
  if (!c || typeof c !== "object" || Array.isArray(c)) return "criteria must be an object";
  const keys = Object.keys(c).sort();
  if (keys.join(",") !== [...LABELS].sort().join(",")) return `criteria must have exactly the labels ${LABELS.join(", ")}`;
  for (const k of keys) if (typeof (c as Record<string, unknown>)[k] !== "string") return `criteria.${k} must be a string`;
  return { type: "choice", instructions: q.instructions, criteria: c as Record<DecisionValue, string> };
}

export interface ParsedAnswer {
  choice: DecisionValue;
  probability: number;
  /** Probability of the choice minus the runner-up's. */
  margin: number;
  /** Model id the server reports (may carry a dated suffix). */
  model?: string;
  costUsd?: number;
}

const finiteProb = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v) && v >= 0 && v <= 1;

/**
 * Strict validation of a decisions response for one "choice" question: the answer exists and has the right
 * type, the choice is one of the labels, the probabilities cover exactly the labels with finite values in
 * [0, 1], and the choice is the most probable label. Returns an error string otherwise.
 */
export function parseDecisionAnswer(raw: unknown, labels: readonly string[] = LABELS): ParsedAnswer | string {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return "response is not an object";
  const o = raw as Record<string, unknown>;
  const answers = o.answers;
  if (!answers || typeof answers !== "object" || Array.isArray(answers)) return "answers are missing";
  const a = (answers as Record<string, unknown>)[QUESTION_ID];
  if (!a || typeof a !== "object" || Array.isArray(a)) return `answer "${QUESTION_ID}" is missing`;
  const ans = a as Record<string, unknown>;
  if (ans.type !== "choice") return `answer type is ${JSON.stringify(ans.type)}, expected "choice"`;
  if (typeof ans.choice !== "string" || !labels.includes(ans.choice)) return `choice ${JSON.stringify(ans.choice)} is not one of ${labels.join(", ")}`;
  const probs = ans.probabilities;
  if (!probs || typeof probs !== "object" || Array.isArray(probs)) return "probabilities are missing";
  const p = probs as Record<string, unknown>;
  const keys = Object.keys(p);
  if (keys.length !== labels.length || !labels.every((l) => keys.includes(l))) return `probabilities must cover exactly ${labels.join(", ")}`;
  for (const l of labels) if (!finiteProb(p[l])) return `probability of ${l} is not a number between 0 and 1`;
  const choice = ans.choice as DecisionValue;
  const probability = p[choice] as number;
  const others = labels.filter((l) => l !== choice).map((l) => p[l] as number);
  const runnerUp = Math.max(...others);
  if (runnerUp > probability) return `choice ${choice} is not the most probable label`;
  const out: ParsedAnswer = { choice, probability, margin: probability - runnerUp };
  if (typeof o.model === "string" && o.model) out.model = o.model.slice(0, 200);
  const usage = o.usage as Record<string, unknown> | undefined;
  if (usage && typeof usage === "object" && typeof usage.cost === "number" && Number.isFinite(usage.cost) && usage.cost >= 0) out.costUsd = usage.cost;
  return out;
}

export interface DecisionModelProviderOptions {
  /** Full provider id ("decision_model" or "decision_model:<name>"). */
  id: string;
  displayName?: string;
  /** Resolved endpoint URL (see decisionEndpointUrl). */
  url: string;
  model: string;
  /** Returns the API key (undefined: send no Authorization header); throws ProviderError("config"). */
  apiKey?: () => Promise<string | undefined>;
  allowPrivateNetwork?: boolean;
  timeoutMs?: number;
  minProbability?: number;
  minMargin?: number;
  onUncertain?: OnUncertain;
  cache?: DecisionCache;
  cacheTtlMs?: number;
  /** Prompt directory; defaults to HARNESS_PROMPTS_DIR. */
  promptsDir?: string;
}

const VERB: Record<DecisionValue, string> = { allow: "Allowed", ask: "Needs your approval", deny: "Denied" };

export class DecisionModelProvider implements DecisionProvider {
  readonly type = "decision_model" as const;
  readonly id: string;
  readonly displayName: string;

  constructor(private readonly o: DecisionModelProviderOptions) {
    this.id = o.id;
    this.displayName = o.displayName || `Typed decision model (${o.model})`;
  }

  async model(): Promise<string> {
    return this.o.model;
  }

  async healthy(): Promise<boolean> {
    try {
      await this.o.apiKey?.();
      this.question();
      return true;
    } catch {
      return false;
    }
  }

  private question(): ChoiceQuestion {
    const files = resolvePromptFiles(this.o.promptsDir ?? config().promptsDir, [this.o.model]);
    const text = files.get(QUESTION_FILE);
    if (!text) throw new ProviderError(`Decision question is missing (prompts/${QUESTION_FILE})`, "config");
    const q = parseQuestion(text);
    if (typeof q === "string") throw new ProviderError(`Invalid decision question (prompts/${QUESTION_FILE}): ${q}`, "config");
    return q;
  }

  private get minProbability() {
    return this.o.minProbability ?? DEFAULT_MIN_PROBABILITY;
  }

  private get minMargin() {
    return this.o.minMargin ?? DEFAULT_MIN_MARGIN;
  }

  async decide(input: DecideInput, ctx: DecisionContext): Promise<ProviderDecision> {
    // Everything that changes the verdict for the same input is part of the cache key.
    const key = decisionCacheKey(ctx.principal.user.id, this.id, [this.o.url, this.o.model, this.minProbability, this.minMargin].join("|"), input);
    const hit = this.o.cache?.get(key);
    if (hit) return { ...hit, cached: true, costUsd: 0 };

    const question = this.question();
    const apiKey = await this.o.apiKey?.();
    const body = {
      model: this.o.model,
      state: renderState(input),
      questions: { [QUESTION_ID]: question },
    };
    const timeoutMs = this.o.timeoutMs ?? DEFAULT_DECISION_MODEL_TIMEOUT_MS;
    const timeout = AbortSignal.timeout(timeoutMs);
    const signal = ctx.signal ? AbortSignal.any([ctx.signal, timeout]) : timeout;
    const headers: Record<string, string> = {
      "content-type": "application/json",
      accept: "application/json",
      "user-agent": "harness-core/decision",
    };
    if (apiKey) headers.authorization = `Bearer ${apiKey}`;

    let status: number;
    let text: string;
    try {
      const res = await guardedFetch(this.o.url, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal,
        allowPrivateNetwork: !!this.o.allowPrivateNetwork,
        // A redirect could carry the key elsewhere; the protocol has none.
        followRedirects: false,
      });
      status = res.status;
      text = await res.text();
    } catch (err) {
      if (timeout.aborted) throw new ProviderError(`Decision model timed out after ${timeoutMs} ms`, "timeout");
      if (ctx.signal?.aborted) throw new ProviderError("Request cancelled", "upstream");
      if (err instanceof ApiError) throw new ProviderError(`Decision model endpoint blocked: ${err.message}`, "config");
      throw new ProviderError(`Decision model endpoint is unreachable: ${(err as Error).message}`, "upstream");
    }
    if (text.length > MAX_RESPONSE_BYTES) throw new ProviderError("Decision model response is too large", "invalid_response");
    let raw: unknown;
    try {
      raw = JSON.parse(text);
    } catch {
      raw = undefined;
    }
    if (status < 200 || status >= 300) {
      const msg = upstreamError(raw);
      throw new ProviderError(`Decision model returned HTTP ${status}${msg ? `: ${msg}` : ""}`, status === 401 || status === 403 ? "config" : "upstream");
    }
    if (raw === undefined) throw new ProviderError("Decision model returned invalid JSON", "invalid_response");
    const errMsg = upstreamError(raw);
    if (errMsg) throw new ProviderError(`Decision model error: ${errMsg}`, "upstream");
    const a = parseDecisionAnswer(raw);
    if (typeof a === "string") throw new ProviderError(`Decision model returned an invalid answer: ${a}`, "invalid_response");

    const model = a.model || this.o.model;
    const costUsd = a.costUsd ?? 0;
    const confident = a.probability >= this.minProbability && a.margin >= this.minMargin;
    const stats = `${model}, p=${a.probability.toFixed(2)}, margin ${a.margin.toFixed(2)}`;
    if (!confident) {
      const onUncertain = this.o.onUncertain ?? "next";
      return {
        decision: "ask",
        reason: `${this.displayName} is not sure (leans ${a.choice}; ${stats})`,
        categories: [],
        confidence: a.probability,
        costUsd,
        ...(onUncertain === "ask" ? { final: true } : {}),
      };
    }
    const v: ProviderDecision = {
      decision: a.choice,
      reason: `${VERB[a.choice]} by ${this.displayName} (${stats})`,
      categories: [],
      confidence: a.probability,
      ...(a.choice === "ask" ? { final: true } : {}),
    };
    this.o.cache?.set(key, v, this.o.cacheTtlMs ?? 0);
    return { ...v, costUsd };
  }
}

function upstreamError(raw: unknown): string {
  if (!raw || typeof raw !== "object") return "";
  const e = (raw as Record<string, unknown>).error;
  if (!e) return "";
  if (typeof e === "string") return e.slice(0, 300);
  if (typeof e === "object" && typeof (e as Record<string, unknown>).message === "string") return ((e as Record<string, unknown>).message as string).slice(0, 300);
  return "unknown error";
}

/** The `state` sent to the model: the permission mode and the classifier's rendering of the call. */
export function renderState(input: DecideInput): string {
  return `permission_mode: auto\n\n${renderContext(input)}`;
}
