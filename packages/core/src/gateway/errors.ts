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

/** Classifies a non-2xx upstream HTTP response. */
export function classifyHttpFailure(status: number, headers: HeaderLike, text: string): UpstreamFailure {
  const { message, type: upstreamType } = extractUpstreamError(text);
  const retryAfterMs = parseRetryAfter(headers);
  const shouldRetry = headers.get("x-should-retry");
  let failure: UpstreamFailure;
  if (status === 529 || status === 503 || upstreamType === "overloaded_error") {
    failure = { type: "overloaded", message, status, retryable: true };
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
