/**
 * OpenTelemetry export of metrics and traces over OTLP/HTTP, configured the standard way by environment:
 *   OTEL_EXPORTER_OTLP_ENDPOINT (or _TRACES_ / _METRICS_ENDPOINT)   turns it on
 *   OTEL_EXPORTER_OTLP_HEADERS, OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES, OTEL_METRIC_EXPORT_INTERVAL
 *   OTEL_TRACES_EXPORTER=none / OTEL_METRICS_EXPORTER=none / OTEL_SDK_DISABLED=true   turn parts off
 * The exporters read these variables themselves. With none set nothing is installed and no connection is made.
 */
import { context, metrics, propagation, trace } from "@opentelemetry/api";
import { AsyncLocalStorageContextManager } from "@opentelemetry/context-async-hooks";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-http";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { MeterProvider, PeriodicExportingMetricReader } from "@opentelemetry/sdk-metrics";
import { BatchSpanProcessor, TracerProvider } from "@opentelemetry/sdk-trace";
import { resetInstruments } from "./metrics.ts";

interface Installed {
  tracer?: TracerProvider;
  meter?: MeterProvider;
}

let installed: Installed | undefined;

function enabled(env: NodeJS.ProcessEnv, signal: "TRACES" | "METRICS"): boolean {
  if (env.OTEL_SDK_DISABLED === "true") return false;
  if (env[`OTEL_${signal}_EXPORTER`] === "none") return false;
  return !!(env.OTEL_EXPORTER_OTLP_ENDPOINT || env[`OTEL_EXPORTER_OTLP_${signal}_ENDPOINT`]);
}

export function startTelemetry(env: NodeJS.ProcessEnv = process.env): boolean {
  if (installed) return true;
  const wantTraces = enabled(env, "TRACES");
  const wantMetrics = enabled(env, "METRICS");
  if (!wantTraces && !wantMetrics) return false;
  const resource = resourceFromAttributes({
    "service.name": env.OTEL_SERVICE_NAME || "harness-core",
    "service.version": env.HARNESS_VERSION || "0.0.0-dev",
  });
  const out: Installed = {};
  if (wantTraces) {
    out.tracer = new TracerProvider({ resource, spanProcessors: [new BatchSpanProcessor({ exporter: new OTLPTraceExporter() })] });
    context.setGlobalContextManager(new AsyncLocalStorageContextManager().enable());
    propagation.setGlobalPropagator(new W3CTraceContextPropagator());
    trace.setGlobalTracerProvider(out.tracer);
  }
  if (wantMetrics) {
    const intervalMs = Number(env.OTEL_METRIC_EXPORT_INTERVAL) || 60_000;
    out.meter = new MeterProvider({
      resource,
      readers: [new PeriodicExportingMetricReader({ exporter: new OTLPMetricExporter(), exportIntervalMillis: intervalMs, exportTimeoutMillis: Math.min(intervalMs, 30_000) })],
    });
    metrics.setGlobalMeterProvider(out.meter);
    resetInstruments();
  }
  installed = out;
  return true;
}

/** Sends what is buffered now. */
export async function flushTelemetry(): Promise<void> {
  await installed?.tracer?.forceFlush();
  await installed?.meter?.forceFlush();
}

/** Flushes and removes the providers (shutdown, tests). */
export async function stopTelemetry(): Promise<void> {
  const i = installed;
  installed = undefined;
  if (!i) return;
  await Promise.allSettled([i.tracer?.shutdown(), i.meter?.shutdown()]);
  trace.disable();
  metrics.disable();
  context.disable();
  propagation.disable();
  resetInstruments();
}
