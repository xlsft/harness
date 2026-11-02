/**
 * MCP client transport over WebSocket (subprotocol "mcp"). Unlike the SDK's WebSocketClientTransport it
 * uses undici's WebSocket with the SSRF-guarded dispatcher and can send headers (tokens, static headers).
 */
import type { Transport } from "@modelcontextprotocol/sdk/shared/transport.js";
import { JSONRPCMessageSchema, type JSONRPCMessage } from "@modelcontextprotocol/sdk/types.js";
import { WebSocket } from "undici";
import { ApiError } from "../lib/errors.ts";
import { assertUrlAllowed, guardedDispatcher, ssrfError } from "../lib/ssrf.ts";

export class GuardedWebSocketTransport implements Transport {
  private socket?: WebSocket;
  onclose?: () => void;
  onerror?: (error: Error) => void;
  onmessage?: (message: JSONRPCMessage) => void;

  constructor(
    private readonly url: URL,
    private readonly headers: Record<string, string>,
    private readonly allowPrivateNetwork: boolean,
  ) {
    // Same scheme-independent checks as HTTP (IP literal, localhost); hostnames are checked at connect time.
    assertUrlAllowed(url.href.replace(/^ws(s?):/i, "http$1:"), allowPrivateNetwork);
  }

  start(): Promise<void> {
    if (this.socket) throw new Error("GuardedWebSocketTransport already started");
    return new Promise((resolve, reject) => {
      let opened = false;
      const ws = new WebSocket(this.url, {
        protocols: ["mcp"],
        headers: this.headers,
        dispatcher: guardedDispatcher(this.allowPrivateNetwork),
      });
      this.socket = ws;
      ws.onopen = () => {
        opened = true;
        resolve();
      };
      ws.onerror = (event) => {
        const raw = (event as unknown as { error?: unknown }).error;
        const err = ssrfError(raw) ?? (raw instanceof Error ? raw : new Error("WebSocket error"));
        if (!opened) reject(err instanceof ApiError ? err : new ApiError("upstream", `WebSocket connection failed: ${err.message}`));
        this.onerror?.(err);
      };
      ws.onclose = () => {
        if (!opened) reject(new ApiError("upstream", "WebSocket connection closed before it opened"));
        this.onclose?.();
      };
      ws.onmessage = (event) => {
        let message: JSONRPCMessage;
        try {
          message = JSONRPCMessageSchema.parse(JSON.parse(String(event.data)));
        } catch (error) {
          this.onerror?.(error as Error);
          return;
        }
        this.onmessage?.(message);
      };
    });
  }

  async send(message: JSONRPCMessage): Promise<void> {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) throw new Error("Not connected");
    this.socket.send(JSON.stringify(message));
  }

  async close(): Promise<void> {
    this.socket?.close();
  }
}
