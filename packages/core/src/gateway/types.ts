/**
 * Canonical (Anthropic-Messages-like) request/response types used between the client and
 * the gateway (docs/protocol/gateway.md). Transport-agnostic.
 */

export interface TextBlock {
  type: "text";
  text: string;
  cache?: boolean;
  [k: string]: unknown;
}
export interface ImageBlock {
  type: "image";
  source: { type: "base64"; media_type: string; data: string } | { type: "url"; url: string };
  cache?: boolean;
}
export interface DocumentBlock {
  type: "document";
  source: { type: "base64"; media_type: string; data: string } | { type: "text"; media_type: string; data: string };
  title?: string;
  cache?: boolean;
}
export interface ThinkingBlock {
  type: "thinking";
  thinking: string;
  signature?: string;
  cache?: boolean;
}
export interface RedactedThinkingBlock {
  type: "redacted_thinking";
  data: string;
  cache?: boolean;
}
export interface ToolUseBlock {
  type: "tool_use";
  id: string;
  name: string;
  input: unknown;
  cache?: boolean;
}
export interface ToolResultBlock {
  type: "tool_result";
  tool_use_id: string;
  is_error?: boolean;
  content?: string | (TextBlock | ImageBlock)[];
  cache?: boolean;
}

export type ContentBlock =
  | TextBlock
  | ImageBlock
  | DocumentBlock
  | ThinkingBlock
  | RedactedThinkingBlock
  | ToolUseBlock
  | ToolResultBlock;

export interface Message {
  role: "user" | "assistant";
  content: string | ContentBlock[];
}

export interface Tool {
  name: string;
  description?: string;
  input_schema: Record<string, unknown>;
  cache?: boolean;
}

export type ToolChoice =
  | { type: "auto"; disable_parallel_tool_use?: boolean }
  | { type: "any"; disable_parallel_tool_use?: boolean }
  | { type: "tool"; name: string; disable_parallel_tool_use?: boolean }
  | { type: "none" };

export type ThinkingConfig = { type: "enabled"; budget_tokens: number } | { type: "adaptive" } | { type: "disabled" };

export interface CanonicalRequest {
  model: string;
  max_tokens: number;
  system: TextBlock[];
  messages: Message[];
  tools?: Tool[];
  tool_choice?: ToolChoice;
  thinking?: ThinkingConfig;
  effort?: string;
  /** "standard", "fast" or "flex"; dropped when the model has no such tier (services/model-tiers.ts). */
  speed?: string;
  /** "5m", "1h" or "24h"; dropped when the model has no such lifetime. */
  cache_ttl?: string;
  temperature?: number;
  top_p?: number;
  top_k?: number;
  stop_sequences?: string[];
  stream: true;
  metadata?: {
    session_id?: string;
    request_class?: string;
    agent_id?: string;
    prompt_bundle_version?: string;
  };
}

/** Normalized usage (docs/protocol/gateway.md). */
export interface Usage {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
}

export type StopReason = "end_turn" | "tool_use" | "max_tokens" | "stop_sequence" | "refusal" | "pause_turn";

/** One outgoing stream event: SSE `event:` name + JSON `data:`. */
export interface GatewayEvent {
  event: string;
  data: Record<string, unknown>;
}

/**
 * Events produced by upstream adapters. Content-block events already have the exact
 * client-facing shape; the gateway owns message_start / message_delta / message_stop.
 */
export type AdapterEvent =
  | { kind: "start"; id?: string; usage?: Partial<Usage> }
  | { kind: "block"; event: "content_block_start" | "content_block_delta" | "content_block_stop"; data: Record<string, unknown> }
  | { kind: "usage"; usage: Partial<Usage> }
  | { kind: "stop"; stop_reason: StopReason | null; stop_sequence?: string | null; usage?: Partial<Usage> }
  | { kind: "error"; error: UpstreamFailure };

/** A classified upstream failure. */
export interface UpstreamFailure {
  /** Client-facing error type (README error types). */
  type: "overloaded" | "rate_limit" | "upstream" | "invalid_request" | "permission" | "internal";
  message: string;
  status?: number;
  retryable: boolean;
  retryAfterMs?: number;
  /** Raw upstream error text (for negotiation heuristics). */
  raw?: string;
}
