import type { UpstreamFailure } from "./types.ts";

interface HeaderLike {
  get(name: string): string | null;
}

/** Parses retry-after-ms / retry-after (seconds or HTTP date) into milliseconds. */
export function parseRetryAfter(headers: HeaderLike): number | undefined {
  const ms = headers.get("retry-after-ms");
  if (ms && /^\d+(\.\d+)?$/.test(ms)) return Math.round(Number(ms));
  const ra = headers.get("retry-after");
  if (!ra) return undefined;
  if (/^\d+(\.\d+)?$/.test(ra)) return Math.round(Number(ra) * 1000);
  const date = Date.parse(ra);
  if (!Number.isNaN(date)) return Math.max(0, date - Date.now());
  return undefined;
}

/** Extracts a human message and upstream error "type" from common JSON error shapes. */
export function extractUpstreamError(text: string): { message: string; type?: string } {
  try {
    const j = JSON.parse(text);
    const e = j?.error ?? j;
    if (typeof e === "string") return { message: e };
    const message = e?.message ?? j?.message ?? j?.detail ?? text;
    const type = e?.type ?? e?.code ?? undefined;
    return { message: typeof message === "string" ? message : JSON.stringify(message), type: type ? String(type) : undefined };
  } catch {
    return { message: text.slice(0, 1000) || "Upstream error" };
  }
}

/**
 * Error codes and texts by which providers say a usage limit or quota is used up, as opposed to a request rate that
 * clears in seconds: OpenAI's insufficient_quota, Codex and ChatGPT plans' usage_limit_reached, Google's
 * RESOURCE_EXHAUSTED quota messages, Anthropic and the coding plans' "usage limit" / "limit reached" texts.
 */
const EXHAUSTED_TYPES = new Set(["insufficient_quota", "usage_limit_reached", "quota_exceeded", "billing_hard_limit_reached", "usage_limit_exceeded", "credit_balance_too_low"]);
const EXHAUSTED_TEXT =
  /insufficient[_ ]quota|exceeded your (current )?quota|usage[_ ]limit|limit (has been |was )?reached|reached (your|the) (usage|weekly|monthly|daily|5[- ]hour|session) limit|(weekly|monthly|daily) (usage )?limit|out of (credits|tokens)|insufficient (balance|credits?|funds)|credit balance is too low|quota (exceeded|exhausted)|resource has been exhausted \(e\.g\. check quota\)|billing/i;

/** A rate limit that is a used-up limit or quota: no retry helps until it resets, so it is never retried. */
export function isExhausted(upstreamType: string | undefined, message: string, retryAfterMs?: number): boolean {
  if (upstreamType && EXHAUSTED_TYPES.has(upstreamType)) return true;
  if (EXHAUSTED_TEXT.test(message)) return true;
  // A reset further away than any retry would wait is the same thing.
  return retryAfterMs !== undefined && retryAfterMs > 5 * 60_000;
}

function exhausted(message: string, status?: number, retryAfterMs?: number): UpstreamFailure {
  const f: UpstreamFailure = { type: "quota_exceeded", message: `The provider's usage limit is reached: ${message}`, retryable: false };
  if (status !== undefined) f.status = status;
  if (retryAfterMs !== undefined) f.retryAfterMs = retryAfterMs;
  return f;
}

/** Classifies a non-2xx upstream HTTP response. */
export function classifyHttpFailure(status: number, headers: HeaderLike, text: string): UpstreamFailure {
  const { message, type: upstreamType } = extractUpstreamError(text);
  const retryAfterMs = parseRetryAfter(headers);
  const shouldRetry = headers.get("x-should-retry");
  let failure: UpstreamFailure;
  if (status === 529 || status === 503 || upstreamType === "overloaded_error") {
    failure = { type: "overloaded", message, status, retryable: true };
  } else if ((status === 429 || status === 402 || status === 403) && isExhausted(upstreamType, message, retryAfterMs)) {
    failure = exhausted(message, status, retryAfterMs);
    failure.raw = text.slice(0, 4000);
    return failure;
  } else if (status === 429) {
    failure = { type: "rate_limit", message, status, retryable: true };
  } else if (status === 408 || status === 409 || status >= 500) {
    failure = { type: "upstream", message, status, retryable: true };
  } else if (status === 400 || status === 404 || status === 413 || status === 422) {
    // 404 usually means the upstream model id is wrong: an admin problem, but report the message.
    failure = { type: status === 404 ? "upstream" : "invalid_request", message, status, retryable: false };
  } else if (status === 401 || status === 402 || status === 403) {
    // Bad credentials or spend limit: not retried.
    failure = { type: "upstream", message: `Upstream rejected credentials (${status}): ${message}`, status, retryable: false };
  } else {
    failure = { type: "upstream", message, status, retryable: false };
  }
  if (shouldRetry === "true") failure.retryable = true;
  if (shouldRetry === "false") failure.retryable = false;
  if (retryAfterMs !== undefined) failure.retryAfterMs = retryAfterMs;
  failure.raw = text.slice(0, 4000);
  return failure;
}

/** Classifies an error event that arrived inside an upstream stream. */
export function classifyStreamError(upstreamType: string | undefined, message: string): UpstreamFailure {
  if ((upstreamType === undefined || upstreamType === "rate_limit_error" || EXHAUSTED_TYPES.has(upstreamType)) && isExhausted(upstreamType, message)) {
    return { ...exhausted(message), raw: message };
  }
  switch (upstreamType) {
    case "overloaded_error":
      return { type: "overloaded", message, retryable: true, raw: message };
    case "rate_limit_error":
      return { type: "rate_limit", message, retryable: true, raw: message };
    case "api_error":
    case "timeout_error":
    case "server_error":
      return { type: "upstream", message, retryable: true, raw: message };
    case "invalid_request_error":
      return { type: "invalid_request", message, retryable: false, raw: message };
    default:
      return { type: "upstream", message, retryable: true, raw: message };
  }
}

export function networkFailure(err: unknown): UpstreamFailure {
  const e = err as { message?: string; cause?: { code?: string; message?: string } };
  const detail = e.cause?.code ?? e.cause?.message ?? e.message ?? String(err);
  // TLS/certificate failures will not fix themselves: do not retry.
  const tls = /CERT|TLS|SSL|SELF_SIGNED/i.test(String(e.cause?.code ?? "") + String(e.cause?.message ?? ""));
  return { type: "upstream", message: `Upstream connection failed: ${detail}`, retryable: !tls };
}
