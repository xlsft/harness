/**
 * LLM gateway service (docs/protocol/gateway.md). Transport-agnostic:
 *
 *   const prepared = await prepareMessages(principal, body)    // throws ApiError before streaming
 *   for await (const ev of runMessages(prepared, principal, signal)) send(ev.event, ev.data)
 *
 * Responsibilities: model resolution, capability filtering + negotiation, upstream retries
 * (only before the first content block), fallback chain, usage/cost accounting.
 */
import { setTimeout as sleep } from "node:timers/promises";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { anthropicAdapter } from "../gateway/adapters/anthropic.ts";
import { openaiChatAdapter } from "../gateway/adapters/openai-chat.ts";
import type { Adapter, AdapterTarget, UpstreamRequest } from "../gateway/adapters/types.ts";
import { computeCost, emptyUsage, estimateTokens, mergeUsage } from "../gateway/cost.ts";
import { classifyHttpFailure, networkFailure } from "../gateway/errors.ts";
import { computeFeatures, normalizeRequest } from "../gateway/features.ts";
import type { CanonicalRequest, GatewayEvent, StopReason, UpstreamFailure, Usage } from "../gateway/types.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { Principal } from "./devices.ts";
import { disableFeature, getDisabledFeatures, resolveModelForUser, type ModelEntry } from "./models.ts";
import { loadProviderRuntime, type ProviderType } from "./providers.ts";
import { enforceQuotas } from "./quotas.ts";

const ADAPTERS: Record<ProviderType, Adapter> = {
  anthropic: anthropicAdapter,
  "openai-chat": openaiChatAdapter,
};

/** Retry-after values above this are surfaced to the client instead of waited out. */
const MAX_RETRY_DELAY_MS = 60_000;
/** On `overloaded`, switch to the next fallback model after this many retries on the current one. */
const RETRIES_BEFORE_FALLBACK = 2;
const MAX_NEGOTIATION_ROUNDS = 5;

export interface PreparedMessages {
  req: CanonicalRequest;
  model: ModelEntry;
}

export async function prepareMessages(principal: Principal, body: unknown): Promise<PreparedMessages> {
  const req = normalizeRequest(body, { requireStream: true });
  const model = await resolveModelForUser(req.model, principal.user);
  // Spend limits are checked before any upstream call (RESOURCE_EXHAUSTED + QUOTA_EXCEEDED).
  await enforceQuotas(principal.user, req.metadata?.session_id || undefined);
  return { req, model };
}

export function backoffDelay(retry: number, base = config().upstreamRetryBaseMs): number {
  const exp = Math.min(base * 2 ** retry, 32_000);
  return Math.round(exp * (1 + Math.random() * 0.25));
}

type AttemptResult =
  | { ok: true; stopReason: StopReason | null }
  | { ok: false; failure: UpstreamFailure; committed: boolean; sent: UpstreamRequest; target: AdapterTarget };

function errorEvent(f: Pick<UpstreamFailure, "type" | "message" | "retryAfterMs" | "status">): GatewayEvent {
  const error: Record<string, unknown> = { type: f.type, message: f.message };
  if (f.retryAfterMs !== undefined) error.retry_after_ms = f.retryAfterMs;
  if (f.status !== undefined) error.upstream_status = f.status;
  return { event: "error", data: { type: "error", error } };
}

async function fallbackChain(primary: ModelEntry, principal: Principal): Promise<ModelEntry[]> {
  const chain = [primary];
  for (const id of primary.fallbacks) {
    try {
      const m = await resolveModelForUser(id, principal.user);
      // A fallback with a smaller context window could not take the same conversation.
      if (m.contextWindow < primary.contextWindow) continue;
      if (!chain.some((c) => c.id === m.id)) chain.push(m);
    } catch {
      // invisible or deleted fallback: skip
    }
  }
  return chain;
}

export async function* runMessages(
  prepared: PreparedMessages,
  principal: Principal,
  signal?: AbortSignal,
): AsyncGenerator<GatewayEvent> {
  const { req } = prepared;
  const cfg = config();
  const startedAt = Date.now();
  const messageId = newId("msg");
  const chain = await fallbackChain(prepared.model, principal);

  let retries = 0;
  let usage: Usage = emptyUsage();
  let committed = false;
  let served: ModelEntry = prepared.model;
  let status: "ok" | "error" | "aborted" = "error";
  let stopReason: StopReason | null = null;
  let errorType: string | undefined;

  /** One upstream attempt; yields client events, returns the outcome. */
  async function* attempt(model: ModelEntry): AsyncGenerator<GatewayEvent, AttemptResult> {
    const provider = await loadProviderRuntime(model.providerId);
    const adapter = ADAPTERS[provider.type];
    const features = computeFeatures(req, model, await getDisabledFeatures(model.id));
    const target: AdapterTarget = { model, provider, features };
    const sent = adapter.buildRequest(req, target);
    usage = emptyUsage();

    let res;
    try {
      res = await guardedFetch(sent.url, {
        method: "POST",
        headers: sent.headers,
        body: JSON.stringify(sent.body),
        signal,
        allowPrivateNetwork: provider.allowPrivateNetwork,
      });
    } catch (err) {
      if (signal?.aborted) throw err;
      if (err instanceof ApiError) {
        return { ok: false, failure: { type: "upstream", message: err.message, retryable: false }, committed: false, sent, target };
      }
      return { ok: false, failure: networkFailure(err), committed: false, sent, target };
    }
    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => "");
      return { ok: false, failure: classifyHttpFailure(res.status, res.headers, text), committed: false, sent, target };
    }

    const commit = (): GatewayEvent => {
      committed = true;
      served = model;
      return {
        event: "message_start",
        data: {
          type: "message_start",
          message: {
            id: messageId,
            model: req.model,
            role: "assistant",
            content: [],
            stop_reason: null,
            usage: { input_tokens: usage.input_tokens, output_tokens: usage.output_tokens },
          },
        },
      };
    };

    for await (const ev of adapter.translate(res.body as ReadableStream<Uint8Array>, target)) {
      switch (ev.kind) {
        case "start":
          usage = mergeUsage(usage, ev.usage);
          break;
        case "usage":
          usage = mergeUsage(usage, ev.usage);
          break;
        case "block":
          if (!committed) yield commit();
          yield { event: ev.event, data: ev.data };
          break;
        case "error":
          return { ok: false, failure: ev.error, committed, sent, target };
        case "stop": {
          usage = mergeUsage(usage, ev.usage);
          if (!committed) yield commit();
          const delta: Record<string, unknown> = { stop_reason: ev.stop_reason };
          if (ev.stop_reason === "stop_sequence" && ev.stop_sequence) delta.stop_sequence = ev.stop_sequence;
          yield { event: "message_delta", data: { type: "message_delta", delta, usage: { ...usage } } };
          const harness: Record<string, unknown> = {
            cost_usd: computeCost(usage, model.pricing),
            upstream_model: model.upstreamModel,
            retries,
            // Catalog alias that answered; differs from req.model after a fallback.
            served_model: model.id,
          };
          yield { event: "message_stop", data: { type: "message_stop", harness } };
          return { ok: true, stopReason: ev.stop_reason };
        }
      }
    }
    return {
      ok: false,
      failure: { type: "upstream", message: "Upstream stream ended unexpectedly", retryable: true },
      committed,
      sent,
      target,
    };
  }

  try {
    chainLoop: for (let ci = 0; ci < chain.length; ci++) {
      const model = chain[ci]!;
      const hasFallback = ci < chain.length - 1;
      let modelRetries = 0;
      let negotiations = 0;
      for (;;) {
        const result = yield* attempt(model);
        if (result.ok) {
          status = "ok";
          stopReason = result.stopReason;
          return;
        }
        const { failure } = result;
        errorType = failure.type;
        if (result.committed) {
          // Content already reached the client: no retries, report and close.
          yield errorEvent(failure);
          return;
        }

        const feature = ADAPTERS[result.target.provider.type].rejectedFeature(failure, result.sent, result.target.features);
        if (feature && negotiations < MAX_NEGOTIATION_ROUNDS) {
          negotiations++;
          console.warn(`[harness] model ${model.id}: upstream rejected "${feature}", retrying without it`);
          await disableFeature(model.id, feature, failure.message);
          continue;
        }

        const unavailable = failure.type === "overloaded";
        if (unavailable && hasFallback && modelRetries >= RETRIES_BEFORE_FALLBACK && retries < cfg.upstreamMaxRetries) {
          retries++;
          yield retryEvent(retries, cfg.upstreamMaxRetries, 0, failure);
          continue chainLoop;
        }

        const delay = failure.retryAfterMs ?? backoffDelay(modelRetries);
        if (failure.retryable && retries < cfg.upstreamMaxRetries && delay <= MAX_RETRY_DELAY_MS) {
          retries++;
          modelRetries++;
          yield retryEvent(retries, cfg.upstreamMaxRetries, delay, failure);
          await sleep(delay, undefined, { signal });
          continue;
        }

        if (unavailable && hasFallback && retries < cfg.upstreamMaxRetries) {
          retries++;
          yield retryEvent(retries, cfg.upstreamMaxRetries, 0, failure);
          continue chainLoop;
        }

        yield errorEvent(failure);
        return;
      }
    }
  } catch (err) {
    if (signal?.aborted) {
      status = "aborted";
      return;
    }
    console.error("[harness] gateway error", err);
    errorType = "internal";
    yield errorEvent({ type: "internal", message: "Internal gateway error" });
  } finally {
    await recordUsage({
      principal,
      req,
      model: served,
      usage,
      retries,
      status,
      stopReason,
      errorType: status === "ok" ? undefined : errorType,
      durationMs: Date.now() - startedAt,
    }).catch((e) => console.error("[harness] failed to record usage", e));
  }
}

function retryEvent(retry: number, max: number, delayMs: number, failure: UpstreamFailure): GatewayEvent {
  // `attempt` is the number of the attempt about to be made (first retry = attempt 2).
  return {
    event: "harness_retry",
    data: {
      attempt: retry + 1,
      max_retries: max,
      retry_delay_ms: delayMs,
      error: { type: failure.type, message: failure.message, ...(failure.status ? { upstream_status: failure.status } : {}) },
    },
  };
}

async function recordUsage(r: {
  principal: Principal;
  req: CanonicalRequest;
  model: ModelEntry;
  usage: Usage;
  retries: number;
  status: string;
  stopReason: StopReason | null;
  errorType?: string;
  durationMs: number;
}) {
  await ensureDb();
  await getDb()
    .insert(schema.usageRecord)
    .values({
      id: newId("usg"),
      userId: r.principal.user.id,
      deviceId: r.principal.deviceId ?? null,
      modelId: r.model.id,
      providerId: r.model.providerId,
      upstreamModel: r.model.upstreamModel,
      requestClass: r.req.metadata?.request_class ?? null,
      sessionId: r.req.metadata?.session_id ?? null,
      agentId: r.req.metadata?.agent_id ?? null,
      inputTokens: r.usage.input_tokens,
      outputTokens: r.usage.output_tokens,
      cacheReadTokens: r.usage.cache_read_input_tokens,
      cacheWriteTokens: r.usage.cache_creation_input_tokens,
      costUsd: computeCost(r.usage, r.model.pricing),
      retries: r.retries,
      status: r.status,
      stopReason: r.stopReason,
      errorType: r.errorType ?? null,
      durationMs: r.durationMs,
      createdAt: new Date(),
    });
}

// --- count_tokens --------------------------------------------------------------------

export async function countTokens(principal: Principal, body: unknown): Promise<{ input_tokens: number; estimated: boolean }> {
  const req = normalizeRequest(body, { requireStream: false });
  const model = await resolveModelForUser(req.model, principal.user);
  const provider = await loadProviderRuntime(model.providerId);
  const adapter = ADAPTERS[provider.type];
  const target: AdapterTarget = { model, provider, features: computeFeatures(req, model, await getDisabledFeatures(model.id)) };
  if (adapter.buildCountTokens) {
    const up = adapter.buildCountTokens(req, target);
    try {
      const res = await guardedFetch(up.url, {
        method: "POST",
        headers: up.headers,
        body: JSON.stringify(up.body),
        allowPrivateNetwork: provider.allowPrivateNetwork,
        signal: AbortSignal.timeout(30_000),
      });
      if (res.ok) {
        const j = (await res.json()) as { input_tokens?: number };
        if (typeof j.input_tokens === "number") return { input_tokens: j.input_tokens, estimated: false };
      } else {
        await res.body?.cancel();
      }
    } catch {
      // fall through to estimate
    }
  }
  return { input_tokens: estimateTokens({ s: req.system, m: req.messages, t: req.tools }), estimated: true };
}
