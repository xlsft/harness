import type { ModelEntry } from "../../services/models.ts";
import type { ProviderRuntime } from "../../services/providers.ts";
import type { FeatureName, Features } from "../features.ts";
import type { AdapterEvent, CanonicalRequest, UpstreamFailure } from "../types.ts";

export interface UpstreamRequest {
  url: string;
  headers: Record<string, string>;
  body: Record<string, unknown>;
}

export interface AdapterTarget {
  model: ModelEntry;
  provider: ProviderRuntime;
  features: Features;
}

export interface Adapter {
  buildRequest(req: CanonicalRequest, target: AdapterTarget): UpstreamRequest;
  /** Translates the upstream SSE body into adapter events. Must not throw for upstream errors: yield `{kind:"error"}`. */
  translate(body: ReadableStream<Uint8Array>, target: AdapterTarget): AsyncGenerator<AdapterEvent>;
  /** If a 4xx says the upstream does not support a feature we sent, returns that feature. */
  rejectedFeature(failure: UpstreamFailure, sent: UpstreamRequest, features: Features): FeatureName | undefined;
  buildCountTokens?(req: CanonicalRequest, target: AdapterTarget): UpstreamRequest;
}

/** Joins a provider base URL and an API path, tolerating a trailing `/v1` on the base. */
export function joinUrl(base: string, path: string): string {
  const b = base.replace(/\/+$/, "");
  if (path.startsWith("/v1/") && /\/v1$/.test(b)) return b + path.slice(3);
  return b + path;
}
