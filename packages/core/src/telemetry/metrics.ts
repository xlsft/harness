/**
 * Metric and span helpers over the OpenTelemetry API. Without an SDK installed (telemetry/otel.ts, off unless configured)
 * the API is a no-op and these cost next to nothing. Attributes carry names and outcomes: never prompts, keys, tokens or
 * email addresses.
 */
import { metrics, SpanKind, SpanStatusCode, trace, type Attributes, type Span } from "@opentelemetry/api";

const SCOPE = "harness-core";

const counters = new Map<string, ReturnType<ReturnType<typeof metrics.getMeter>["createCounter"]>>();
const histograms = new Map<string, ReturnType<ReturnType<typeof metrics.getMeter>["createHistogram"]>>();

/** Instruments are bound to the provider current when first used: reset after installing one. */
export function resetInstruments(): void {
  counters.clear();
  histograms.clear();
}

export function countMetric(name: string, value: number, attributes: Attributes = {}, unit?: string): void {
  let c = counters.get(name);
  if (!c) counters.set(name, (c = metrics.getMeter(SCOPE).createCounter(name, unit ? { unit } : {})));
  c.add(value, attributes);
}

export function recordMetric(name: string, value: number, attributes: Attributes = {}, unit?: string): void {
  let h = histograms.get(name);
  if (!h) histograms.set(name, (h = metrics.getMeter(SCOPE).createHistogram(name, unit ? { unit } : {})));
  h.record(value, attributes);
}

/** Runs `fn` in a span; a thrown error marks it failed and goes on. The active context parents nested spans. */
export async function withSpan<T>(name: string, attributes: Attributes, fn: (span: Span) => Promise<T>, kind: SpanKind = SpanKind.INTERNAL): Promise<T> {
  return trace.getTracer(SCOPE).startActiveSpan(name, { attributes, kind }, async (span) => {
    try {
      return await fn(span);
    } catch (err) {
      span.setStatus({ code: SpanStatusCode.ERROR, message: err instanceof Error ? err.name : "error" });
      throw err;
    } finally {
      span.end();
    }
  });
}
