/**
 * Anthropic models on the clouds' own request shapes. The body is the Messages one with the model in the URL and a
 * platform version in the body; the responses are the same events (Vertex: plain SSE; Bedrock's InvokeModel: inside an AWS
 * event stream, gateway/cloud/eventstream.ts). Bedrock's newer `bedrock-mantle` endpoint speaks plain Messages and uses the
 * "anthropic" adapter with AWS signing instead.
 */
import { eventStreamToSse } from "../cloud/eventstream.ts";
import { anthropicAdapter, toAnthropicBody } from "./anthropic.ts";
import { joinUrl, type Adapter, type AdapterTarget } from "./types.ts";

const BEDROCK_VERSION = "bedrock-2023-05-31";
const VERTEX_VERSION = "vertex-2023-10-16";

function cloudBody(req: Parameters<typeof toAnthropicBody>[0], target: AdapterTarget, version: string): Record<string, unknown> {
  const body = toAnthropicBody(req, target);
  delete body.model;
  delete body.stream;
  // Fast mode is first-party only; services/model-tiers.ts never offers it on these platforms.
  delete body.speed;
  return { anthropic_version: version, ...body };
}

export const bedrockInvokeAdapter: Adapter = {
  ...anthropicAdapter,
  buildCountTokens: undefined,
  buildRequest(req, target) {
    const model = encodeURIComponent(target.model.upstreamModel);
    return {
      url: joinUrl(target.provider.baseUrl, `/model/${model}/invoke-with-response-stream`),
      headers: { "content-type": "application/json", accept: "application/vnd.amazon.eventstream", ...target.provider.headers },
      body: cloudBody(req, target, BEDROCK_VERSION),
    };
  },
  translate(body, target) {
    return anthropicAdapter.translate(eventStreamToSse(body), target);
  },
};

export const vertexAnthropicAdapter: Adapter = {
  ...anthropicAdapter,
  buildCountTokens: undefined,
  buildRequest(req, target) {
    const body = cloudBody(req, target, VERTEX_VERSION);
    body.stream = true;
    const model = encodeURIComponent(target.model.upstreamModel);
    return {
      url: joinUrl(target.provider.baseUrl, `/publishers/anthropic/models/${model}:streamRawPredict`),
      headers: { "content-type": "application/json", accept: "text/event-stream", ...target.provider.headers },
      body,
    };
  },
};
